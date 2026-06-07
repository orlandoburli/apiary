package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// seedBoundTask creates a source-bound InternalTask with one workflow instance
// (keyed by both task id and cell id) plus a task log, mirroring how a dispatched
// GitHub-sourced task looks in the database. The cell id equals the source item id
// — the GitHub adapter sets SourceItem.ID to the issue number — which is exactly
// what a user passes to `apiary delete <issue-number>`.
func seedBoundTask(ctx context.Context, t *testing.T, dbc *db.Client, sourceID, itemID string) (taskID, instID string) {
	t.Helper()
	task := &model.InternalTask{Title: "Fix the thing", State: model.TaskStateRunning}
	binding := &model.SourceBinding{SourceID: sourceID, SourceItemID: itemID, SourceItemNumber: "#" + itemID}
	if err := dbc.CreateTaskWithBinding(ctx, task, binding); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	inst := &db.WorkflowInstance{
		ID: "wf_" + itemID, WorkflowID: "wf", TaskID: task.ID,
		CellID: itemID, SourceID: sourceID, State: db.InstanceStateRunning,
	}
	if err := dbc.CreateWorkflowInstance(ctx, inst); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	if err := dbc.WriteTaskLog(ctx, itemID, "info", "started"); err != nil {
		t.Fatalf("seed log: %v", err)
	}
	return task.ID, inst.ID
}

func openTestDB(ctx context.Context, t *testing.T) *db.Client {
	t.Helper()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "del.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })
	return dbc
}

// TestDeleteTask_Resolution proves a task is fully removed regardless of which
// reference form the user supplies: the bare source-item id (the original bug —
// `apiary delete 1956`), the source:item pair, or the canonical InternalTask id.
func TestDeleteTask_Resolution(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		ref  func(taskID, itemID string) string
	}{
		{"by bare source-item id", func(_, itemID string) string { return itemID }},
		{"by source:item", func(_, itemID string) string { return "github:" + itemID }},
		{"by internal task id", func(taskID, _ string) string { return taskID }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dbc := openTestDB(ctx, t)
			taskID, instID := seedBoundTask(ctx, t, dbc, "github", "1956")

			d := &Dispatcher{db: dbc}
			if err := d.DeleteTask(ctx, tc.ref(taskID, "1956")); err != nil {
				t.Fatalf("DeleteTask: %v", err)
			}

			if tk, err := dbc.InternalTasks().GetTask(ctx, taskID); err != nil || tk != nil {
				t.Fatalf("task row should be gone: task=%v err=%v", tk, err)
			}
			if bs, err := dbc.ListBindingsByTask(ctx, taskID); err != nil || len(bs) != 0 {
				t.Fatalf("bindings should be gone: %v err=%v", bs, err)
			}
			if inst, err := dbc.GetWorkflowInstance(ctx, instID); err != nil || inst != nil {
				t.Fatalf("instance should be gone: %v err=%v", inst, err)
			}
		})
	}
}

// TestDeleteTask_NotFound asserts an unresolvable reference yields ErrTaskNotFound,
// which the IPC handler maps to HTTP 404 (not a bare 500).
func TestDeleteTask_NotFound(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)

	d := &Dispatcher{db: dbc}
	if err := d.DeleteTask(ctx, "does-not-exist"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("want ErrTaskNotFound, got %v", err)
	}
}

// TestDeleteTask_OrphanedCell covers the stuck-queue case: a workflow instance
// keyed by a cell id whose task row is already gone. Deleting by that cell id must
// still clean up the instance without erroring on the missing task.
func TestDeleteTask_OrphanedCell(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)

	inst := &db.WorkflowInstance{ID: "wf_orphan", WorkflowID: "wf", CellID: "orphan-1", State: db.InstanceStateRunning}
	if err := dbc.CreateWorkflowInstance(ctx, inst); err != nil {
		t.Fatalf("seed instance: %v", err)
	}

	d := &Dispatcher{db: dbc}
	if err := d.DeleteTask(ctx, "orphan-1"); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if got, err := dbc.GetWorkflowInstance(ctx, "wf_orphan"); err != nil || got != nil {
		t.Fatalf("orphan instance should be gone: %v err=%v", got, err)
	}
}
