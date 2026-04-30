package testkit

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"

	_ "modernc.org/sqlite"
)

// NewAppCtx returns an *sdk.AppCtx backed by an in-memory SQLite.
// The manifest at manifestPath is parsed; if it declares a `db`
// block, every *.sql file in the manifest's `db.migrations` directory
// is applied (alphabetical order, idempotent). The ctx is torn down
// at t.Cleanup.
//
// manifestPath is a path on disk relative to the test's working dir
// (typically the package directory containing the apteva.yaml). For
// most apps that's just "apteva.yaml".
//
// Options modify the resulting AppCtx — see WithProjectID, WithEnv,
// WithConfig.
//
// One DB per call; nothing is shared between tests. This keeps tests
// independent and parallel-safe.
func NewAppCtx(t *testing.T, manifestPath string, opts ...Option) *sdk.AppCtx {
	t.Helper()
	c := resolveOptions(opts)

	// Apply env vars for the duration of the test.
	if c.projectID != "" {
		t.Setenv("APTEVA_PROJECT_ID", c.projectID)
	}
	for k, v := range c.env {
		t.Setenv(k, v)
	}

	// Load the manifest.
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("testkit: read manifest %q: %v", manifestPath, err)
	}
	manifest, err := sdk.ParseManifest(manifestBytes)
	if err != nil {
		t.Fatalf("testkit: parse manifest %q: %v", manifestPath, err)
	}

	// Open in-memory SQLite. Each call gets its own pool — the shared
	// cache mode would let other tests in the same process see each
	// other's tables, which we don't want.
	//
	// Cap the pool at one connection: with `:memory:` every fresh
	// connection in the pool gets its OWN private database, so a
	// nested query (or even a sequential one routed to a different
	// pooled connection) will see an empty schema. SetMaxOpenConns(1)
	// pins everything to the same connection that ran the migrations.
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=2000")
	if err != nil {
		t.Fatalf("testkit: open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		t.Fatalf("testkit: ping sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Run migrations if the manifest declares them. Path is resolved
	// relative to the manifest's directory.
	if manifest.DB != nil && manifest.DB.Migrations != "" {
		migDir := manifest.DB.Migrations
		if !filepath.IsAbs(migDir) {
			migDir = filepath.Join(filepath.Dir(manifestPath), migDir)
		}
		if err := applyMigrations(db, migDir); err != nil {
			t.Fatalf("testkit: migrations: %v", err)
		}
	}

	cfg := sdk.Config(c.cfg)
	ctx := sdk.NewAppCtxForTest(manifest, db, cfg, nil, nil)
	if c.emitter != nil {
		ctx.SetEmitter(c.emitter)
	}
	return ctx
}

// applyMigrations reads every *.sql in dir (sorted) and execs each
// against db. Failures bubble up. Idempotency is the migration
// author's responsibility — the testkit doesn't track applied state
// because each test gets a fresh DB.
func applyMigrations(db *sql.DB, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read migrations dir: %w", err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, f := range files {
		body, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return fmt.Errorf("read %s: %w", f, err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", f, err)
		}
	}
	return nil
}
