package improve

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// A backup made with VACUUM INTO is a plain rollback-journal file, not WAL.
// improve must still be able to analyse it: a reader has no business setting
// journal_mode, and doing so on a read-only handle fails with "attempt to
// write a readonly database" (the bug behind this test).
func TestOpenReadOnlyAcceptsVacuumIntoCopy(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live.db")
	copyPath := filepath.Join(dir, "backup.db")

	src, err := sql.Open("sqlite", "file:"+url.PathEscape(live)+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open live: %v", err)
	}
	if _, err := src.Exec(`CREATE TABLE workflow_instances (id TEXT PRIMARY KEY, state TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := src.Exec(`INSERT INTO workflow_instances VALUES ('i1', 'done')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := src.Exec(`VACUUM INTO '` + strings.ReplaceAll(copyPath, "'", "''") + `'`); err != nil {
		t.Fatalf("vacuum into: %v", err)
	}
	src.Close()

	db, err := OpenReadOnly(copyPath)
	if err != nil {
		t.Fatalf("OpenReadOnly on VACUUM INTO copy: %v", err)
	}
	defer db.Close()

	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if strings.EqualFold(mode, "wal") {
		t.Fatalf("VACUUM INTO copy should not be WAL, got %q (test no longer exercises the bug)", mode)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workflow_instances`).Scan(&n); err != nil {
		t.Fatalf("query copy: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row in copy, got %d", n)
	}

	if _, err := db.Exec(`INSERT INTO workflow_instances VALUES ('i2', 'done')`); err == nil {
		t.Fatal("handle must be read-only, but INSERT succeeded")
	}
}
