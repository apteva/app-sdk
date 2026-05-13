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
	// modernc.org/sqlite ignores `_journal_mode=…` / `_foreign_keys=…`
	// (those are mattn/go-sqlite3 syntax) — use `_pragma=` so the
	// settings actually take. journal_mode=WAL is a no-op for :memory:
	// (in-memory DBs are always "memory" mode), so we only set the
	// pragmas that matter here: foreign_keys for cascade correctness
	// and busy_timeout for symmetry with prod.
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(on)&_pragma=busy_timeout(2000)")
	if err != nil {
		t.Fatalf("testkit: open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		t.Fatalf("testkit: ping sqlite: %v", err)
	}
	// Assert FK enforcement is actually on. If the driver swaps or the
	// DSN drifts, fail loudly here rather than silently passing tests
	// that depend on ON DELETE CASCADE — the prod bug this guards
	// against was exactly that: cascades declared but not enforced.
	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("testkit: read foreign_keys pragma: %v", err)
	}
	if fk != 1 {
		t.Fatalf("testkit: foreign_keys=%d, want 1 (DSN syntax mismatch?)", fk)
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
	ctx := sdk.NewAppCtxForTest(manifest, db, cfg, c.platform, nil)
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
