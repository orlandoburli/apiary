package dashboard

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
)

// TestOpenWorkflowMonitorLoadsAllInstances verifies that pressing Enter on a task
// that fanned out to several workflows (e.g. triage → implementation) loads every
// instance, newest-first, rather than only the latest one.
func TestOpenWorkflowMonitorLoadsAllInstances(t *testing.T) {
	ctx := context.Background()
	c, err := db.New(ctx, filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	base := time.Date(2026, 6, 6, 9, 0, 0, 0, time.UTC)
	// Two workflows on the same cell: triage first, then implementation.
	_ = c.CreateWorkflowInstance(ctx, &db.WorkflowInstance{ID: "i1", WorkflowID: "triage", CellID: "ISSUE-9", State: db.InstanceStateDone, CreatedAt: base.Add(time.Minute)})
	_ = c.CreateWorkflowInstance(ctx, &db.WorkflowInstance{ID: "i2", WorkflowID: "implementation", CellID: "ISSUE-9", State: db.InstanceStateRunning, CreatedAt: base.Add(2 * time.Minute)})

	a := &App{model: NewModel(), dbConn: c}
	msg, ok := a.openWorkflowMonitorOrLogs("ISSUE-9")().(workflowMonitorMsg)
	if !ok {
		t.Fatalf("expected workflowMonitorMsg")
	}
	if len(msg.instances) != 2 {
		t.Fatalf("expected 2 instances, got %d", len(msg.instances))
	}
	// Newest-first: implementation (i2) then triage (i1).
	if msg.instances[0].ID != "i2" || msg.instances[1].ID != "i1" {
		t.Fatalf("instances not newest-first: %s, %s", msg.instances[0].ID, msg.instances[1].ID)
	}
	if msg.instances[1].Workflow != "triage" {
		t.Errorf("older instance should be the triage workflow, got %q", msg.instances[1].Workflow)
	}
}

// TestSwitchWorkflowInstanceKeys verifies [ and ] move between a task's workflow
// instances and reset the step cursor, clamping at both ends.
func TestSwitchWorkflowInstanceKeys(t *testing.T) {
	a := &App{model: NewModel()}
	insts := []*WorkflowInstanceItem{
		{ID: "i2", Workflow: "implementation", State: "running", Steps: []WorkflowStepItem{{StepID: "a"}, {StepID: "b"}}},
		{ID: "i1", Workflow: "triage", State: "done", Steps: []WorkflowStepItem{{StepID: "x"}}},
	}
	a.model.tasksTab = &TasksTab{
		View:              TaskViewWorkflow,
		WorkflowInstances: insts,
		WorkflowInstance:  insts[0],
		WorkflowStepIdx:   1,
	}
	t0 := a.model.tasksTab

	// "[" → older (triage, i1); step cursor resets.
	a.handleWorkflowMonitorKey("[")
	if t0.WorkflowInstanceIdx != 1 || t0.WorkflowInstance.ID != "i1" {
		t.Fatalf("[ should move to older instance i1, got idx=%d id=%s", t0.WorkflowInstanceIdx, t0.WorkflowInstance.ID)
	}
	if t0.WorkflowStepIdx != 0 {
		t.Errorf("step cursor should reset on switch, got %d", t0.WorkflowStepIdx)
	}

	// "[" again clamps at the oldest.
	a.handleWorkflowMonitorKey("[")
	if t0.WorkflowInstanceIdx != 1 {
		t.Errorf("[ at oldest should stay at idx 1, got %d", t0.WorkflowInstanceIdx)
	}

	// "]" → newer (implementation, i2).
	a.handleWorkflowMonitorKey("]")
	if t0.WorkflowInstanceIdx != 0 || t0.WorkflowInstance.ID != "i2" {
		t.Fatalf("] should move to newer instance i2, got idx=%d id=%s", t0.WorkflowInstanceIdx, t0.WorkflowInstance.ID)
	}

	// "]" again clamps at the newest.
	a.handleWorkflowMonitorKey("]")
	if t0.WorkflowInstanceIdx != 0 {
		t.Errorf("] at newest should stay at idx 0, got %d", t0.WorkflowInstanceIdx)
	}
}
