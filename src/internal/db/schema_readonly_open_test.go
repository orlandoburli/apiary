package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestInitSchema_DoesNotMutateData is the regression guard for #467.
//
// Every process that opens the database runs InitSchema — the daemon, the
// dashboard, `apiary memory` — and the dashboard routinely runs against the
// same WAL file as a live daemon. When the data repairs lived inside
// InitSchema, merely opening the dashboard rewrote lifecycle state and could
// drop and recreate approval_requests underneath a running engine, losing any
// row the daemon inserted mid-rebuild.
//
// InitSchema must therefore create what is missing and change nothing else.
func TestInitSchema_DoesNotMutateData(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "apiary.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	// A task that repairSupersededFailedTasks would flip from failed to done:
	// settled, and its newest terminal top-level instance succeeded.
	now := time.Now().UTC()
	seed := []struct {
		stmt string
		args []any
	}{
		{`INSERT INTO internal_tasks (id, title, state, outstanding_workflows, generation, created_at, updated_at)
		  VALUES ('task-1', 'seeded', 'failed', 0, 1, ?, ?)`, []any{now, now}},
		{`INSERT INTO workflow_instances (id, workflow_id, cell_id, task_id, state, created_at, updated_at)
		  VALUES ('inst-1', 'wf', 'cell-1', 'task-1', 'done', ?, ?)`, []any{now, now}},
	}
	for _, s := range seed {
		if _, err := c.db.ExecContext(ctx, s.stmt, s.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// A second opener — this is the dashboard, opening while a daemon runs.
	if err := InitSchema(ctx, c.db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	var got string
	if err := c.db.QueryRowContext(ctx, `SELECT state FROM internal_tasks WHERE id = 'task-1'`).Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != "failed" {
		t.Fatalf("InitSchema rewrote task state to %q; it must not touch data (#467)", got)
	}

	// The daemon, by contrast, is expected to apply the repair.
	if err := c.MigrateData(ctx); err != nil {
		t.Fatalf("MigrateData: %v", err)
	}
	if err := c.db.QueryRowContext(ctx, `SELECT state FROM internal_tasks WHERE id = 'task-1'`).Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != "done" {
		t.Fatalf("MigrateData did not apply the repair: state = %q, want done", got)
	}
}

// TestMigrateData_IsIdempotent pins that the daemon may run the migrations on
// every start, and that `apiary migrate` is safe to run on an already-migrated
// database.
func TestMigrateData_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "apiary.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	for i := range 3 {
		if err := c.MigrateData(ctx); err != nil {
			t.Fatalf("MigrateData run %d: %v", i+1, err)
		}
	}
}
