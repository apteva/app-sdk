package testkit

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

type restoreFixtureApp struct{}

func (restoreFixtureApp) Manifest() sdk.Manifest {
	return sdk.Manifest{Schema: sdk.SchemaCurrent, Name: "restore-fixture", Version: "0.0.1"}
}
func (restoreFixtureApp) OnMount(*sdk.AppCtx) error         { return nil }
func (restoreFixtureApp) OnUnmount(*sdk.AppCtx) error       { return nil }
func (restoreFixtureApp) HTTPRoutes() []sdk.Route           { return nil }
func (restoreFixtureApp) MCPTools() []sdk.Tool              { return nil }
func (restoreFixtureApp) Channels() []sdk.ChannelFactory    { return nil }
func (restoreFixtureApp) Workers() []sdk.Worker             { return nil }
func (restoreFixtureApp) EventHandlers() []sdk.EventHandler { return nil }
func (restoreFixtureApp) OnRestore(ctx context.Context, app *sdk.AppCtx, tx *sql.Tx, rc sdk.RestoreContext) error {
	if _, err := sdk.RemapInstallIDColumns(ctx, tx, rc, "routes.target_install_id"); err != nil {
		return err
	}
	// Files next to the database belong to the copy, not the fixture.
	return os.WriteFile(filepath.Join(app.DataDir(), "restored.txt"), []byte(rc.ID), 0o600)
}

func TestRunRestore(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "fixture.db")
	db, err := sql.Open("sqlite", fixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE routes (id INTEGER PRIMARY KEY, project_id TEXT, target_install_id INTEGER)`,
		`INSERT INTO routes (project_id, target_install_id) VALUES ('rt_old', 40)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	rc := sdk.RestoreContext{ID: "kit", OldProjectID: "rt_old", NewProjectID: "rt_new", InstallIDs: map[int64]int64{40: 90}}
	restored, report, err := RunRestore(t, restoreFixtureApp{}, fixture, rc)
	if err != nil {
		t.Fatal(err)
	}
	var project string
	var target int64
	if err := restored.QueryRow(`SELECT project_id, target_install_id FROM routes`).Scan(&project, &target); err != nil {
		t.Fatal(err)
	}
	if project != "rt_new" || target != 90 || report.ProjectRows["routes"] != 1 {
		t.Fatal(project, target, report)
	}
	if data, err := os.ReadFile(filepath.Join(os.Getenv("APTEVA_DATA_DIR"), "restored.txt")); err != nil || string(data) != "kit" {
		t.Fatal("hook did not see the copied data dir", err)
	}
	original, _ := sql.Open("sqlite", fixture)
	defer original.Close()
	if err := original.QueryRow(`SELECT project_id FROM routes`).Scan(&project); err != nil || project != "rt_old" {
		t.Fatal("fixture was modified", project, err)
	}
}
