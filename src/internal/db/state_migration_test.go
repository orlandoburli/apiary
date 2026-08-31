package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/state"
)

// TestMigrateStates_OnlyTouchesTerminalRows is the safety property the whole
// two-release rollout rests on (#465): a task that is running, an instance
// parked on a CI wait, and a job holding a lease must come through the migration
// untouched, so nothing is ever rewritten underneath a live engine.
func TestMigrateStates_OnlyTouchesTerminalRows(t *testing.T) {
	ctx := context.Background()
	c := newStateTestClient(t)
	now := time.Now().UTC()

	seed := []string{
		// Terminal rows in the legacy vocabulary — these SHOULD convert.
		`INSERT INTO workflow_instances (id, workflow_id, cell_id, state, created_at, updated_at)
		 VALUES ('i-term', 'w', 'c', 'done', '` + now.Format(time.DateTime) + `', '` + now.Format(time.DateTime) + `')`,
		`INSERT INTO step_runs (id, workflow_instance_id, step_id, state) VALUES ('s-passed', 'i-term', 'a', 'passed')`,
		`INSERT INTO step_runs (id, workflow_instance_id, step_id, state) VALUES ('s-cached', 'i-term', 'b', 'skipped_cached')`,

		// Live rows in the legacy vocabulary — these MUST NOT convert.
		`INSERT INTO internal_tasks (id, title, state, created_at, updated_at)
		 VALUES ('t-live', 'running task', 'registered', '` + now.Format(time.DateTime) + `', '` + now.Format(time.DateTime) + `')`,
		`INSERT INTO workflow_instances (id, workflow_id, cell_id, state, created_at, updated_at)
		 VALUES ('i-wait', 'w', 'c', 'waiting', '` + now.Format(time.DateTime) + `', '` + now.Format(time.DateTime) + `')`,
		`INSERT INTO workflow_instances (id, workflow_id, cell_id, state, created_at, updated_at)
		 VALUES ('i-appr', 'w', 'c', 'approval_waiting', '` + now.Format(time.DateTime) + `', '` + now.Format(time.DateTime) + `')`,
		`INSERT INTO step_runs (id, workflow_instance_id, step_id, state) VALUES ('s-run', 'i-wait', 'c', 'running')`,
	}
	for _, stmt := range seed {
		if _, err := c.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %s: %v", stmt, err)
		}
	}

	if err := c.MigrateData(ctx); err != nil {
		t.Fatalf("MigrateData: %v", err)
	}

	get := func(table, id string) string {
		var got string
		if err := c.db.QueryRowContext(ctx, `SELECT state FROM `+table+` WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("read %s/%s: %v", table, id, err)
		}
		return got
	}

	// Converted.
	if got := get("step_runs", "s-passed"); got != "done" {
		t.Errorf("terminal step 'passed' = %q, want done", got)
	}
	if got := get("step_runs", "s-cached"); got != "skipped" {
		t.Errorf("terminal step 'skipped_cached' = %q, want skipped", got)
	}
	var reason string
	if err := c.db.QueryRowContext(ctx, `SELECT COALESCE(skipped_reason,'') FROM step_runs WHERE id='s-cached'`).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != string(state.ReasonCached) {
		t.Errorf("skipped_reason = %q, want cached", reason)
	}

	// Untouched — the whole point.
	for _, c2 := range []struct{ table, id, want string }{
		{"internal_tasks", "t-live", "registered"},
		{"workflow_instances", "i-wait", "waiting"},
		{"workflow_instances", "i-appr", "approval_waiting"},
		{"step_runs", "s-run", "running"},
	} {
		if got := get(c2.table, c2.id); got != c2.want {
			t.Errorf("live row %s/%s = %q, want it untouched (%q)", c2.table, c2.id, got, c2.want)
		}
	}
}

// TestPropagateInterruptedToTasks covers the first of the two defects the
// canonical model exists to fix: a task whose every instance was orphaned used
// to sit in the queued state, indistinguishable from one that had just arrived.
func TestPropagateInterruptedToTasks(t *testing.T) {
	ctx := context.Background()
	c := newStateTestClient(t)
	now := time.Now().UTC()
	ts := now.Format(time.DateTime)

	mkTask := func(id, st string) {
		if _, err := c.db.ExecContext(ctx,
			`INSERT INTO internal_tasks (id, title, state, outstanding_workflows, created_at, updated_at)
			 VALUES (?, ?, ?, 0, ?, ?)`, id, id, st, ts, ts); err != nil {
			t.Fatalf("seed task: %v", err)
		}
	}
	mkInst := func(id, taskID, st, reason string) {
		if _, err := c.db.ExecContext(ctx,
			`INSERT INTO workflow_instances (id, workflow_id, cell_id, task_id, state, blocked_reason, created_at, updated_at)
			 VALUES (?, 'w', 'c', ?, ?, ?, ?, ?)`, id, taskID, st, reason, ts, ts); err != nil {
			t.Fatalf("seed instance: %v", err)
		}
	}

	// Orphaned: only instance is interrupted → should propagate.
	mkTask("t-orphan", "queued")
	mkInst("i-orphan", "t-orphan", InstanceStateBlocked, string(state.ReasonInterrupted))

	// Still alive: one interrupted, one parked on an approval → must not.
	mkTask("t-mixed", "queued")
	mkInst("i-dead", "t-mixed", InstanceStateBlocked, string(state.ReasonInterrupted))
	mkInst("i-appr", "t-mixed", InstanceStateBlocked, string(state.ReasonApproval))

	// Settled: terminal task → must not be reopened as blocked.
	mkTask("t-done", "done")
	mkInst("i-done", "t-done", InstanceStateBlocked, string(state.ReasonInterrupted))

	if _, err := c.PropagateInterruptedToTasks(ctx); err != nil {
		t.Fatalf("propagate: %v", err)
	}

	check := func(id, wantState, wantReason string) {
		t.Helper()
		var st, reason string
		if err := c.db.QueryRowContext(ctx,
			`SELECT state, COALESCE(blocked_reason,'') FROM internal_tasks WHERE id = ?`, id).Scan(&st, &reason); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if st != wantState || reason != wantReason {
			t.Errorf("task %s = (%q,%q), want (%q,%q)", id, st, reason, wantState, wantReason)
		}
	}
	check("t-orphan", "blocked", string(state.ReasonInterrupted))
	check("t-mixed", "queued", "")
	check("t-done", "done", "")
}

// TestHasActiveInstanceForRoute_InterruptedDoesNotShadow pins that an orphaned
// instance never blocks a re-dispatch. Interruption shares the 'blocked' state
// with live parks now, so only the reason keeps these apart — and getting it
// wrong would strand every workflow across a daemon restart.
func TestHasActiveInstanceForRoute_InterruptedDoesNotShadow(t *testing.T) {
	ctx := context.Background()
	c := newStateTestClient(t)

	if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
		ID: "i1", WorkflowID: "wf", CellID: "c", TaskID: "T1",
		State: InstanceStateBlocked, BlockedReason: string(state.ReasonInterrupted),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	active, err := c.HasActiveInstanceForRoute(ctx, "T1", "wf")
	if err != nil {
		t.Fatalf("has active: %v", err)
	}
	if active {
		t.Error("an interrupted instance must not shadow a re-dispatch")
	}

	if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
		ID: "i2", WorkflowID: "wf", CellID: "c", TaskID: "T1",
		State: InstanceStateBlocked, BlockedReason: string(state.ReasonApproval),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	active, err = c.HasActiveInstanceForRoute(ctx, "T1", "wf")
	if err != nil {
		t.Fatalf("has active: %v", err)
	}
	if !active {
		t.Error("an instance parked on an approval is alive and must shadow a re-dispatch")
	}
}

func newStateTestClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(context.Background(), filepath.Join(t.TempDir(), "apiary.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
