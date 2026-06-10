package db

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// PruneLogsBefore must delete service_logs and task_logs rows older than the
// cutoff — including more rows than one delete batch — while keeping recent
// ones, and report the total deleted.
func TestPruneLogsBefore(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	old := time.Now().AddDate(0, 0, -40)

	// 5001 old task_logs rows force a second delete batch (batch size 5000).
	insert := func(stmt string, args ...any) {
		t.Helper()
		if _, err := c.db.ExecContext(ctx, stmt, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	for i := 0; i < 5001; i++ {
		insert(`INSERT INTO task_logs (task_id, level, message, timestamp) VALUES (?, ?, ?, ?)`,
			"task-old", "INFO", fmt.Sprintf("old line %d", i), old)
	}
	insert(`INSERT INTO task_logs (task_id, level, message, timestamp) VALUES (?, ?, ?, ?)`,
		"task-new", "INFO", "recent line", time.Now())
	insert(`INSERT INTO service_logs (level, message, component, timestamp) VALUES (?, ?, ?, ?)`,
		"INFO", "old service line", "dispatcher", old)
	insert(`INSERT INTO service_logs (level, message, component, timestamp) VALUES (?, ?, ?, ?)`,
		"INFO", "recent service line", "dispatcher", time.Now())

	deleted, err := c.PruneLogsBefore(ctx, time.Now().AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("PruneLogsBefore: %v", err)
	}
	if deleted != 5002 {
		t.Errorf("deleted = %d, want 5002", deleted)
	}

	count := func(table string) int {
		t.Helper()
		var n int
		if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return n
	}
	if n := count("task_logs"); n != 1 {
		t.Errorf("task_logs rows = %d, want 1 (recent only)", n)
	}
	if n := count("service_logs"); n != 1 {
		t.Errorf("service_logs rows = %d, want 1 (recent only)", n)
	}

	// Pruning again with nothing in range is a no-op.
	deleted, err = c.PruneLogsBefore(ctx, time.Now().AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("PruneLogsBefore (2nd): %v", err)
	}
	if deleted != 0 {
		t.Errorf("second prune deleted = %d, want 0", deleted)
	}
}
