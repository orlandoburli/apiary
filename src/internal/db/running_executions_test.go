package db

import (
	"context"
	"testing"
	"time"
)

// seedExecution inserts a task_executions row directly. The dashboard-facing
// columns (pid, heartbeat_at, heartbeat_count) are added by migration rather
// than by the CREATE TABLE statement, so the test inserts them explicitly.
func seedExecution(t *testing.T, c *Client, id int64, taskID, agentID, status string, started time.Time, pid int, hb *time.Time) {
	t.Helper()
	ctx := context.Background()
	var hbVal any
	if hb != nil {
		hbVal = hb.UTC().Format("2006-01-02 15:04:05")
	}
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO task_executions
			(id, task_id, agent_id, title, task_number, task_url, model, runner,
			 status, started_at, pid, heartbeat_at, heartbeat_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, taskID, agentID, "task "+taskID, "REF-"+taskID, "https://example.test/"+taskID,
		"claude-opus", "cli", status, started.UTC().Format("2006-01-02 15:04:05"), pid, hbVal, 3)
	if err != nil {
		t.Fatalf("seed execution %d (%s): %v", id, taskID, err)
	}
}

// GetRunningExecutions must list exactly the executions that are in flight:
// one row per running execution, with the fields the Running tab renders.
//
// The "latest execution per task" join matters for a retried task: a task whose
// first attempt failed and whose second attempt is running must show only the
// running attempt, not one row per attempt.
func TestGetRunningExecutions(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	now := time.Now()

	// A running execution with a live heartbeat.
	seedExecution(t, c, 1, "T-1", "engineer", "running", now.Add(-2*time.Minute), 4242, &now)
	// A finished execution — must not appear.
	seedExecution(t, c, 2, "T-2", "engineer", "success", now.Add(-10*time.Minute), 0, nil)
	// A failed execution — must not appear.
	seedExecution(t, c, 3, "T-3", "reviewer", "failed", now.Add(-5*time.Minute), 0, nil)

	got, err := c.GetRunningExecutions(ctx)
	if err != nil {
		t.Fatalf("GetRunningExecutions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d running executions, want 1: %+v", len(got), got)
	}

	r := got[0]
	if r.TaskID != "T-1" {
		t.Errorf("TaskID = %q, want T-1", r.TaskID)
	}
	if r.AgentID != "engineer" {
		t.Errorf("AgentID = %q, want engineer", r.AgentID)
	}
	if r.PID != 4242 {
		t.Errorf("PID = %d, want 4242", r.PID)
	}
	if r.TaskNumber != "REF-T-1" {
		t.Errorf("TaskNumber = %q, want REF-T-1", r.TaskNumber)
	}
	if r.TaskURL != "https://example.test/T-1" {
		t.Errorf("TaskURL = %q, want the source URL", r.TaskURL)
	}
	if r.Model != "claude-opus" || r.Runner != "cli" {
		t.Errorf("Model/Runner = %q/%q, want claude-opus/cli", r.Model, r.Runner)
	}
	if r.HeartbeatAt == nil {
		t.Error("HeartbeatAt = nil, want the seeded heartbeat")
	}
	if r.HeartbeatCnt != 3 {
		t.Errorf("HeartbeatCnt = %d, want 3", r.HeartbeatCnt)
	}
	if r.StartedAt == nil {
		t.Error("StartedAt = nil, want the seeded start time")
	}
}

// A task whose earlier attempt failed and whose latest attempt is running must
// appear exactly once — the latest execution, not one row per attempt.
func TestGetRunningExecutionsLatestAttemptOnly(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	now := time.Now()

	seedExecution(t, c, 1, "T-9", "engineer", "failed", now.Add(-30*time.Minute), 0, nil)
	seedExecution(t, c, 2, "T-9", "engineer", "running", now.Add(-1*time.Minute), 777, &now)

	got, err := c.GetRunningExecutions(ctx)
	if err != nil {
		t.Fatalf("GetRunningExecutions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d running executions, want 1 (only the latest attempt): %+v", len(got), got)
	}
	if got[0].PID != 777 {
		t.Errorf("PID = %d, want 777 (the running attempt)", got[0].PID)
	}
}

// With nothing running the list is empty, not nil-with-an-error: the Running
// tab renders "no agents running" from it.
func TestGetRunningExecutionsEmpty(t *testing.T) {
	c := newTestClient(t)
	got, err := c.GetRunningExecutions(context.Background())
	if err != nil {
		t.Fatalf("GetRunningExecutions: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d running executions, want 0", len(got))
	}
}

// A row that says 'running' but whose heartbeat has gone stale must still be
// listed: the dashboard needs it to show the user that work is stuck, not to
// silently hide it. Whether it counts as live is decided by the heartbeat
// age, which the query deliberately leaves to the caller.
func TestGetRunningExecutionsIncludesStaleRows(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	now := time.Now()
	old := now.Add(-2 * time.Hour).UTC().Format("2006-01-02 15:04:05")

	seedExecution(t, c, 1, "T-stale", "janitor", "running", now.Add(-3*time.Hour), 999, nil)
	if _, err := c.db.ExecContext(ctx,
		"UPDATE task_executions SET heartbeat_at = ? WHERE id = 1", old); err != nil {
		t.Fatalf("age heartbeat: %v", err)
	}

	got, err := c.GetRunningExecutions(ctx)
	if err != nil {
		t.Fatalf("GetRunningExecutions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d running executions, want 1 (a stale row is still work in flight): %+v", len(got), got)
	}
	if got[0].HeartbeatAt == nil {
		t.Error("HeartbeatAt = nil, want the stale heartbeat so the caller can judge liveness")
	}
	if got[0].PID != 999 {
		t.Errorf("PID = %d, want 999", got[0].PID)
	}
}
