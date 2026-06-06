package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
)

func mustCreateInstance(t *testing.T, dbc *db.Client, inst *db.WorkflowInstance) {
	t.Helper()
	if err := dbc.CreateWorkflowInstance(context.Background(), inst); err != nil {
		t.Fatalf("create instance %s: %v", inst.ID, err)
	}
}

// TestTaskHistory_OrdersInstancesAndMapsStepsAndLogs verifies the IPC mapping:
// segments are oldest-first and each instance's summary/steps/logs are carried
// into the view. (Precise per-instance log windowing is covered at the db layer
// in TestGetTaskWorkflowHistory_*.)
func TestTaskHistory_OrdersInstancesAndMapsStepsAndLogs(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	const task, cell = "task-1", "issue-1948"
	// Past dates so the WriteTaskLog line (timestamped now) lands in the latest,
	// open-ended instance window regardless of when the test runs.
	t0 := time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(10 * time.Minute)

	mustCreateInstance(t, dbc, &db.WorkflowInstance{ID: "wi_inv", WorkflowID: "investigator", TaskID: task, CellID: cell, State: db.InstanceStateDone, CreatedAt: t0})
	mustCreateInstance(t, dbc, &db.WorkflowInstance{ID: "wi_impl", WorkflowID: "implementation", TaskID: task, CellID: cell, State: db.InstanceStateRunning, CreatedAt: t1})

	for _, s := range []struct{ inst, step string }{{"wi_inv", "classify"}, {"wi_inv", "triage"}, {"wi_impl", "implement"}} {
		if err := dbc.CreateStepRun(ctx, &db.StepRun{ID: s.inst + "-" + s.step, WorkflowInstanceID: s.inst, StepID: s.step, AgentID: "ag", State: db.StepStatePassed}); err != nil {
			t.Fatalf("create step %s/%s: %v", s.inst, s.step, err)
		}
	}
	_ = dbc.WriteTaskLog(ctx, cell, "INFO", "started implementation workflow")

	resp, err := (&Dispatcher{db: dbc}).TaskHistory(ctx, task)
	if err != nil {
		t.Fatalf("TaskHistory: %v", err)
	}
	if resp == nil || len(resp.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %+v", resp)
	}
	if resp.Segments[0].Instance.Workflow != "investigator" || resp.Segments[1].Instance.Workflow != "implementation" {
		t.Fatalf("segments out of order: %q then %q (want investigator then implementation)",
			resp.Segments[0].Instance.Workflow, resp.Segments[1].Instance.Workflow)
	}
	if len(resp.Segments[0].Steps) != 2 {
		t.Errorf("investigator step views = %d, want 2", len(resp.Segments[0].Steps))
	}
	if len(resp.Segments[1].Steps) != 1 || resp.Segments[1].Steps[0].StepID != "implement" {
		t.Errorf("implementation steps = %+v, want [implement]", resp.Segments[1].Steps)
	}
	if resp.Segments[1].Instance.State != db.InstanceStateRunning {
		t.Errorf("implementation state = %q, want running", resp.Segments[1].Instance.State)
	}
	if len(resp.Segments[1].Logs) != 1 || resp.Segments[1].Logs[0].Message != "started implementation workflow" {
		t.Errorf("implementation logs = %+v, want one mapped line", resp.Segments[1].Logs)
	}
}

func TestTaskHistory_NoInstancesReturnsNil(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	resp, err := (&Dispatcher{db: dbc}).TaskHistory(ctx, "nobody")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil for a task with no instances, got %+v", resp)
	}
}
