package dashboard

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

func TestDrillKeyFor(t *testing.T) {
	task := model.InternalTask{ID: "tk_1"}
	if got := drillKeyFor(task, nil); got != "tk_1" {
		t.Errorf("drillKeyFor(no bindings) = %q, want the task id tk_1", got)
	}
	bound := []model.SourceBinding{{SourceID: "github", SourceItemID: "ISSUE-9"}}
	if got := drillKeyFor(task, bound); got != "ISSUE-9" {
		t.Errorf("drillKeyFor(bound) = %q, want the primary binding's source item id ISSUE-9", got)
	}
}

func TestTaskItemFromInternal(t *testing.T) {
	created := time.Date(2026, 6, 6, 9, 0, 0, 0, time.UTC)
	updated := created.Add(5 * time.Minute)

	// Bound, done task: number/url from binding, drill key = source item id, completed set.
	done := model.InternalTask{
		ID: "tk_done", Title: "Fix login", State: model.TaskStateDone,
		CreatedAt: created, UpdatedAt: updated, OutstandingWorkflows: 0,
	}
	bindings := []model.SourceBinding{{SourceID: "github", SourceItemID: "ISSUE-9", SourceItemNumber: "#9", SourceItemURL: "https://x/9"}}
	it := taskItemFromInternal(done, bindings)
	if it.InternalTaskID != "tk_done" || it.DrillKey != "ISSUE-9" || it.TaskID != "ISSUE-9" {
		t.Errorf("ids wrong: InternalTaskID=%q DrillKey=%q TaskID=%q", it.InternalTaskID, it.DrillKey, it.TaskID)
	}
	if it.Status != "done" || it.Number != "#9" || it.URL != "https://x/9" {
		t.Errorf("mapping wrong: Status=%q Number=%q URL=%q", it.Status, it.Number, it.URL)
	}
	if it.StartedAt == nil || !it.StartedAt.Equal(created) {
		t.Errorf("StartedAt should mirror CreatedAt")
	}
	if it.CompletedAt == nil || !it.CompletedAt.Equal(updated) {
		t.Errorf("CompletedAt should mirror UpdatedAt for a done task")
	}
	if len(it.Bindings) != 1 || it.Bindings[0].ItemNumber != "#9" {
		t.Errorf("Bindings not mapped: %+v", it.Bindings)
	}

	// Binding-less running task: drill key = task id, no CompletedAt.
	running := model.InternalTask{ID: "tk_run", Title: "spawned", State: model.TaskStateRunning, CreatedAt: created}
	r := taskItemFromInternal(running, nil)
	if r.DrillKey != "tk_run" || r.TaskID != "tk_run" {
		t.Errorf("binding-less drill key should be the task id, got %q", r.DrillKey)
	}
	if r.CompletedAt != nil {
		t.Errorf("a running task should have no CompletedAt")
	}
	if len(r.Bindings) != 0 {
		t.Errorf("binding-less task should have no bindings, got %+v", r.Bindings)
	}
}

// TestFetchTaskDetailAugments exercises the full fetch glue end-to-end against a
// real DB: a spawned child task (bound, with a workflow instance and a parent)
// must come back with its bindings, root-first lineage, parent title, instances,
// and a Status that reflects the InternalTask lifecycle state.
func TestFetchTaskDetailAugments(t *testing.T) {
	ctx := context.Background()
	c, err := db.New(ctx, filepath.Join(t.TempDir(), "detail.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	ts := c.InternalTasks()
	base := time.Date(2026, 6, 6, 9, 0, 0, 0, time.UTC)

	parent := &model.InternalTask{Title: "Payments incident", State: model.TaskStateRunning, CreatedAt: base}
	if err := ts.CreateTask(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	child := &model.InternalTask{
		Title: "Collect logs", State: model.TaskStateRunning, ParentTaskID: parent.ID,
		OutstandingWorkflows: 1, CreatedAt: base.Add(time.Minute),
	}
	if err := ts.CreateTask(ctx, child); err != nil {
		t.Fatalf("create child: %v", err)
	}
	binding := &model.SourceBinding{TaskID: child.ID, SourceID: "github", SourceItemID: "ISSUE-9", SourceItemNumber: "#9", SourceItemURL: "https://x/9"}
	if err := c.SourceBindings().CreateBinding(ctx, binding); err != nil {
		t.Fatalf("create binding: %v", err)
	}
	// Two workflow instances for the child (fan-out).
	_ = c.CreateWorkflowInstance(ctx, &db.WorkflowInstance{ID: "i1", WorkflowID: "collect-logs", CellID: "ISSUE-9", TaskID: child.ID, State: db.InstanceStateRunning, CreatedAt: base.Add(2 * time.Minute)})
	_ = c.CreateWorkflowInstance(ctx, &db.WorkflowInstance{ID: "i2", WorkflowID: "notify", CellID: "ISSUE-9", TaskID: child.ID, State: db.InstanceStateDone, CreatedAt: base.Add(3 * time.Minute)})

	a := &App{model: NewModel(), dbConn: c}
	msg := a.fetchTaskDetail("ISSUE-9", child.ID)()
	dm, ok := msg.(taskDetailMsg)
	if !ok {
		t.Fatalf("expected taskDetailMsg, got %T", msg)
	}
	d := dm.detail
	if d == nil {
		t.Fatal("expected a synthesized detail for a task with no execution row")
	}
	if d.InternalTaskID != child.ID || d.DrillKey != "ISSUE-9" {
		t.Errorf("ids wrong: InternalTaskID=%q DrillKey=%q", d.InternalTaskID, d.DrillKey)
	}
	if d.Status != "running" {
		t.Errorf("detail Status = %q, want the InternalTask state 'running'", d.Status)
	}
	if d.OutstandingWorkflows != 1 {
		t.Errorf("OutstandingWorkflows = %d, want 1", d.OutstandingWorkflows)
	}
	if len(d.Bindings) != 1 || d.Bindings[0].ItemNumber != "#9" {
		t.Errorf("bindings not augmented: %+v", d.Bindings)
	}
	// Lineage root-first incl. self: [parent, child]; parent title resolved.
	if len(d.Lineage) != 2 || d.Lineage[0].Title != "Payments incident" || d.Lineage[1].Title != "Collect logs" {
		t.Fatalf("lineage = %+v, want [Payments incident, Collect logs]", d.Lineage)
	}
	if d.ParentTitle != "Payments incident" {
		t.Errorf("ParentTitle = %q, want Payments incident", d.ParentTitle)
	}
	// All instances surfaced, newest first.
	if len(d.Instances) != 2 || d.Instances[0].ID != "i2" {
		t.Fatalf("instances = %+v, want 2 newest-first (i2,i1)", d.Instances)
	}
	// The single-latest instance still drives the step-progress panel.
	if dm.instance == nil || dm.instance.ID != "i2" {
		t.Errorf("DetailInstance should be the latest instance i2, got %+v", dm.instance)
	}
}
