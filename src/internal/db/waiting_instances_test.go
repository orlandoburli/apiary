package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

// The waiting section of the Running tab lists every workflow instance that
// has not reached a terminal state, with the task title and the step that is
// open.
//
// The distinction that matters is running versus parked: an instance with an
// open step has an agent working on it, and one without is waiting — on an
// approval, a CI run, a blocker, or the debris of a run that died.
func TestListNonTerminalInstances(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "inst.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	seed := func(id, workflowID, taskID, title, instState, reason string) {
		t.Helper()
		// The instance's title comes from its task, so the task has to exist.
		if err := c.InternalTasks().CreateTask(ctx, &model.InternalTask{
			ID: taskID, Title: title, State: model.TaskStateRunning,
		}); err != nil {
			t.Fatalf("create task %s: %v", taskID, err)
		}
		inst := &WorkflowInstance{
			ID: id, WorkflowID: workflowID, TaskID: taskID, CellID: taskID,
			State: instState, BlockedReason: reason,
			CreatedAt: time.Now().Add(-time.Hour), UpdatedAt: time.Now().Add(-time.Hour),
		}
		if err := c.CreateWorkflowInstance(ctx, inst); err != nil {
			t.Fatalf("create instance %s: %v", id, err)
		}
	}

	seed("i-running", "implementation", "T-1", "Running task", "running", "")
	seed("i-ci", "implementation", "T-2", "CI task", "blocked", "ci")
	seed("i-approval", "implementation", "T-3", "Approval task", "blocked", "approval")
	seed("i-done", "implementation", "T-4", "Done task", "done", "")
	seed("i-failed", "implementation", "T-5", "Failed task", "failed", "")
	seed("i-canceled", "implementation", "T-6", "Canceled task", "canceled", "")

	got, err := c.ListNonTerminalInstances(ctx, 50)
	if err != nil {
		t.Fatalf("ListNonTerminalInstances: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d non-terminal instances, want 3 (running, ci, approval): %+v", len(got), got)
	}

	byID := map[string]WaitingInstance{}
	for _, g := range got {
		byID[g.ID] = g
	}
	if w, ok := byID["i-ci"]; !ok {
		t.Error("the CI-waiting instance is missing")
	} else {
		if w.BlockedReason != "ci" {
			t.Errorf("ci instance reason = %q, want ci", w.BlockedReason)
		}
		if w.Title != "CI task" {
			t.Errorf("ci instance title = %q, want the task's title", w.Title)
		}
		if w.WorkflowID != "implementation" {
			t.Errorf("ci instance workflow = %q, want implementation", w.WorkflowID)
		}
	}
	if w, ok := byID["i-running"]; !ok {
		t.Error("the running instance is missing")
	} else if w.BlockedReason != "" {
		t.Errorf("a running instance must not carry a blocked reason, got %q", w.BlockedReason)
	}
}

// An instance with an open step reports that step and its agent: that is what
// tells a running instance apart from a parked one.
func TestListNonTerminalInstancesReportsTheOpenStep(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "step.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	inst := &WorkflowInstance{
		ID: "i-1", WorkflowID: "implementation", TaskID: "T-1", CellID: "T-1",
		State: "running", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := c.CreateWorkflowInstance(ctx, inst); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	if _, err := c.db.ExecContext(ctx, `
		INSERT INTO step_runs (id, workflow_instance_id, step_id, agent_id, state, started_at)
		VALUES ('s-1', 'i-1', 'implement', 'engineer', 'running', ?)`, time.Now().UTC().Format("2006-01-02 15:04:05")); err != nil {
		t.Fatalf("create step run: %v", err)
	}

	got, err := c.ListNonTerminalInstances(ctx, 10)
	if err != nil {
		t.Fatalf("ListNonTerminalInstances: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d instances, want 1: %+v", len(got), got)
	}
	if got[0].StepID != "implement" {
		t.Errorf("step = %q, want implement", got[0].StepID)
	}
	if got[0].AgentID != "engineer" {
		t.Errorf("agent = %q, want engineer", got[0].AgentID)
	}
}
