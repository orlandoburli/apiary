package db

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// seedStaleExecution creates a running execution row with a controlled pid,
// heartbeat and start time.
func seedStaleExecution(t *testing.T, c *Client, id int64, taskID, agentID string, pid int, hb *time.Time, started time.Time) {
	t.Helper()
	ctx := context.Background()
	var hbVal any
	if hb != nil {
		hbVal = hb.UTC().Format("2006-01-02 15:04:05")
	}
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO task_executions
			(id, task_id, agent_id, title, status, started_at, created_at, pid, heartbeat_at, heartbeat_count)
		VALUES (?, ?, ?, 'work', 'running', ?, ?, ?, ?, 1)
	`, id, taskID, agentID, started.UTC().Format("2006-01-02 15:04:05"),
		started.UTC().Format("2006-01-02 15:04:05"), pid, hbVal)
	if err != nil {
		t.Fatalf("seed execution %d: %v", id, err)
	}
}

// The reaper closes a row whose process is provably gone, and leaves a row
// that is demonstrably alive alone.
func TestReapStaleExecutionsClosesOnlyDeadRows(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "reap.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Now()
	live := now.Add(-5 * time.Second)
	dead := now.Add(-2 * time.Hour)

	seedStaleExecution(t, c, 1, "T-live", "engineer", 111, &live, now.Add(-time.Minute))
	seedStaleExecution(t, c, 2, "T-dead", "engineer", 222, &dead, now.Add(-3*time.Hour))
	seedStaleExecution(t, c, 3, "T-done", "engineer", 333, &live, now.Add(-time.Minute))
	// The third row is not running, so it must be untouched.
	if _, err := c.db.ExecContext(ctx,
		"UPDATE task_executions SET status = 'success' WHERE id = 3"); err != nil {
		t.Fatalf("finish execution: %v", err)
	}

	n, err := c.ReapStaleExecutions(ctx, now)
	if err != nil {
		t.Fatalf("ReapStaleExecutions: %v", err)
	}
	if n != 1 {
		t.Fatalf("reaped %d rows, want 1 (only the dead one)", n)
	}

	var status string
	if err := c.db.QueryRowContext(ctx, "SELECT status FROM task_executions WHERE id = 1").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Errorf("live execution status = %q, want it left alone", status)
	}
	if err := c.db.QueryRowContext(ctx, "SELECT status FROM task_executions WHERE id = 2").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Errorf("dead execution status = %q, want failed", status)
	}
	if err := c.db.QueryRowContext(ctx, "SELECT status FROM task_executions WHERE id = 3").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "success" {
		t.Errorf("finished execution status = %q, want it untouched", status)
	}
}

// The reaper must record why it closed a row. A row whose process died
// without the runner ever reporting an exit leaves no other trace, so the
// message is the only evidence of what happened.
func TestReapStaleExecutionsRecordsWhy(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "why.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Now()
	dead := now.Add(-2 * time.Hour)
	seedStaleExecution(t, c, 1, "T-dead", "engineer", 222, &dead, now.Add(-3*time.Hour))

	if _, err := c.ReapStaleExecutions(ctx, now); err != nil {
		t.Fatalf("ReapStaleExecutions: %v", err)
	}

	var msg string
	var completedAt *time.Time
	if err := c.db.QueryRowContext(ctx,
		"SELECT error_message, completed_at FROM task_executions WHERE id = 1").Scan(&msg, &completedAt); err != nil {
		t.Fatal(err)
	}
	if msg == "" {
		t.Error("the reaped row has no error_message — the cause is lost")
	}
	if completedAt == nil {
		t.Error("the reaped row has no completed_at")
	}
}

// A row whose heartbeat has not landed yet must not be closed: the daemon
// writes the pid and the first heartbeat when the runner starts, and closing
// the row in that window would kill work that is merely about to begin.
func TestReapStaleExecutionsLeavesHeartbeatlessFreshRows(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "grace.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Now()
	seedStaleExecution(t, c, 1, "T-just-started", "engineer", 111, nil, now.Add(-time.Minute))
	seedStaleExecution(t, c, 2, "T-long-gone", "engineer", 222, nil, now.Add(-2*time.Hour))

	n, err := c.ReapStaleExecutions(ctx, now)
	if err != nil {
		t.Fatalf("ReapStaleExecutions: %v", err)
	}
	if n != 1 {
		t.Fatalf("reaped %d rows, want 1 (only the long-abandoned one)", n)
	}

	var status string
	if err := c.db.QueryRowContext(ctx, "SELECT status FROM task_executions WHERE id = 1").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Errorf("a row that just started must not be reaped, status = %q", status)
	}
}

// Reaping an execution must also close the step_run and the workflow_instance
// hanging off it. Otherwise the workflow monitor keeps rendering a phantom
// in-progress step — which is exactly the shape the janitor's stuck-scan
// reports as stuck.
func TestReapStaleExecutionsClosesTheWorkflowState(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "wf.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Now()
	dead := now.Add(-2 * time.Hour)

	inst := &WorkflowInstance{
		ID: "inst-1", WorkflowID: "implementation", CellID: "T-1", TaskID: "T-1",
		State: InstanceStateRunning, CreatedAt: now.Add(-3 * time.Hour),
	}
	if err := c.CreateWorkflowInstance(ctx, inst); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	if _, err := c.db.ExecContext(ctx, `
		INSERT INTO step_runs (id, workflow_instance_id, step_id, agent_id, state, started_at)
		VALUES ('step-1', 'inst-1', 'implement', 'engineer', 'running', ?)
	`, now.Add(-3*time.Hour).UTC().Format("2006-01-02 15:04:05")); err != nil {
		t.Fatalf("create step run: %v", err)
	}
	seedStaleExecution(t, c, 1, "T-1", "engineer", 222, &dead, now.Add(-3*time.Hour))
	// The daemon links an execution to its instance and step when it starts.
	if err := c.SetStepLink(ctx, 1, "inst-1", "implement"); err != nil {
		t.Fatalf("link execution to step: %v", err)
	}

	if _, err := c.ReapStaleExecutions(ctx, now); err != nil {
		t.Fatalf("ReapStaleExecutions: %v", err)
	}

	var stepState string
	if err := c.db.QueryRowContext(ctx,
		"SELECT state FROM step_runs WHERE id = 'step-1'").Scan(&stepState); err != nil {
		t.Fatal(err)
	}
	if stepState != StepStateBlocked {
		t.Errorf("step state = %q, want %q — a phantom running step is what the janitor's scan reports as stuck",
			stepState, StepStateBlocked)
	}

	var instState string
	if err := c.db.QueryRowContext(ctx,
		"SELECT state FROM workflow_instances WHERE id = 'inst-1'").Scan(&instState); err != nil {
		t.Fatal(err)
	}
	if instState != InstanceStateBlocked {
		t.Errorf("instance state = %q, want %q", instState, InstanceStateBlocked)
	}
}

// An instance with a live step must not be closed out from under it: only the
// step that belonged to the dead execution is closed.
func TestReapStaleExecutionsLeavesInstancesWithLiveSteps(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "live-step.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Now()
	dead := now.Add(-2 * time.Hour)

	inst := &WorkflowInstance{
		ID: "inst-1", WorkflowID: "implementation", CellID: "T-1", TaskID: "T-1",
		State: InstanceStateRunning, CreatedAt: now.Add(-3 * time.Hour),
	}
	if err := c.CreateWorkflowInstance(ctx, inst); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	// A live step under the same instance, owned by a different execution.
	if _, err := c.db.ExecContext(ctx, `
		INSERT INTO step_runs (id, workflow_instance_id, step_id, agent_id, state, started_at)
		VALUES ('step-live', 'inst-1', 'qa', 'qa', 'running', ?)
	`, now.UTC().Format("2006-01-02 15:04:05")); err != nil {
		t.Fatalf("create live step: %v", err)
	}
	seedStaleExecution(t, c, 1, "T-1", "engineer", 222, &dead, now.Add(-3*time.Hour))
	// This execution owns the 'implement' step; the 'qa' step under the same
	// instance belongs to a live run and must be left alone.
	if err := c.SetStepLink(ctx, 1, "inst-1", "implement"); err != nil {
		t.Fatalf("link execution to step: %v", err)
	}

	if _, err := c.ReapStaleExecutions(ctx, now); err != nil {
		t.Fatalf("ReapStaleExecutions: %v", err)
	}

	var instState string
	if err := c.db.QueryRowContext(ctx,
		"SELECT state FROM workflow_instances WHERE id = 'inst-1'").Scan(&instState); err != nil {
		t.Fatal(err)
	}
	if instState != InstanceStateRunning {
		t.Errorf("instance state = %q, want it left running — it still has a live step", instState)
	}
}

// Reaping twice must be harmless: the second pass finds nothing left to close.
func TestReapStaleExecutionsIsIdempotent(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "idem.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Now()
	dead := now.Add(-2 * time.Hour)
	seedStaleExecution(t, c, 1, "T-dead", "engineer", 222, &dead, now.Add(-3*time.Hour))

	n, err := c.ReapStaleExecutions(ctx, now)
	if err != nil {
		t.Fatalf("first reap: %v", err)
	}
	if n != 1 {
		t.Fatalf("first reap closed %d rows, want 1", n)
	}
	n, err = c.ReapStaleExecutions(ctx, now)
	if err != nil {
		t.Fatalf("second reap: %v", err)
	}
	if n != 0 {
		t.Fatalf("second reap closed %d rows, want 0", n)
	}
}

// The one failure that would matter: closing a run that is working.
//
// The reaper runs on a timer while the daemon is busy, so "the row says
// running" is not evidence of anything — only a fresh heartbeat is. This
// sweeps heartbeats across the window and asserts that every row the
// heartbeat says is alive survives, and every row it says is gone does not.
func TestReapStaleExecutionsNeverClosesALiveRun(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "safety.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Now()
	// Heartbeats from very fresh to very old, straddling the window.
	ages := []time.Duration{
		0,
		5 * time.Second,
		30 * time.Second,
		55 * time.Second,
		65 * time.Second,
		5 * time.Minute,
		2 * time.Hour,
	}
	for i, age := range ages {
		hb := now.Add(-age)
		seedStaleExecution(t, c, int64(i+1), fmt.Sprintf("T-%d", i), "engineer", 1000+i, &hb, now.Add(-age))
	}

	n, err := c.ReapStaleExecutions(ctx, now)
	if err != nil {
		t.Fatalf("ReapStaleExecutions: %v", err)
	}

	remaining, err := c.GetRunningExecutions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	surviving := map[string]bool{}
	for _, r := range remaining {
		surviving[r.TaskID] = true
	}

	for i, age := range ages {
		task := fmt.Sprintf("T-%d", i)
		alive := age <= ExecutionHeartbeatWindow
		if alive && !surviving[task] {
			t.Errorf("%s (heartbeat %v old) is alive but was closed — the reaper closed live work", task, age)
		}
		if !alive && surviving[task] {
			t.Errorf("%s (heartbeat %v old) is stale but was left running", task, age)
		}
	}

	wantClosed := 0
	for _, age := range ages {
		if age > ExecutionHeartbeatWindow {
			wantClosed++
		}
	}
	if int(n) != wantClosed {
		t.Errorf("reaped %d rows, want %d", n, wantClosed)
	}
}
