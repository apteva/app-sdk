package sdk

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openRestoreTestDB(t *testing.T, ddl ...string) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(on)&_pragma=busy_timeout(2000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, q := range ddl {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return db, path
}

type restoreHookApp struct {
	metaTestApp
	hook    func(context.Context, *AppCtx, *sql.Tx, RestoreContext) error
	mounted func(*AppCtx) error
	db      *DBConfig
}

func (a *restoreHookApp) Manifest() Manifest {
	m := a.metaTestApp.Manifest()
	m.Schema = SchemaCurrent
	m.Runtime.StartupTimeoutSeconds = 5
	m.DB = a.db
	return m
}
func (a *restoreHookApp) OnMount(c *AppCtx) error {
	if a.mounted != nil {
		return a.mounted(c)
	}
	return nil
}
func (a *restoreHookApp) OnRestore(ctx context.Context, app *AppCtx, tx *sql.Tx, rc RestoreContext) error {
	if a.hook != nil {
		return a.hook(ctx, app, tx, rc)
	}
	return nil
}

func scalar[T any](t *testing.T, db *sql.DB, q string, args ...any) T {
	t.Helper()
	var v T
	if err := db.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

func TestDefaultRestoreRewritesOnlyMatchingProjectRows(t *testing.T) {
	db, _ := openRestoreTestDB(t,
		`CREATE TABLE items (id INTEGER PRIMARY KEY, project_id TEXT NOT NULL, name TEXT)`,
		`CREATE TABLE notes (id INTEGER PRIMARY KEY, owner_install_id INTEGER, body TEXT)`,
		`CREATE TABLE links (id INTEGER PRIMARY KEY, project_id TEXT, install_id INTEGER)`,
		`INSERT INTO items (project_id, name) VALUES ('rt_old','a'),('other','b'),('rt_old','c')`,
		`INSERT INTO notes (owner_install_id, body) VALUES (11,'rt_old stays in text')`,
		`INSERT INTO links (project_id, install_id) VALUES ('rt_old', 11)`,
	)
	rc := RestoreContext{ID: "r1", OldProjectID: "rt_old", NewProjectID: "rt_new", InstallIDs: map[int64]int64{11: 21}}
	report, err := ApplyRestore(context.Background(), NewAppCtxForTest(nil, db, nil, nil, nil), &metaTestApp{}, rc)
	if err != nil {
		t.Fatal(err)
	}
	if report.ProjectRows["items"] != 2 || report.ProjectRows["links"] != 1 || len(report.ProjectRows) != 2 {
		t.Fatalf("report %+v", report.ProjectRows)
	}
	if n := scalar[int](t, db, `SELECT COUNT(*) FROM items WHERE project_id='rt_new'`); n != 2 {
		t.Fatalf("items rewritten %d", n)
	}
	if n := scalar[int](t, db, `SELECT COUNT(*) FROM items WHERE project_id='other'`); n != 1 {
		t.Fatal("another project's rows changed")
	}
	if body := scalar[string](t, db, `SELECT body FROM notes`); body != "rt_old stays in text" {
		t.Fatal("table without project_id changed", body)
	}
	if id := scalar[int64](t, db, `SELECT install_id FROM links`); id != 11 {
		t.Fatal("install ids must only change when an app declares the column", id)
	}
	if id := scalar[int64](t, db, `SELECT owner_install_id FROM notes`); id != 11 {
		t.Fatal("owner_install_id remapped without being declared", id)
	}
}

func TestApplyRestoreDefersForeignKeys(t *testing.T) {
	db, _ := openRestoreTestDB(t,
		`CREATE TABLE parent (project_id TEXT NOT NULL, id INTEGER NOT NULL, PRIMARY KEY (project_id, id))`,
		`CREATE TABLE child (id INTEGER PRIMARY KEY, project_id TEXT NOT NULL, parent_id INTEGER NOT NULL,
			FOREIGN KEY (project_id, parent_id) REFERENCES parent(project_id, id))`,
		`INSERT INTO parent VALUES ('rt_old', 1)`,
		`INSERT INTO child (project_id, parent_id) VALUES ('rt_old', 1)`,
	)
	rc := RestoreContext{ID: "fk", OldProjectID: "rt_old", NewProjectID: "rt_new"}
	if _, err := ApplyRestore(context.Background(), NewAppCtxForTest(nil, db, nil, nil, nil), &metaTestApp{}, rc); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, db, `SELECT COUNT(*) FROM child JOIN parent ON parent.project_id=child.project_id AND parent.id=child.parent_id WHERE child.project_id='rt_new'`); n != 1 {
		t.Fatal("rows referencing each other were not rewritten together")
	}
}

func TestApplyRestoreRunsOnce(t *testing.T) {
	db, _ := openRestoreTestDB(t,
		`CREATE TABLE items (id INTEGER PRIMARY KEY, project_id TEXT)`,
		`INSERT INTO items (project_id) VALUES ('rt_old')`,
	)
	actx := NewAppCtxForTest(nil, db, nil, nil, nil)
	calls := 0
	app := &restoreHookApp{hook: func(context.Context, *AppCtx, *sql.Tx, RestoreContext) error { calls++; return nil }}
	rc := RestoreContext{ID: "once", OldProjectID: "rt_old", NewProjectID: "rt_new"}
	if _, err := ApplyRestore(context.Background(), actx, app, rc); err != nil {
		t.Fatal(err)
	}
	// A row written under the old project after the restore must survive a
	// restart that still carries the same context.
	if _, err := db.Exec(`INSERT INTO items (project_id) VALUES ('rt_old')`); err != nil {
		t.Fatal(err)
	}
	report, err := ApplyRestore(context.Background(), actx, app, rc)
	if err != nil {
		t.Fatal(err)
	}
	if !report.AlreadyApplied || calls != 1 {
		t.Fatalf("restore ran twice: already=%v hook calls=%d", report.AlreadyApplied, calls)
	}
	if n := scalar[int](t, db, `SELECT COUNT(*) FROM items WHERE project_id='rt_old'`); n != 1 {
		t.Fatal("second run rewrote rows again")
	}
}

func TestApplyRestoreWithoutDatabaseRunsHookOnce(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APTEVA_DATA_DIR", dir)
	actx := NewAppCtxForTest(nil, nil, nil, nil, nil)
	calls := 0
	app := &restoreHookApp{hook: func(_ context.Context, _ *AppCtx, tx *sql.Tx, _ RestoreContext) error {
		if tx != nil {
			t.Fatal("tx given to an app without a database")
		}
		calls++
		return nil
	}}
	rc := RestoreContext{ID: "nodb", OldProjectID: "rt_old", NewProjectID: "rt_new"}
	if report, err := ApplyRestore(context.Background(), actx, app, rc); err != nil || report.AlreadyApplied {
		t.Fatalf("first restore: %+v %v", report, err)
	}
	report, err := ApplyRestore(context.Background(), actx, app, rc)
	if err != nil || !report.AlreadyApplied || calls != 1 {
		t.Fatalf("second restore: %+v %v hook calls=%d", report, err, calls)
	}
	// A failing hook records nothing, so the next start tries again.
	failing := &restoreHookApp{hook: func(context.Context, *AppCtx, *sql.Tx, RestoreContext) error { return errors.New("boom") }}
	rc2 := RestoreContext{ID: "nodb-2", OldProjectID: "rt_old", NewProjectID: "rt_new"}
	if _, err := ApplyRestore(context.Background(), actx, failing, rc2); err == nil {
		t.Fatal("hook error not returned")
	}
	if _, err := os.Stat(restoreMarkerFile(actx, rc2.ID)); !os.IsNotExist(err) {
		t.Fatal("failed restore recorded as applied", err)
	}
}

func TestRestorerErrorRollsBackEverything(t *testing.T) {
	db, _ := openRestoreTestDB(t,
		`CREATE TABLE items (id INTEGER PRIMARY KEY, project_id TEXT, install_id INTEGER)`,
		`INSERT INTO items (project_id, install_id) VALUES ('rt_old', 5)`,
	)
	app := &restoreHookApp{hook: func(ctx context.Context, _ *AppCtx, tx *sql.Tx, rc RestoreContext) error {
		if _, err := RemapInstallIDColumns(ctx, tx, rc, "items.install_id"); err != nil {
			return err
		}
		return errors.New("hook refused")
	}}
	rc := RestoreContext{ID: "fail", OldProjectID: "rt_old", NewProjectID: "rt_new", InstallIDs: map[int64]int64{5: 6}}
	if _, err := ApplyRestore(context.Background(), NewAppCtxForTest(nil, db, nil, nil, nil), app, rc); err == nil || !strings.Contains(err.Error(), "hook refused") {
		t.Fatal("hook error not returned", err)
	}
	if p := scalar[string](t, db, `SELECT project_id FROM items`); p != "rt_old" {
		t.Fatal("project rewrite survived a failed restore")
	}
	if id := scalar[int64](t, db, `SELECT install_id FROM items`); id != 5 {
		t.Fatal("install remap survived a failed restore")
	}
	if n := scalar[int](t, db, `SELECT COUNT(*) FROM sqlite_master WHERE name='`+restoreMarkerTable+`'`); n != 0 {
		t.Fatal("failed restore left its marker")
	}
}

func TestRemapInstallIDColumnsMapsEachValueOnce(t *testing.T) {
	db, _ := openRestoreTestDB(t,
		`CREATE TABLE refs (id INTEGER PRIMARY KEY, target_install_id INTEGER)`,
		`INSERT INTO refs (target_install_id) VALUES (5),(7),(3),(NULL)`,
	)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	// 5→7 and 7→9 overlap: chained updates would turn 5 into 9.
	rc := RestoreContext{InstallIDs: map[int64]int64{5: 7, 7: 9}}
	counts, err := RemapInstallIDColumns(context.Background(), tx, rc, "refs.target_install_id")
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if counts["refs.target_install_id"] != 2 {
		t.Fatal(counts)
	}
	rows, err := db.Query(`SELECT COALESCE(target_install_id, 0) FROM refs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var v int64
		rows.Scan(&v)
		got = append(got, v)
	}
	if fmt.Sprint(got) != "[7 9 3 0]" {
		t.Fatal(got)
	}
	if _, err := RemapInstallIDColumns(context.Background(), nil, rc, "no_dot"); err == nil {
		t.Fatal("malformed column accepted")
	}
}

func TestRemapJSONInstallIDs(t *testing.T) {
	rc := RestoreContext{InstallIDs: map[int64]int64{5: 7, 7: 9}}
	doc := `{"target_install_id":5,"nested":{"install_ids":[5,7,8],"label":"keep <b>"},"other":5,"big":12345678901234567890}`
	out, n, err := RemapJSONInstallIDs(doc, rc, "target_install_id", "install_ids")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatal("changed", n, out)
	}
	var v struct {
		Target int64 `json:"target_install_id"`
		Nested struct {
			IDs   []int64 `json:"install_ids"`
			Label string  `json:"label"`
		} `json:"nested"`
		Other int64       `json:"other"`
		Big   json.Number `json:"big"`
	}
	dec := json.NewDecoder(strings.NewReader(out))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	if v.Target != 7 || fmt.Sprint(v.Nested.IDs) != "[7 9 8]" || v.Other != 5 || v.Nested.Label != "keep <b>" || v.Big.String() != "12345678901234567890" {
		t.Fatalf("%+v %s", v, out)
	}
	same := `{ "target_install_id": 3 }`
	if got, n, _ := RemapJSONInstallIDs(same, rc, "target_install_id"); got != same || n != 0 {
		t.Fatal("unchanged document was re-encoded", got)
	}
}

func TestRewriteOrigin(t *testing.T) {
	rc := RestoreContext{PublicBaseURLs: map[string]string{
		"https://runtime.apteva.invalid/rt_a":   "https://runtime.apteva.invalid/rt_b",
		"https://runtime.apteva.invalid/rt_a_2": "https://runtime.apteva.invalid/rt_c",
	}}
	in := `["https://runtime.apteva.invalid/rt_a/cb","https://runtime.apteva.invalid/rt_a_2/cb","https://elsewhere/rt_a"]`
	want := `["https://runtime.apteva.invalid/rt_b/cb","https://runtime.apteva.invalid/rt_c/cb","https://elsewhere/rt_a"]`
	if got := RewriteOrigin(in, rc); got != want {
		t.Fatal(got)
	}
	db, _ := openRestoreTestDB(t,
		`CREATE TABLE clients (id INTEGER PRIMARY KEY, redirect_uris TEXT)`,
		`INSERT INTO clients (redirect_uris) VALUES ('`+in+`'),('["https://other/x"]')`,
	)
	tx, _ := db.Begin()
	counts, err := RewriteOriginColumns(context.Background(), tx, rc, "clients.redirect_uris")
	if err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	if got := scalar[string](t, db, `SELECT redirect_uris FROM clients WHERE id=1`); got != want {
		t.Fatal(got)
	}
	if counts["clients.redirect_uris"] != 2 { // one pass per old base URL
		t.Fatal(counts)
	}
}

func TestRestoreContextValidation(t *testing.T) {
	for name, rc := range map[string]RestoreContext{
		"no id":          {OldProjectID: "a", NewProjectID: "b"},
		"half project":   {ID: "x", OldProjectID: "a"},
		"bad install":    {ID: "x", InstallIDs: map[int64]int64{0: 2}},
		"two to one":     {ID: "x", InstallIDs: map[int64]int64{1: 3, 2: 3}},
		"empty base url": {ID: "x", PublicBaseURLs: map[string]string{"https://a": ""}},
	} {
		if rc.Validate() == nil {
			t.Fatal(name, "accepted")
		}
	}
	path := filepath.Join(t.TempDir(), "ctx.json")
	os.WriteFile(path, []byte(`{"id":"x","old_project_id":"a","new_project_id":"b","install_ids":{"5":6},"surprise":1}`), 0o600)
	if _, err := LoadRestoreContext(path); err == nil {
		t.Fatal("unknown field accepted")
	}
	os.WriteFile(path, []byte(`{"id":"x","old_project_id":"a","new_project_id":"b","install_ids":{"5":6}}`), 0o600)
	rc, err := LoadRestoreContext(path)
	if err != nil || rc.InstallIDs[5] != 6 {
		t.Fatal(rc, err)
	}
}

// runRestoreApp starts app through runApp with a restore context and returns
// the base URL, the result file path and the runApp result channel.
func runRestoreApp(t *testing.T, app *restoreHookApp, rc RestoreContext, ddl ...string) (string, string, *sql.DB, chan error, context.CancelFunc) {
	t.Helper()
	db, dbPath := openRestoreTestDB(t, ddl...)
	db.Close()
	app.db = &DBConfig{Driver: "sqlite", Path: dbPath}
	dir := t.TempDir()
	rc.ResultPath = filepath.Join(dir, "result.json")
	ctxPath := filepath.Join(dir, "context.json")
	data, _ := json.Marshal(rc)
	if err := os.WriteFile(ctxPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	t.Setenv("APTEVA_APP_PORT", fmt.Sprint(port))
	t.Setenv("APTEVA_APP_TOKEN", "")
	t.Setenv("DB_PATH", dbPath)
	t.Setenv(RestoreContextEnv, ctxPath)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runApp(ctx, app) }()
	check, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { check.Close() })
	return fmt.Sprintf("http://127.0.0.1:%d", port), rc.ResultPath, check, done, cancel
}

func readRestoreResult(t *testing.T, path string) RestoreResult {
	t.Helper()
	var r RestoreResult
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunAppFailedRestoreServesNothing(t *testing.T) {
	mounted := false
	app := &restoreHookApp{
		hook:    func(context.Context, *AppCtx, *sql.Tx, RestoreContext) error { return errors.New("cannot map tenant") },
		mounted: func(*AppCtx) error { mounted = true; return nil },
	}
	rc := RestoreContext{ID: "boot-fail", OldProjectID: "rt_old", NewProjectID: "rt_new"}
	base, result, db, done, cancel := runRestoreApp(t, app, rc,
		`CREATE TABLE items (id INTEGER PRIMARY KEY, project_id TEXT)`,
		`INSERT INTO items (project_id) VALUES ('rt_old')`)
	defer cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cannot map tenant") {
			t.Fatal("startup did not fail with the hook error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startup did not stop after a failed restore")
	}
	if mounted {
		t.Fatal("OnMount ran after a failed restore")
	}
	if r := readRestoreResult(t, result); r.Status != "failed" || !strings.Contains(r.Error, "cannot map tenant") || r.ID != "boot-fail" {
		t.Fatalf("result %+v", r)
	}
	if resp, err := (&http.Client{Timeout: time.Second}).Get(base + "/mcp"); err == nil {
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Fatal("tools served after a failed restore")
		}
	}
	if p := scalar[string](t, db, `SELECT project_id FROM items`); p != "rt_old" {
		t.Fatal("failed restore changed data", p)
	}
}

func TestRunAppRestoreRunsBeforeMountAndReportsHealth(t *testing.T) {
	var seenAtMount string
	app := &restoreHookApp{mounted: func(c *AppCtx) error {
		return c.AppDB().QueryRow(`SELECT project_id FROM items`).Scan(&seenAtMount)
	}}
	rc := RestoreContext{ID: "boot-ok", OldProjectID: "rt_old", NewProjectID: "rt_new"}
	base, result, _, done, cancel := runRestoreApp(t, app, rc,
		`CREATE TABLE items (id INTEGER PRIMARY KEY, project_id TEXT)`,
		`INSERT INTO items (project_id) VALUES ('rt_old')`)
	defer cancel()
	client := &http.Client{Timeout: time.Second}
	var body string
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		resp, err := client.Get(base + "/health")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				body = string(b)
				break
			}
		}
		select {
		case err := <-done:
			t.Fatal("startup failed", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	var health struct {
		Restore   string `json:"restore"`
		RestoreID string `json:"restore_id"`
	}
	if json.Unmarshal([]byte(body), &health) != nil || health.Restore != RestoreStatusApplied || health.RestoreID != "boot-ok" {
		t.Fatalf("health %q", body)
	}
	if seenAtMount != "rt_new" {
		t.Fatal("OnMount saw unrestored data", seenAtMount)
	}
	if r := readRestoreResult(t, result); r.Status != RestoreStatusApplied || r.Report == nil || r.Report.ProjectRows["items"] != 1 {
		t.Fatalf("result %+v", r)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("app did not stop")
	}
}
