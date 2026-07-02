package db

import (
	"context"
	"testing"

	"github.com/orlandoburli/apiary/internal/model"
)

// taskGeneration reads a task's dispatch generation. The column is SQL-only
// (not surfaced on model.InternalTask), so tests query it directly.
func taskGeneration(t *testing.T, c *Client, id string) int {
	t.Helper()
	var g int
	if err := c.db.QueryRowContext(context.Background(),
		`SELECT generation FROM internal_tasks WHERE id = ?`, id).Scan(&g); err != nil {
		t.Fatalf("read task generation: %v", err)
	}
	return g
}

func instanceGeneration(t *testing.T, c *Client, id string) int {
	t.Helper()
	var g int
	if err := c.db.QueryRowContext(context.Background(),
		`SELECT task_generation FROM workflow_instances WHERE id = ?`, id).Scan(&g); err != nil {
		t.Fatalf("read instance generation: %v", err)
	}
	return g
}

// TestInternalTask_IncrementBumpsGenerationOnReopen locks in when the dispatch
// generation advances: only when a positive increment reopens a task from a
// terminal state. Mid-round increments (registered/running) must not bump, or
// same-round siblings would land in different generations.
func TestInternalTask_IncrementBumpsGenerationOnReopen(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	store := c.InternalTasks()

	task := &model.InternalTask{Title: "t"}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}
	if g := taskGeneration(t, c, task.ID); g != 0 {
		t.Fatalf("fresh task generation = %d, want 0", g)
	}

	// Increment while registered: no bump.
	if _, err := store.IncrementOutstanding(ctx, task.ID, 2); err != nil {
		t.Fatalf("increment (registered): %v", err)
	}
	if g := taskGeneration(t, c, task.ID); g != 0 {
		t.Errorf("increment while registered bumped generation to %d, want 0", g)
	}

	// Increment while running: no bump.
	if err := store.UpdateTaskState(ctx, task.ID, model.TaskStateRunning); err != nil {
		t.Fatalf("set running: %v", err)
	}
	if _, err := store.IncrementOutstanding(ctx, task.ID, 1); err != nil {
		t.Fatalf("increment (running): %v", err)
	}
	if g := taskGeneration(t, c, task.ID); g != 0 {
		t.Errorf("increment while running bumped generation to %d, want 0", g)
	}

	// Reopen from done: bump.
	if err := store.UpdateTaskState(ctx, task.ID, model.TaskStateDone); err != nil {
		t.Fatalf("set done: %v", err)
	}
	if _, err := store.IncrementOutstanding(ctx, task.ID, 1); err != nil {
		t.Fatalf("increment (done): %v", err)
	}
	if g := taskGeneration(t, c, task.ID); g != 1 {
		t.Errorf("reopen from done: generation = %d, want 1", g)
	}
	got, _ := store.GetTask(ctx, task.ID)
	if got.State != model.TaskStateRunning {
		t.Errorf("reopen from done: state = %q, want running", got.State)
	}

	// Reopen from failed: bump again.
	if err := store.UpdateTaskState(ctx, task.ID, model.TaskStateFailed); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	if _, err := store.IncrementOutstanding(ctx, task.ID, 1); err != nil {
		t.Fatalf("increment (failed): %v", err)
	}
	if g := taskGeneration(t, c, task.ID); g != 2 {
		t.Errorf("reopen from failed: generation = %d, want 2", g)
	}
}

func TestCreateWorkflowInstance_StampsTaskGeneration(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	store := c.InternalTasks()

	task := &model.InternalTask{Title: "t", State: model.TaskStateDone}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Reopen once so the task sits at generation 1.
	if _, err := store.IncrementOutstanding(ctx, task.ID, 1); err != nil {
		t.Fatalf("increment: %v", err)
	}

	if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
		ID: "i1", WorkflowID: "impl", CellID: "1", TaskID: task.ID, State: InstanceStateRunning,
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	if g := instanceGeneration(t, c, "i1"); g != 1 {
		t.Errorf("instance generation = %d, want the task's current generation 1", g)
	}

	// Transient/unknown task ids stamp generation 0.
	if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
		ID: "i2", WorkflowID: "impl", CellID: "2", TaskID: "not-a-task", State: InstanceStateRunning,
	}); err != nil {
		t.Fatalf("create transient instance: %v", err)
	}
	if g := instanceGeneration(t, c, "i2"); g != 0 {
		t.Errorf("transient instance generation = %d, want 0", g)
	}
	if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
		ID: "i3", WorkflowID: "impl", CellID: "3", State: InstanceStateRunning,
	}); err != nil {
		t.Fatalf("create task-less instance: %v", err)
	}
	if g := instanceGeneration(t, c, "i3"); g != 0 {
		t.Errorf("task-less instance generation = %d, want 0", g)
	}
}

// TestHasFailedInstance_GenerationScoped is the core of the stuck-failed-task
// fix: a failed instance from an earlier round must stop counting once a
// re-dispatch reopens the task under a new generation, while failures within
// the current round still fail the task.
func TestHasFailedInstance_GenerationScoped(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	store := c.InternalTasks()

	task := &model.InternalTask{Title: "t"}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}
	has := func() bool {
		ok, err := c.HasFailedInstance(ctx, task.ID)
		if err != nil {
			t.Fatalf("has failed instance: %v", err)
		}
		return ok
	}

	if has() {
		t.Fatal("no instances yet: want false")
	}

	// Round 0 failure counts.
	if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
		ID: "g0-fail", WorkflowID: "impl", CellID: "1", TaskID: task.ID, State: InstanceStateFailed,
	}); err != nil {
		t.Fatalf("create g0 instance: %v", err)
	}
	if !has() {
		t.Fatal("current-generation failure: want true")
	}

	// Task settles failed, then a re-dispatch reopens it → new generation.
	// The stale failure no longer counts.
	if err := store.UpdateTaskState(ctx, task.ID, model.TaskStateFailed); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	if _, err := store.IncrementOutstanding(ctx, task.ID, 1); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if has() {
		t.Fatal("previous-generation failure still counted after reopen: want false")
	}

	// A failure in the new round counts again.
	if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
		ID: "g1-fail", WorkflowID: "impl-10x", CellID: "1", TaskID: task.ID, State: InstanceStateFailed,
	}); err != nil {
		t.Fatalf("create g1 instance: %v", err)
	}
	if !has() {
		t.Fatal("new-generation failure: want true")
	}
}

// TestCountConsecutiveFailedInstances_AcrossGenerations locks in that the
// re-dispatch failure cap (settings.max_attempts) is NOT generation-scoped:
// every re-dispatch bumps the generation, so a scoped count would never exceed
// one and the cap would never fire. Only a later success resets it.
func TestCountConsecutiveFailedInstances_AcrossGenerations(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	store := c.InternalTasks()

	task := &model.InternalTask{Title: "t"}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}

	fail := func(id string) {
		if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
			ID: id, WorkflowID: "impl", CellID: "1", TaskID: task.ID, State: InstanceStateFailed,
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	fail("f1") // generation 0
	if err := store.UpdateTaskState(ctx, task.ID, model.TaskStateFailed); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	if _, err := store.IncrementOutstanding(ctx, task.ID, 1); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	fail("f2") // generation 1

	n, err := c.CountConsecutiveFailedInstances(ctx, task.ID, "impl")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("consecutive failures across generations = %d, want 2", n)
	}
}

// TestRepairSupersededFailedTasks covers the one-shot migration repair for
// databases written by builds that aggregated failures across the task's whole
// history: settled 'failed' tasks whose newest terminal top-level instance is
// done are flipped to 'done'; everything else is left alone.
func TestRepairSupersededFailedTasks(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	store := c.InternalTasks()

	mkTask := func(title string) *model.InternalTask {
		t.Helper()
		task := &model.InternalTask{Title: title, State: model.TaskStateFailed}
		if err := store.CreateTask(ctx, task); err != nil {
			t.Fatalf("create task %s: %v", title, err)
		}
		return task
	}
	mkInst := func(id, taskID, state, parent string) {
		t.Helper()
		if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
			ID: id, WorkflowID: "impl", CellID: "1", TaskID: taskID,
			State: state, ParentInstanceID: parent,
		}); err != nil {
			t.Fatalf("create instance %s: %v", id, err)
		}
	}
	state := func(id string) model.TaskState {
		t.Helper()
		got, err := store.GetTask(ctx, id)
		if err != nil || got == nil {
			t.Fatalf("get task %s: %v (%v)", id, err, got)
		}
		return got.State
	}

	// (i) failed task whose newest terminal instance is done → flipped.
	superseded := mkTask("superseded")
	mkInst("s-old-fail", superseded.ID, InstanceStateFailed, "")
	mkInst("s-new-done", superseded.ID, InstanceStateDone, "")

	// (ii) failed task whose newest terminal instance is failed → untouched.
	stillFailed := mkTask("still-failed")
	mkInst("r-old-done", stillFailed.ID, InstanceStateDone, "")
	mkInst("r-new-fail", stillFailed.ID, InstanceStateFailed, "")

	// (iii) task with outstanding workflows → untouched even with a newest done.
	inFlight := mkTask("in-flight")
	if _, err := store.IncrementOutstanding(ctx, inFlight.ID, 1); err != nil {
		t.Fatalf("increment: %v", err)
	}
	// Reopen flipped it to running; put it back to failed with the counter held.
	if err := store.UpdateTaskState(ctx, inFlight.ID, model.TaskStateFailed); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	mkInst("p-done", inFlight.ID, InstanceStateDone, "")

	// (iv) genuine single failure → untouched.
	genuine := mkTask("genuine")
	mkInst("g-fail", genuine.ID, InstanceStateFailed, "")

	// (v) done sub-workflow child under a failed top-level parent → untouched:
	// child rows are inserted before the parent settles, so by-rowid ordering
	// must not read the done child as "newest terminal".
	childDone := mkTask("child-done")
	mkInst("c-parent-fail", childDone.ID, InstanceStateFailed, "")
	mkInst("c-child-done", childDone.ID, InstanceStateDone, "c-parent-fail")

	if err := repairSupersededFailedTasks(ctx, c.db); err != nil {
		t.Fatalf("repair: %v", err)
	}

	if s := state(superseded.ID); s != model.TaskStateDone {
		t.Errorf("superseded task = %q, want done", s)
	}
	if s := state(stillFailed.ID); s != model.TaskStateFailed {
		t.Errorf("still-failed task = %q, want failed", s)
	}
	if s := state(inFlight.ID); s != model.TaskStateFailed {
		t.Errorf("in-flight task = %q, want failed (outstanding > 0)", s)
	}
	if s := state(genuine.ID); s != model.TaskStateFailed {
		t.Errorf("genuine failure = %q, want failed", s)
	}
	if s := state(childDone.ID); s != model.TaskStateFailed {
		t.Errorf("done-child-under-failed-parent = %q, want failed", s)
	}

	// Idempotent: a second run changes nothing.
	if err := repairSupersededFailedTasks(ctx, c.db); err != nil {
		t.Fatalf("repair (second run): %v", err)
	}
	if s := state(superseded.ID); s != model.TaskStateDone {
		t.Errorf("superseded task after re-run = %q, want done", s)
	}
}
