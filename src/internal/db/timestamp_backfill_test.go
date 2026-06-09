package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeGoTime(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"2026-06-06 18:14:37.630756 -0400 -04 m=+301.172927834", "2026-06-06 18:14:37.630756-04:00", true},
		{"2026-06-06 18:17:07 -0400 -04 m=+451.0", "2026-06-06 18:17:07-04:00", true},
		{"2026-06-07 00:00:55.034507 +0000 UTC", "2026-06-07 00:00:55.034507+00:00", true},
		// Already-canonical values are never passed in by the caller, but if they
		// were, the single-space form has too few fields and is left untouched.
		{"2026-06-05 08:39:10.944026-04:00", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := normalizeGoTime(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("normalizeGoTime(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestNormalizeLegacyTimestamps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	ctx := context.Background()
	c, err := New(ctx, path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	// Seed a broken Go-native timestamp directly (bypassing the now-fixed writer).
	broken := "2026-06-06 18:14:37.630756 -0400 -04 m=+301.172927834"
	if _, err := c.db.ExecContext(ctx,
		`INSERT INTO task_executions (task_id, agent_id, status, created_at, cost_usd, total_tokens)
		 VALUES ('tk','ag','success', ?, 1.25, 100)`, broken); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Before normalization DATE() cannot parse it.
	var dnull sql.NullString
	_ = c.db.QueryRowContext(ctx, `SELECT DATE(created_at) FROM task_executions`).Scan(&dnull)
	if dnull.Valid {
		t.Fatalf("expected DATE() to be NULL on broken value, got %q", dnull.String)
	}

	if err := normalizeLegacyTimestamps(ctx, c.db); err != nil {
		t.Fatalf("normalize: %v", err)
	}

	// CAST(... AS TEXT) reads the raw stored bytes; a plain string scan would get
	// modernc's RFC3339 auto-reformat instead of the on-disk value.
	var day, raw string
	if err := c.db.QueryRowContext(ctx, `SELECT DATE(created_at), CAST(created_at AS TEXT) FROM task_executions`).Scan(&day, &raw); err != nil {
		t.Fatalf("post-scan: %v", err)
	}
	if day != "2026-06-06" {
		t.Errorf("DATE(created_at) = %q, want 2026-06-06 (raw=%q)", day, raw)
	}
	if raw != "2026-06-06 18:14:37.630756-04:00" {
		t.Errorf("normalized value = %q", raw)
	}

	// Idempotent: a second pass changes nothing and leaves zero broken rows.
	if err := normalizeLegacyTimestamps(ctx, c.db); err != nil {
		t.Fatalf("normalize 2nd: %v", err)
	}
	var broke int
	_ = c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_executions WHERE created_at LIKE '% % %'`).Scan(&broke)
	if broke != 0 {
		t.Errorf("expected 0 broken rows after normalize, got %d", broke)
	}
}

// TestNormalizeRealDBCopy runs the backfill against a real DB snapshot when
// APIARY_TEST_DB points at one, then asserts no broken timestamps remain. Skipped
// in CI. Use: APIARY_TEST_DB=/tmp/apiary-test-copy.db go test -run RealDBCopy ./internal/db/
func TestNormalizeRealDBCopy(t *testing.T) {
	path := os.Getenv("APIARY_TEST_DB")
	if path == "" {
		t.Skip("set APIARY_TEST_DB to a DB copy to run")
	}
	ctx := context.Background()
	c, err := New(ctx, path) // New runs InitSchema -> normalizeLegacyTimestamps
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	for _, col := range []struct{ table, column string }{
		{"task_executions", "created_at"},
		{"step_runs", "started_at"},
		{"workflow_instances", "created_at"},
		{"task_logs", "timestamp"},
		{"service_logs", "timestamp"},
	} {
		var n int
		q := "SELECT COUNT(*) FROM " + col.table + " WHERE " + col.column + " LIKE '% % %'"
		if err := c.db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatalf("%s.%s: %v", col.table, col.column, err)
		}
		if n != 0 {
			t.Errorf("%s.%s still has %d broken rows", col.table, col.column, n)
		}
	}
}
