package testkit

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/apteva/app-sdk"

	_ "modernc.org/sqlite"
)

// RunRestore applies rc to a copy of fixture exactly as a sidecar does when
// it starts on data restored from a snapshot: DefaultRestore, then the app's
// sdk.Restorer hook, in one transaction that records rc.ID. The fixture itself
// is never modified.
//
// fixture is either a SQLite file, copied to <dataDir>/app.db, or a data
// directory copied as a whole (its app.db, if present, is the app database).
// DB_PATH and APTEVA_DATA_DIR point at the copy for the duration of the test,
// so hooks that read files next to the database see the copied ones.
//
// It returns the restored database (nil when the fixture has no app.db), the
// report and the restore error, so tests can assert both outcomes.
func RunRestore(t *testing.T, app sdk.App, fixture string, rc sdk.RestoreContext, opts ...Option) (*sql.DB, *sdk.RestoreReport, error) {
	t.Helper()
	c := resolveOptions(opts)
	dir := t.TempDir()
	info, err := os.Stat(fixture)
	if err != nil {
		t.Fatalf("testkit: restore fixture: %v", err)
	}
	dbPath := filepath.Join(dir, "app.db")
	if info.IsDir() {
		if err := copyTree(fixture, dir); err != nil {
			t.Fatalf("testkit: copy restore fixture: %v", err)
		}
	} else {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if _, err := os.Stat(fixture + suffix); err != nil {
				continue
			}
			if err := copyFile(fixture+suffix, dbPath+suffix); err != nil {
				t.Fatalf("testkit: copy restore fixture: %v", err)
			}
		}
	}
	t.Setenv("DB_PATH", dbPath)
	t.Setenv("APTEVA_DATA_DIR", dir)
	if c.projectID != "" {
		t.Setenv("APTEVA_PROJECT_ID", c.projectID)
	}
	for k, v := range c.env {
		t.Setenv(k, v)
	}
	var db *sql.DB
	if _, err := os.Stat(dbPath); err == nil {
		db, err = sql.Open("sqlite", dbPath+"?_pragma=foreign_keys(on)&_pragma=busy_timeout(2000)")
		if err != nil {
			t.Fatalf("testkit: open restored db: %v", err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
	}
	manifest := app.Manifest()
	actx := sdk.NewAppCtxForTest(&manifest, db, sdk.Config(c.cfg), c.platform, nil)
	report, err := sdk.ApplyRestore(context.Background(), actx, app, rc)
	return db, report, err
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
