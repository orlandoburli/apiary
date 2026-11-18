package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// An abandoned instance must be closed: nothing is working on it and nothing
// is waiting to start it.
//
// The periodic reaper closes an execution whose process is gone, and the
// instance that execution belonged to. It works *from* a running execution, so
// it cannot reach an instance whose execution was already closed — or that
// never had one. Those accumulate, one per dead routine run, and the
// dashboard's waiting section lists them as "run died" for as long as the
// database lives.
func TestReconcileAbandonedInstancesClosesDeadOnes(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "abandoned.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// A dead instance: interrupted, with a step that never finished.
	if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
		ID: "i-dead", WorkflowID: "routine", TaskID: "T1", CellID: "T1",
		State: InstanceStateBlocked, BlockedReason: "interrupted",
		CreatedAt: time.Now().Add(-time.Hour), UpdatedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	if _, err := c.db.ExecContext(ctx, `
		INSERT INTO step_runs (id, workflow_instance_id, step_id, agent_id, state, started_at)
		VALUES ('s-dead', 'i-dead', 'check', 'janitor', 'blocked', ?)`, time.Now().UTC().Format("2006-01-02 15:04:05")); err != nil {
		t.Fatalf("create step run: %v", err)
	}

	// The legacy state name, from a database written before the canonical
	// vocabulary. It must be cleaned up too.
	if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
		ID: "i-legacy", WorkflowID: "routine", TaskID: "T2", CellID: "T2",
		State:     "interrupted",
		CreatedAt: time.Now().Add(-time.Hour), UpdatedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("create legacy instance: %v", err)
	}

	n, err := c.ReconcileAbandonedInstances(ctx)
	if err != nil {
		t.Fatalf("ReconcileAbandonedInstances: %v", err)
	}
	if n != 2 {
		t.Fatalf("closed %d instances, want 2 (the dead one and the legacy one)", n)
	}

	for _, id := range []string{"i-dead", "i-legacy"} {
		var state, reason string
		if err := c.db.QueryRowContext(ctx,
			"SELECT state, COALESCE(blocked_reason,'') FROM workflow_instances WHERE id = ?", id).
			Scan(&state, &reason); err != nil {
			t.Fatal(err)
		}
		if state != InstanceStateFailed {
			t.Errorf("instance %s = %q, want %q — a terminal state is what drops it from the waiting list and makes the pass idempotent", id, state, InstanceStateFailed)
		}
		if reason != "" {
			t.Errorf("instance %s kept blocked_reason %q; a closed instance has no reason left", id, reason)
		}
	}

	// The step left open under it is closed too, or the dashboard renders a
	// phantom in-progress step under an instance nobody is working on.
	var stepState string
	var finished *time.Time
	if err := c.db.QueryRowContext(ctx,
		"SELECT state, finished_at FROM step_runs WHERE id = 's-dead'").
		Scan(&stepState, &finished); err != nil {
		t.Fatal(err)
	}
	if stepState != StepStateFailed {
		t.Errorf("step state = %q, want %q", stepState, StepStateFailed)
	}
	if finished == nil {
		t.Error("the abandoned step has no finished_at")
	}
}

// A workflow that is genuinely waiting must not be closed. It is waiting for
// something that may still arrive, and closing it would throw away pending
// work.
func TestReconcileAbandonedInstancesLeavesWaitingOnes(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "waiting.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	for _, spec := range []struct{ id, reason string }{
		{"i-ci", "ci"},
		{"i-approval", "approval"},
		{"i-dependency", "dependency"},
	} {
		if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
			ID: spec.id, WorkflowID: "implementation", TaskID: "T1", CellID: "T1",
			State: InstanceStateBlocked, BlockedReason: spec.reason,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("create instance %s: %v", spec.id, err)
		}
	}

	n, err := c.ReconcileAbandonedInstances(ctx)
	if err != nil {
		t.Fatalf("ReconcileAbandonedInstances: %v", err)
	}
	if n != 0 {
		t.Fatalf("closed %d instances, want 0 — a workflow waiting on CI or a human is not abandoned", n)
	}
}

// An instance with an open step has an agent working on it, whatever the
// heartbeat says.
func TestReconcileAbandonedInstancesLeavesAnInstanceWithALiveStep(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
		ID: "i-live", WorkflowID: "implementation", TaskID: "T1", CellID: "T1",
		State: InstanceStateBlocked, BlockedReason: "interrupted",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	if _, err := c.db.ExecContext(ctx, `
		INSERT INTO step_runs (id, workflow_instance_id, step_id, agent_id, state, started_at)
		VALUES ('s-live', 'i-live', 'implement', 'engineer', 'running', ?)`, time.Now().UTC().Format("2006-01-02 15:04:05")); err != nil {
		t.Fatalf("create step run: %v", err)
	}

	n, err := c.ReconcileAbandonedInstances(ctx)
	if err != nil {
		t.Fatalf("ReconcileAbandonedInstances: %v", err)
	}
	if n != 0 {
		t.Fatalf("closed %d instances, want 0 — an agent is still working on it", n)
	}
}

// An instance with a running execution is being worked on, even though its
// step row says otherwise.
func TestReconcileAbandonedInstancesLeavesAnInstanceWithALiveExecution(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "liveexec.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
		ID: "i-live-exec", WorkflowID: "implementation", TaskID: "T1", CellID: "T1",
		State: InstanceStateBlocked, BlockedReason: "interrupted",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	exec, err := c.CreateExecution(ctx, "T1", "engineer", "work", "T1", "", "m", "cli", 1)
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}
	if err := c.SetPID(ctx, exec.ID, 4242); err != nil {
		t.Fatalf("set pid: %v", err)
	}

	n, err := c.ReconcileAbandonedInstances(ctx)
	if err != nil {
		t.Fatalf("ReconcileAbandonedInstances: %v", err)
	}
	if n != 0 {
		t.Fatalf("closed %d instances, want 0 — an execution is still running on it", n)
	}
}

// An instance a queued job is waiting to start is a promise the run is still
// coming, so it must not be closed.
func TestReconcileAbandonedInstancesLeavesAnInstanceAJobWillStart(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "job.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
		ID: "i-queued", WorkflowID: "implementation", TaskID: "T1", CellID: "T1",
		State: InstanceStateBlocked, BlockedReason: "interrupted",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	// A queued job that reserves this instance.
	if _, err := c.db.ExecContext(ctx, `
		INSERT INTO dispatch_jobs
			(id, idempotency_key, task_id, workflow_id, payload, payload_version, state, attempt_count, max_attempts, available_at, created_at, updated_at)
		VALUES ('j-1', 'T1:0:implementation', 'T1', 'implementation',
		        json_object('match', json_object('instance_id', 'i-queued')), 1,
		        'queued', 0, 3, ?, ?, ?)`,
		time.Now().UTC().Format("2006-01-02 15:04:05"),
		time.Now().UTC().Format("2006-01-02 15:04:05"),
		time.Now().UTC().Format("2006-01-02 15:04:05")); err != nil {
		t.Fatalf("create job: %v", err)
	}

	n, err := c.ReconcileAbandonedInstances(ctx)
	if err != nil {
		t.Fatalf("ReconcileAbandonedInstances: %v", err)
	}
	if n != 0 {
		t.Fatalf("closed %d instances, want 0 — a queued job is waiting to start it", n)
	}
}

// Cleaning up twice is harmless.
func TestReconcileAbandonedInstancesIsIdempotent(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "idem.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
		ID: "i-1", WorkflowID: "routine", TaskID: "T1", CellID: "T1",
		State: InstanceStateBlocked, BlockedReason: "interrupted",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}

	if n, err := c.ReconcileAbandonedInstances(ctx); err != nil || n != 1 {
		t.Fatalf("first pass: n=%d err=%v, want 1", n, err)
	}
	if n, err := c.ReconcileAbandonedInstances(ctx); err != nil || n != 0 {
		t.Fatalf("second pass: n=%d err=%v, want 0", n, err)
	}
}
