package sdk

// restore.go — starting a sidecar on data copied from a snapshot.
//
// A runtime snapshot copies each app's data directory. When the platform
// restores it into a new runtime, the copied rows still name the project and
// app installations of the runtime they were captured from. The platform then
// starts the sidecar with APTEVA_RESTORE_CONTEXT pointing at a JSON file that
// maps the old identities to the new ones, and the SDK rewrites the data
// before the app mounts:
//
//	migrations → restore (one transaction) → OnMount → ready
//
// The restore transaction runs DefaultRestore (project_id columns, matched by
// value) and then the app's own Restorer hook, and records the restore id so a
// later restart with the same context does nothing. The outcome is written to
// the context's result file and reported by /health as "restore": "applied".
// A failure rolls the transaction back, records the error in the result file
// and stops the sidecar before any route or tool is served.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RestoreContextEnv names the environment variable holding the path of the
// restore context file.
const RestoreContextEnv = "APTEVA_RESTORE_CONTEXT"

// RestoreStatusApplied is the /health "restore" value after a restore context
// was applied (or found already applied).
const RestoreStatusApplied = "applied"

// RestoreContext maps the identities copied from a snapshot to the ones of
// the runtime the data now belongs to.
type RestoreContext struct {
	// ID identifies this restore. Applying the same ID twice is a no-op.
	ID           string `json:"id"`
	OldProjectID string `json:"old_project_id"`
	NewProjectID string `json:"new_project_id"`
	// InstallIDs maps every app installation captured in the snapshot from
	// its old ID to its new one.
	InstallIDs map[int64]int64 `json:"install_ids,omitempty"`
	// PublicBaseURLs maps old public base URLs to new ones, e.g.
	// https://runtime.apteva.invalid/rt_old → https://runtime.apteva.invalid/rt_new.
	PublicBaseURLs map[string]string `json:"public_base_urls,omitempty"`
	// ResultPath, when set, receives a RestoreResult written by the SDK.
	ResultPath string `json:"result_path,omitempty"`
}

// InstallID returns the new installation ID for old, if the snapshot
// captured that installation.
func (rc RestoreContext) InstallID(old int64) (int64, bool) {
	id, ok := rc.InstallIDs[old]
	return id, ok
}

// Validate reports a context that cannot be applied safely.
func (rc RestoreContext) Validate() error {
	if strings.TrimSpace(rc.ID) == "" {
		return errors.New("restore context: id required")
	}
	if (rc.OldProjectID == "") != (rc.NewProjectID == "") {
		return errors.New("restore context: old_project_id and new_project_id must both be set")
	}
	seen := make(map[int64]int64, len(rc.InstallIDs))
	for old, next := range rc.InstallIDs {
		if old <= 0 || next <= 0 {
			return fmt.Errorf("restore context: invalid install id mapping %d→%d", old, next)
		}
		if prior, dup := seen[next]; dup {
			return fmt.Errorf("restore context: installs %d and %d both map to %d", prior, old, next)
		}
		seen[next] = old
	}
	for old, next := range rc.PublicBaseURLs {
		if strings.TrimSpace(old) == "" || strings.TrimSpace(next) == "" {
			return errors.New("restore context: empty public base URL mapping")
		}
	}
	return nil
}

// LoadRestoreContext reads and validates a restore context file.
func LoadRestoreContext(path string) (RestoreContext, error) {
	var rc RestoreContext
	data, err := os.ReadFile(path)
	if err != nil {
		return rc, fmt.Errorf("read restore context: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rc); err != nil {
		return rc, fmt.Errorf("parse restore context: %w", err)
	}
	return rc, rc.Validate()
}

// Restorer is implemented by apps whose data needs more than DefaultRestore:
// install IDs in their own columns, IDs inside JSON, stored public URLs, or
// identities folded into keys. OnRestore runs inside the restore transaction,
// after DefaultRestore and before OnMount; returning an error rolls back the
// whole restore and stops the sidecar. app gives access to DataDir, config
// and logging for data kept outside the app database; it is not mounted yet.
// tx is nil when the manifest declares no database; the restore id is then
// recorded under DataDir, so a restart still runs the hook only once.
type Restorer interface {
	OnRestore(ctx context.Context, app *AppCtx, tx *sql.Tx, rc RestoreContext) error
}

// RestoreReport describes one applied restore.
type RestoreReport struct {
	ID string `json:"id"`
	// AlreadyApplied is true when the database had already recorded this
	// restore ID, so nothing was changed this time.
	AlreadyApplied bool `json:"already_applied,omitempty"`
	// ProjectRows counts rows whose project_id DefaultRestore rewrote, per table.
	ProjectRows map[string]int64 `json:"project_rows,omitempty"`
}

// RestoreResult is the file the SDK writes to RestoreContext.ResultPath.
type RestoreResult struct {
	ID     string         `json:"id"`
	Status string         `json:"status"` // "applied" or "failed"
	Error  string         `json:"error,omitempty"`
	Report *RestoreReport `json:"report,omitempty"`
	At     time.Time      `json:"at"`
}

const restoreMarkerTable = "_apteva_restores"

// ApplyRestore runs one restore against actx's app database: DefaultRestore
// and then the app's Restorer hook, inside a single transaction that also
// records rc.ID. Apps without a database only get their hook. It is what the
// SDK runs at startup and what testkit.RunRestore runs in tests.
func ApplyRestore(ctx context.Context, actx *AppCtx, app App, rc RestoreContext) (*RestoreReport, error) {
	if err := rc.Validate(); err != nil {
		return nil, err
	}
	report := &RestoreReport{ID: rc.ID}
	restorer, _ := app.(Restorer)
	var db *sql.DB
	if actx != nil {
		db = actx.AppDB()
	}
	if db == nil {
		// Without an app database the restore id is recorded as a file in
		// the data dir, so a restart with the same context skips the hook.
		marker := restoreMarkerFile(actx, rc.ID)
		if marker != "" {
			if _, err := os.Stat(marker); err == nil {
				report.AlreadyApplied = true
				return report, nil
			}
		}
		if restorer != nil {
			if err := restorer.OnRestore(ctx, actx, nil, rc); err != nil {
				return nil, fmt.Errorf("OnRestore: %w", err)
			}
		}
		if marker != "" {
			if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
				return nil, fmt.Errorf("record restore: %w", err)
			}
			if err := os.WriteFile(marker, []byte(rc.ID), 0o600); err != nil {
				return nil, fmt.Errorf("record restore: %w", err)
			}
		}
		return report, nil
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Rows that reference each other by project are rewritten table by
	// table; check foreign keys once everything is consistent again.
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+restoreMarkerTable+` (
		id TEXT PRIMARY KEY,
		old_project_id TEXT NOT NULL,
		new_project_id TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return nil, fmt.Errorf("create restore marker: %w", err)
	}
	var seen int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+restoreMarkerTable+` WHERE id = ?`, rc.ID).Scan(&seen); err != nil {
		return nil, err
	}
	if seen > 0 {
		report.AlreadyApplied = true
		return report, tx.Commit()
	}
	rows, err := DefaultRestore(ctx, tx, rc)
	if err != nil {
		return nil, err
	}
	report.ProjectRows = rows
	if restorer != nil {
		if err := restorer.OnRestore(ctx, actx, tx, rc); err != nil {
			return nil, fmt.Errorf("OnRestore: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+restoreMarkerTable+` (id, old_project_id, new_project_id, applied_at) VALUES (?, ?, ?, ?)`,
		rc.ID, rc.OldProjectID, rc.NewProjectID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return nil, fmt.Errorf("record restore: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit restore: %w", err)
	}
	return report, nil
}

// DefaultRestore rewrites project_id from the old project to the new one in
// every table that has that column. Only rows holding exactly the old project
// ID change. Install IDs and JSON documents are left alone: an app maps those
// in its Restorer with RemapInstallIDColumns, RemapJSONInstallIDs and
// RewriteOriginColumns. Virtual tables backed by external content are skipped;
// their content table's triggers keep them in sync.
func DefaultRestore(ctx context.Context, tx *sql.Tx, rc RestoreContext) (map[string]int64, error) {
	if rc.OldProjectID == "" || rc.OldProjectID == rc.NewProjectID {
		return nil, nil
	}
	tables, err := restorableTables(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, table := range tables {
		has, err := tableHasColumn(ctx, tx, table, "project_id")
		if err != nil {
			return nil, err
		}
		if !has {
			continue
		}
		res, err := tx.ExecContext(ctx, `UPDATE `+quoteIdent(table)+` SET project_id = ? WHERE project_id = ?`, rc.NewProjectID, rc.OldProjectID)
		if err != nil {
			return nil, fmt.Errorf("restore %s.project_id: %w", table, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			out[table] = n
		}
	}
	return out, nil
}

// RemapInstallIDColumns maps platform install IDs stored in the given
// "table.column" integer columns. Only values captured in the snapshot change,
// and every column is rewritten in one statement so overlapping old and new
// IDs cannot be mapped twice. It returns the rows changed per column.
func RemapInstallIDColumns(ctx context.Context, tx *sql.Tx, rc RestoreContext, columns ...string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(rc.InstallIDs) == 0 {
		return out, nil
	}
	olds := sortedOldInstallIDs(rc)
	for _, spec := range columns {
		table, column, err := splitColumn(spec)
		if err != nil {
			return nil, err
		}
		col := quoteIdent(column)
		var b strings.Builder
		args := make([]any, 0, len(olds)*3)
		b.WriteString(`UPDATE ` + quoteIdent(table) + ` SET ` + col + ` = CASE ` + col)
		for _, old := range olds {
			b.WriteString(` WHEN ? THEN ?`)
			args = append(args, old, rc.InstallIDs[old])
		}
		b.WriteString(` ELSE ` + col + ` END WHERE ` + col + ` IN (` + strings.TrimSuffix(strings.Repeat("?,", len(olds)), ",") + `)`)
		for _, old := range olds {
			args = append(args, old)
		}
		res, err := tx.ExecContext(ctx, b.String(), args...)
		if err != nil {
			return nil, fmt.Errorf("remap %s: %w", spec, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			out[spec] = n
		}
	}
	return out, nil
}

// RemapJSONInstallIDs maps install IDs inside a JSON document: every number,
// or array of numbers, stored under one of keys (at any depth) that the
// snapshot captured is replaced by its new ID. It returns the document and how
// many values changed; an unchanged document is returned byte for byte.
func RemapJSONInstallIDs(doc string, rc RestoreContext, keys ...string) (string, int, error) {
	if strings.TrimSpace(doc) == "" || len(rc.InstallIDs) == 0 || len(keys) == 0 {
		return doc, 0, nil
	}
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return doc, 0, fmt.Errorf("parse JSON: %w", err)
	}
	wanted := make(map[string]bool, len(keys))
	for _, k := range keys {
		wanted[k] = true
	}
	changed := 0
	mapNumber := func(x any) any {
		n, ok := x.(json.Number)
		if !ok {
			return x
		}
		old, err := strconv.ParseInt(n.String(), 10, 64)
		if err != nil {
			return x
		}
		if next, ok := rc.InstallIDs[old]; ok {
			changed++
			return json.Number(strconv.FormatInt(next, 10))
		}
		return x
	}
	var walk func(any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case map[string]any:
			for k, val := range t {
				if wanted[k] {
					switch inner := val.(type) {
					case json.Number:
						t[k] = mapNumber(inner)
						continue
					case []any:
						for i := range inner {
							inner[i] = mapNumber(inner[i])
						}
						continue
					}
				}
				t[k] = walk(val)
			}
		case []any:
			for i := range t {
				t[i] = walk(t[i])
			}
		}
		return x
	}
	v = walk(v)
	if changed == 0 {
		return doc, 0, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return doc, 0, err
	}
	return strings.TrimSuffix(buf.String(), "\n"), changed, nil
}

// RewriteOrigin replaces every old public base URL in s with its new one,
// longest first so a base URL that prefixes another cannot shadow it.
func RewriteOrigin(s string, rc RestoreContext) string {
	for _, old := range sortedBaseURLs(rc) {
		s = strings.ReplaceAll(s, old, rc.PublicBaseURLs[old])
	}
	return s
}

// RewriteOriginColumns applies RewriteOrigin to the given "table.column" text
// columns (plain text or JSON) and returns the rows changed per column.
func RewriteOriginColumns(ctx context.Context, tx *sql.Tx, rc RestoreContext, columns ...string) (map[string]int64, error) {
	out := map[string]int64{}
	olds := sortedBaseURLs(rc)
	if len(olds) == 0 {
		return out, nil
	}
	for _, spec := range columns {
		table, column, err := splitColumn(spec)
		if err != nil {
			return nil, err
		}
		col := quoteIdent(column)
		for _, old := range olds {
			res, err := tx.ExecContext(ctx, `UPDATE `+quoteIdent(table)+` SET `+col+` = replace(`+col+`, ?, ?) WHERE instr(`+col+`, ?) > 0`, old, rc.PublicBaseURLs[old], old)
			if err != nil {
				return nil, fmt.Errorf("rewrite %s: %w", spec, err)
			}
			if n, _ := res.RowsAffected(); n > 0 {
				out[spec] += n
			}
		}
	}
	return out, nil
}

// restoreAtStartup applies the context named by APTEVA_RESTORE_CONTEXT, if
// any, and writes the result file. A nil report means no restore was asked.
func restoreAtStartup(ctx context.Context, actx *AppCtx, app App, logger Logger) (*RestoreReport, error) {
	path := strings.TrimSpace(os.Getenv(RestoreContextEnv))
	if path == "" {
		return nil, nil
	}
	rc, err := LoadRestoreContext(path)
	if err != nil {
		// Without a readable context there is no result path either; the
		// platform sees the sidecar exit before readiness.
		return nil, err
	}
	report, err := ApplyRestore(ctx, actx, app, rc)
	result := RestoreResult{ID: rc.ID, Status: RestoreStatusApplied, Report: report, At: time.Now().UTC()}
	if err != nil {
		result = RestoreResult{ID: rc.ID, Status: "failed", Error: err.Error(), At: time.Now().UTC()}
	}
	if rc.ResultPath != "" {
		if werr := writeRestoreResult(rc.ResultPath, result); werr != nil && err == nil {
			err = fmt.Errorf("write restore result: %w", werr)
		}
	}
	if err != nil {
		return nil, err
	}
	if logger != nil {
		tables := make([]string, 0, len(report.ProjectRows))
		for t, n := range report.ProjectRows {
			tables = append(tables, fmt.Sprintf("%s=%d", t, n))
		}
		sort.Strings(tables)
		logger.Info("restore applied", "id", rc.ID, "already_applied", report.AlreadyApplied, "project_rows", strings.Join(tables, ","))
	}
	return report, nil
}

// restoreMarkerFile is where an app without a database records an applied
// restore id: <DataDir>/.apteva-restores/<sha256(id)>. It is "" when the app
// has no data dir.
func restoreMarkerFile(actx *AppCtx, id string) string {
	if actx == nil {
		return ""
	}
	dir := actx.DataDir()
	if dir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(dir, ".apteva-restores", hex.EncodeToString(sum[:]))
}

func writeRestoreResult(path string, result RestoreResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".restore-result-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func restorableTables(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name, COALESCE(sql, '') FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name, ddl string
		if err := rows.Scan(&name, &ddl); err != nil {
			return nil, err
		}
		if strings.HasPrefix(name, "sqlite_") || name == "_migrations" || strings.HasPrefix(name, "_apteva_") {
			continue
		}
		upper := strings.ToUpper(ddl)
		if strings.HasPrefix(strings.TrimSpace(upper), "CREATE VIRTUAL TABLE") && strings.Contains(strings.ReplaceAll(upper, " ", ""), "CONTENT=") {
			continue
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func tableHasColumn(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func splitColumn(spec string) (string, string, error) {
	table, column, ok := strings.Cut(spec, ".")
	if !ok || strings.TrimSpace(table) == "" || strings.TrimSpace(column) == "" || strings.Contains(column, ".") {
		return "", "", fmt.Errorf("column %q must be table.column", spec)
	}
	return table, column, nil
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func sortedOldInstallIDs(rc RestoreContext) []int64 {
	olds := make([]int64, 0, len(rc.InstallIDs))
	for old := range rc.InstallIDs {
		olds = append(olds, old)
	}
	sort.Slice(olds, func(i, j int) bool { return olds[i] < olds[j] })
	return olds
}

func sortedBaseURLs(rc RestoreContext) []string {
	olds := make([]string, 0, len(rc.PublicBaseURLs))
	for old, next := range rc.PublicBaseURLs {
		if old != "" && old != next {
			olds = append(olds, old)
		}
	}
	sort.Slice(olds, func(i, j int) bool {
		if len(olds[i]) != len(olds[j]) {
			return len(olds[i]) > len(olds[j])
		}
		return olds[i] < olds[j]
	})
	return olds
}
