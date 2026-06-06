package db

import (
	"context"
	"testing"
	"time"
)

// seedHistory creates one instance bound to (taskID, cellID) at createdAt with the
// given workflow id/state, plus one step run per step id (state "passed").
func seedHistory(t *testing.T, c *Client, instID, workflowID, taskID, cellID, state string, createdAt time.Time, stepIDs ...string) {
	t.Helper()
	ctx := context.Background()
	inst := &WorkflowInstance{
		ID:         instID,
		WorkflowID: workflowID,
		TaskID:     taskID,
		CellID:     cellID,
		State:      state,
		CreatedAt:  createdAt, // CreateWorkflowInstance honours a preset CreatedAt
	}
	if err := c.CreateWorkflowInstance(ctx, inst); err != nil {
		t.Fatalf("seed instance %s: %v", instID, err)
	}
	for i, sid := range stepIDs {
		_, err := c.db.ExecContext(ctx,
			`INSERT INTO step_runs (id, workflow_instance_id, step_id, agent_id, state) VALUES (?, ?, ?, ?, ?)`,
			instID+"-s"+itoa(i), instID, sid, "ag", StepStatePassed)
		if err != nil {
			t.Fatalf("seed step %s/%s: %v", instID, sid, err)
		}
	}
}

// writeLogAt inserts a task_logs row at an explicit timestamp (WriteTaskLog uses
// time.Now(), so the test inserts directly for deterministic windowing).
func writeLogAt(t *testing.T, c *Client, cellID, msg string, at time.Time) {
	t.Helper()
	_, err := c.db.ExecContext(context.Background(),
		`INSERT INTO task_logs (task_id, level, message, timestamp) VALUES (?, ?, ?, ?)`,
		cellID, "INFO", msg, at)
	if err != nil {
		t.Fatalf("seed log %q: %v", msg, err)
	}
}

func itoa(i int) string { return string(rune('0' + i)) }

func logMessages(segs []TaskHistorySegment, i int) []string {
	var out []string
	for _, l := range segs[i].Logs {
		out = append(out, l.Message)
	}
	return out
}

func TestGetTaskWorkflowHistory_HandoffSplitsLogsByInstance(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	const task, cell = "task-1", "issue-1948"
	t0 := time.Date(2026, 6, 6, 14, 0, 0, 0, time.UTC)
	t1 := t0.Add(10 * time.Minute) // implementation starts later

	// investigator ran first, then handed off to implementation (same cell).
	seedHistory(t, c, "wi_inv", "investigator", task, cell, InstanceStateDone, t0, "classify", "triage")
	seedHistory(t, c, "wi_impl", "implementation", task, cell, InstanceStateRunning, t1, "implement")

	writeLogAt(t, c, cell, "picked up issue #1948", t0.Add(1*time.Minute)) // → investigator window
	writeLogAt(t, c, cell, "APIARY_PUBLISH → implementation", t0.Add(2*time.Minute))
	writeLogAt(t, c, cell, "started implementation workflow", t1.Add(1*time.Minute)) // → implementation window

	segs, err := c.GetTaskWorkflowHistory(ctx, task)
	if err != nil {
		t.Fatalf("GetTaskWorkflowHistory: %v", err)
	}
	if len(segs) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segs))
	}

	// Oldest-first: investigator, then implementation.
	if segs[0].Instance.WorkflowID != "investigator" {
		t.Errorf("segment 0 workflow = %q, want investigator (oldest first)", segs[0].Instance.WorkflowID)
	}
	if segs[1].Instance.WorkflowID != "implementation" {
		t.Errorf("segment 1 workflow = %q, want implementation", segs[1].Instance.WorkflowID)
	}

	// Steps preserved per instance.
	if len(segs[0].Steps) != 2 || segs[0].Steps[0].StepID != "classify" || segs[0].Steps[1].StepID != "triage" {
		t.Errorf("investigator steps = %+v, want [classify triage]", stepIDsOf(segs[0].Steps))
	}
	if len(segs[1].Steps) != 1 || segs[1].Steps[0].StepID != "implement" {
		t.Errorf("implementation steps = %+v, want [implement]", stepIDsOf(segs[1].Steps))
	}

	// Logs partitioned by window: investigator owns the first two, not the third.
	inv := logMessages(segs, 0)
	if len(inv) != 2 {
		t.Fatalf("investigator logs = %v, want 2 lines", inv)
	}
	for _, m := range inv {
		if m == "started implementation workflow" {
			t.Errorf("implementation log leaked into investigator window: %v", inv)
		}
	}
	impl := logMessages(segs, 1)
	if len(impl) != 1 || impl[0] != "started implementation workflow" {
		t.Errorf("implementation logs = %v, want [started implementation workflow]", impl)
	}
}

func TestGetTaskWorkflowHistory_SingleInstanceOpenWindow(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	const task, cell = "task-2", "cell-2"
	t0 := time.Date(2026, 6, 6, 9, 0, 0, 0, time.UTC)
	seedHistory(t, c, "wi_only", "solo", task, cell, InstanceStateRunning, t0, "do")
	writeLogAt(t, c, cell, "line a", t0.Add(time.Minute))
	writeLogAt(t, c, cell, "line b", t0.Add(time.Hour)) // open-ended window must still capture it

	segs, err := c.GetTaskWorkflowHistory(ctx, task)
	if err != nil {
		t.Fatalf("GetTaskWorkflowHistory: %v", err)
	}
	if len(segs) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segs))
	}
	if got := len(segs[0].Logs); got != 2 {
		t.Errorf("single open-ended window captured %d logs, want 2", got)
	}
}

func TestGetTaskWorkflowHistory_NoInstances(t *testing.T) {
	c := newTestClient(t)
	segs, err := c.GetTaskWorkflowHistory(context.Background(), "task-unknown")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(segs) != 0 {
		t.Errorf("expected no segments for unknown task, got %d", len(segs))
	}
}

func stepIDsOf(steps []StepRun) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.StepID
	}
	return out
}
