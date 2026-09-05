package sdk

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrationAtomicReceiptAndLegacyRebuild(t *testing.T) {
	db, e := sql.Open("sqlite", filepath.Join(t.TempDir(), "m.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, e = db.Exec("PRAGMA foreign_keys=ON;CREATE TABLE _migrations(filename TEXT PRIMARY KEY)"); e != nil {
		t.Fatal(e)
	}
	if e = applyMigration(db, "bad.sql", "CREATE TABLE partial(id INTEGER);INSERT INTO missing VALUES(1);"); e == nil {
		t.Fatal("expected failure")
	}
	var n int
	db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='partial'").Scan(&n)
	if n != 0 {
		t.Fatal("partially committed migration")
	}
	e = applyMigration(db, "good.sql", `PRAGMA foreign_keys=OFF;
BEGIN IMMEDIATE;
CREATE TABLE parent(id INTEGER PRIMARY KEY);
CREATE TABLE child(id INTEGER REFERENCES parent(id));
CREATE TABLE log(id INTEGER);
CREATE TRIGGER audit AFTER INSERT ON parent
BEGIN
 INSERT INTO log VALUES(new.id);
END;
INSERT INTO parent VALUES(1);
INSERT INTO child VALUES(1);
COMMIT;
PRAGMA foreign_keys=ON;`)
	if e != nil {
		t.Fatal(e)
	}
	db.QueryRow("PRAGMA foreign_keys").Scan(&n)
	if n != 1 {
		t.Fatal("foreign keys not restored")
	}
	db.QueryRow("SELECT count(*) FROM log").Scan(&n)
	if n != 1 {
		t.Fatal("trigger not preserved")
	}
	db.QueryRow("SELECT count(*) FROM _migrations").Scan(&n)
	if n != 1 {
		t.Fatal("receipt missing")
	}
	e = applyMigration(db, "broken.sql", `PRAGMA foreign_keys=OFF;
INSERT INTO child VALUES(99);
PRAGMA foreign_keys=ON;`)
	if e == nil {
		t.Fatal("accepted foreign key violation")
	}
	db.QueryRow("SELECT count(*) FROM child").Scan(&n)
	if n != 1 {
		t.Fatal("bad foreign key committed")
	}
}
