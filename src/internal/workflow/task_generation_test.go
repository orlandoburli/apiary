package workflow

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// dbTracker adapts *db.Client to TaskTracker exactly like the daemon's
// dbTaskTracker, so these tests exercise the real generation-scoped
// HasFailedInstance query instead of the fakeTracker's boolean flag.
type dbTracker struct{ c *db.Client }

func (t dbTracker) DecrementOutstanding(ctx context.Context, taskID string) (int, error) {
	return t.c.InternalTasks().DecrementOutstanding(ctx, taskID)
}
func (t dbTracker) HasFailedInstance(ctx context.Context, taskID string) (bool, error) {
	return t.c.HasFailedInstance(ctx, taskID)
}
func (t dbTracker) SetTaskState(ctx context.Context, taskID string, state model.TaskState) error {
	return t.c.InternalTasks().UpdateTaskState(ctx, taskID, state)
}

// generationTestEnv wires an engine to a real SQLite db.Client (Store and
// TaskTracker both) with the tasks: completion hook configured.
func generationTestEnv(t *testing.T) (*db.Client, *Engine, *fakeExecutor, *fakeSide) {
	t.Helper()
	client, err := db.New(context.Background(), filepath.Join(t.TempDir(), "wf.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	exec := &fakeExecutor{results: map[string]StepResult{}}
	side := &fakeSide{}
	eng := trackerEngine(taskHookCfg(), client, exec, side, dbTracker{client})
	return client, eng, exec, side
}

func createTask(t *testing.T, client *db.Client, title string) model.InternalTask {
	t.Helper()
	task := &model.InternalTask{Title: title}
	if err := client.InternalTasks().CreateTask(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return *task
}

func scriptStep(exec *fakeExecutor, stepID string, res StepResult) {
	exec.mu.Lock()
	exec.results[stepID] = res
	exec.mu.Unlock()
}

func taskState(t *testing.T, client *db.Client, id string) model.TaskState {
	t.Helper()
	got, err := client.InternalTasks().GetTask(context.Background(), id)
	if err != nil || got == nil {
		t.Fatalf("get task: %v (%v)", err, got)
	}
	return got.State
}

func lastHook(t *testing.T, side *fakeSide) config.OnComplete {
	t.Helper()
	if len(side.hooks) == 0 {
		t.Fatal("no tasks: hook fired")
	}
	return side.hooks[len(side.hooks)-1]
}

// TestEngine_RedispatchAfterFailureMarksTaskDone is the production reproducer
// for the stuck-failed-task bug: a workflow fails and settles the task as
// failed; a later re-dispatch of the same workflow (fanOut-style
// IncrementOutstanding reopen) succeeds. The task must settle done — before
// generation scoping, the round-one failed instance kept it failed forever.
func TestEngine_RedispatchAfterFailureMarksTaskDone(t *testing.T) {
	ctx := context.Background()
	client, eng, exec, side := generationTestEnv(t)
	task := createTask(t, client, "t")
	tasks := client.InternalTasks()

	// Round 1: dispatch fails.
	if _, err := tasks.IncrementOutstanding(ctx, task.ID, 1); err != nil {
		t.Fatalf("increment round 1: %v", err)
	}
	scriptStep(exec, "a", StepResult{Success: false, Output: "boom"})
	if _, _, err := eng.RunInstance(ctx, oneStepWF("impl", "a"), task); err != nil {
		t.Fatalf("RunInstance round 1: %v", err)
	}
	if s := taskState(t, client, task.ID); s != model.TaskStateFailed {
		t.Fatalf("after round 1: task = %q, want failed", s)
	}

	// Round 2: re-dispatch of the same workflow succeeds.
	if _, err := tasks.IncrementOutstanding(ctx, task.ID, 1); err != nil {
		t.Fatalf("increment round 2: %v", err)
	}
	scriptStep(exec, "a", StepResult{Success: true, Output: "ok"})
	if _, _, err := eng.RunInstance(ctx, oneStepWF("impl", "a"), task); err != nil {
		t.Fatalf("RunInstance round 2: %v", err)
	}
	if s := taskState(t, client, task.ID); s != model.TaskStateDone {
		t.Errorf("after successful re-dispatch: task = %q, want done", s)
	}
	if h := lastHook(t, side); h.SetState != "all_done" {
		t.Errorf("last tasks: hook = %q, want on_complete (all_done)", h.SetState)
	}
}

// TestEngine_EscalationWorkflowMarksTaskDone covers the cross-workflow round:
// the first workflow fails, an escalation route dispatches a DIFFERENT workflow
// that succeeds (project-erp's implementation → implementation-10x chain). The
// task settles done even though the first workflow's newest instance is still
// failed.
func TestEngine_EscalationWorkflowMarksTaskDone(t *testing.T) {
	ctx := context.Background()
	client, eng, exec, side := generationTestEnv(t)
	task := createTask(t, client, "t")
	tasks := client.InternalTasks()

	if _, err := tasks.IncrementOutstanding(ctx, task.ID, 1); err != nil {
		t.Fatalf("increment round 1: %v", err)
	}
	scriptStep(exec, "a", StepResult{Success: false, Output: "boom"})
	if _, _, err := eng.RunInstance(ctx, oneStepWF("impl", "a"), task); err != nil {
		t.Fatalf("RunInstance impl: %v", err)
	}
	if s := taskState(t, client, task.ID); s != model.TaskStateFailed {
		t.Fatalf("after impl failure: task = %q, want failed", s)
	}

	if _, err := tasks.IncrementOutstanding(ctx, task.ID, 1); err != nil {
		t.Fatalf("increment round 2: %v", err)
	}
	if _, _, err := eng.RunInstance(ctx, oneStepWF("impl-10x", "b"), task); err != nil {
		t.Fatalf("RunInstance impl-10x: %v", err)
	}
	if s := taskState(t, client, task.ID); s != model.TaskStateDone {
		t.Errorf("after escalation success: task = %q, want done", s)
	}
	if h := lastHook(t, side); h.SetState != "all_done" {
		t.Errorf("last tasks: hook = %q, want on_complete (all_done)", h.SetState)
	}
}

// TestEngine_SameRoundSiblingFailureFailsTask locks in that generation scoping
// does NOT weaken any-fail semantics within a round: two workflows fan out
// together, one fails, the other succeeds — the task is failed.
func TestEngine_SameRoundSiblingFailureFailsTask(t *testing.T) {
	ctx := context.Background()
	client, eng, exec, side := generationTestEnv(t)
	task := createTask(t, client, "t")

	if _, err := client.InternalTasks().IncrementOutstanding(ctx, task.ID, 2); err != nil {
		t.Fatalf("increment: %v", err)
	}
	scriptStep(exec, "a", StepResult{Success: false, Output: "boom"})
	if _, _, err := eng.RunInstance(ctx, oneStepWF("wf-a", "a"), task); err != nil {
		t.Fatalf("RunInstance wf-a: %v", err)
	}
	if _, _, err := eng.RunInstance(ctx, oneStepWF("wf-b", "b"), task); err != nil {
		t.Fatalf("RunInstance wf-b: %v", err)
	}
	if s := taskState(t, client, task.ID); s != model.TaskStateFailed {
		t.Errorf("sibling failure in same round: task = %q, want failed", s)
	}
	if h := lastHook(t, side); h.SetState != "all_failed" {
		t.Errorf("last tasks: hook = %q, want on_fail (all_failed)", h.SetState)
	}
}

// TestEngine_Stage2FailureAfterStage1SuccessFailsTask covers multi-stage
// pipelines: stage 1 settles the task done, the next stage's dispatch reopens
// it (bumping the generation), and a stage-2 failure must fail the task — the
// new round's outcome wins in both directions.
func TestEngine_Stage2FailureAfterStage1SuccessFailsTask(t *testing.T) {
	ctx := context.Background()
	client, eng, exec, side := generationTestEnv(t)
	task := createTask(t, client, "t")
	tasks := client.InternalTasks()

	if _, err := tasks.IncrementOutstanding(ctx, task.ID, 1); err != nil {
		t.Fatalf("increment stage 1: %v", err)
	}
	if _, _, err := eng.RunInstance(ctx, oneStepWF("triage", "a"), task); err != nil {
		t.Fatalf("RunInstance triage: %v", err)
	}
	if s := taskState(t, client, task.ID); s != model.TaskStateDone {
		t.Fatalf("after stage 1: task = %q, want done", s)
	}

	if _, err := tasks.IncrementOutstanding(ctx, task.ID, 1); err != nil {
		t.Fatalf("increment stage 2: %v", err)
	}
	scriptStep(exec, "b", StepResult{Success: false, Output: "boom"})
	if _, _, err := eng.RunInstance(ctx, oneStepWF("impl", "b"), task); err != nil {
		t.Fatalf("RunInstance impl: %v", err)
	}
	if s := taskState(t, client, task.ID); s != model.TaskStateFailed {
		t.Errorf("after stage-2 failure: task = %q, want failed", s)
	}
	if h := lastHook(t, side); h.SetState != "all_failed" {
		t.Errorf("last tasks: hook = %q, want on_fail (all_failed)", h.SetState)
	}
}
