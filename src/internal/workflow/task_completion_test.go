package workflow

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// fakeTracker is an in-memory TaskTracker: it tracks a per-task outstanding
// counter (decremented on each terminal instance), a settable "a sibling
// instance failed" flag, and the lifecycle state the engine flips the task to.
type fakeTracker struct {
	mu          sync.Mutex
	outstanding map[string]int
	failed      map[string]bool
	states      map[string]model.TaskState
}

func newFakeTracker() *fakeTracker {
	return &fakeTracker{
		outstanding: map[string]int{},
		failed:      map[string]bool{},
		states:      map[string]model.TaskState{},
	}
}

func (f *fakeTracker) DecrementOutstanding(_ context.Context, id string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.outstanding[id] - 1
	if n < 0 {
		n = 0
	}
	f.outstanding[id] = n
	return n, nil
}

func (f *fakeTracker) HasFailedInstance(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failed[id], nil
}

func (f *fakeTracker) SetTaskState(_ context.Context, id string, s model.TaskState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[id] = s
	return nil
}

func (f *fakeTracker) state(id string) (model.TaskState, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.states[id]
	return s, ok
}

// taskHookCfg returns a config with no per-workflow hooks (so the only thing
// reaching fakeSide.hooks is the top-level tasks: completion hook) and a tasks:
// block with distinguishable on_complete/on_fail states.
func taskHookCfg() *config.Config {
	cfg := baseCfg()
	cfg.Settings.StateLock = false
	cfg.Settings.ResultComment = false
	cfg.Tasks = &config.TasksConfig{
		OnComplete: &config.OnComplete{SetState: "all_done"},
		OnFail:     &config.OnComplete{SetState: "all_failed"},
	}
	return cfg
}

func trackerEngine(cfg *config.Config, store Store, exec StepExecutor, side SideEffects, tr TaskTracker) *Engine {
	var seq atomic.Int64
	return NewEngine(cfg, store, exec,
		WithSideEffects(side),
		WithTaskTracker(tr),
		WithClock(func() time.Time { return time.Unix(1000, 0) }),
		WithIDGen(func(prefix string) string { return prefix + "-" + itoa(int(seq.Add(1))) }),
	)
}

func oneStepWF(id, stepID string) config.WorkflowConfig {
	return config.WorkflowConfig{ID: id, Steps: []config.StepConfig{{ID: stepID, Agent: "backend-dev"}}}
}

// TestEngine_TaskHookFiresAfterLastWorkflowSucceeds covers 8.2.3 (success path):
// with two workflows fanned out on one task, the tasks: on_complete hook fires
// only once the second (last) instance settles — not after the first — and the
// task transitions to done.
func TestEngine_TaskHookFiresAfterLastWorkflowSucceeds(t *testing.T) {
	cfg := taskHookCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{}}
	side := &fakeSide{}
	tr := newFakeTracker()
	tr.outstanding["T1"] = 2
	eng := trackerEngine(cfg, store, exec, side, tr)
	task := model.InternalTask{ID: "T1", Title: "x"}

	if _, _, err := eng.RunInstance(context.Background(), oneStepWF("wf-a", "a"), task); err != nil {
		t.Fatalf("RunInstance wf-a: %v", err)
	}
	if len(side.hooks) != 0 {
		t.Fatalf("task hook fired after first of two workflows: %+v", side.hooks)
	}
	if _, ok := tr.state("T1"); ok {
		t.Fatalf("task state set before the last workflow settled")
	}

	if _, _, err := eng.RunInstance(context.Background(), oneStepWF("wf-b", "b"), task); err != nil {
		t.Fatalf("RunInstance wf-b: %v", err)
	}
	if len(side.hooks) != 1 || side.hooks[0].SetState != "all_done" {
		t.Fatalf("expected one on_complete task hook, got %+v", side.hooks)
	}
	if s, _ := tr.state("T1"); s != model.TaskStateDone {
		t.Errorf("task state = %q, want done", s)
	}
}

// TestEngine_TaskHookFiresOnFailWhenAnyWorkflowFailed covers 8.2.3 (failure
// path): when one of the task's instances failed (recorded earlier, surfaced via
// HasFailedInstance), the aggregate outcome is failure — so once the last
// instance settles the tasks: on_fail hook fires even though that final instance
// itself succeeded, and the task transitions to failed.
func TestEngine_TaskHookFiresOnFailWhenAnyWorkflowFailed(t *testing.T) {
	cfg := taskHookCfg()
	store := newFakeStore()
	// wf-a's step fails; wf-b's step succeeds (default).
	exec := &fakeExecutor{results: map[string]StepResult{"a": {Success: false, Output: "boom"}}}
	side := &fakeSide{}
	tr := newFakeTracker()
	tr.outstanding["T1"] = 2
	tr.failed["T1"] = true // a sibling instance failed and was persisted
	eng := trackerEngine(cfg, store, exec, side, tr)
	task := model.InternalTask{ID: "T1", Title: "x"}

	// First (failing) instance settles: counter 2→1, no hook yet.
	if _, _, err := eng.RunInstance(context.Background(), oneStepWF("wf-a", "a"), task); err != nil {
		t.Fatalf("RunInstance wf-a: %v", err)
	}
	if len(side.hooks) != 0 {
		t.Fatalf("task hook fired before the last workflow settled: %+v", side.hooks)
	}

	// Last (succeeding) instance settles: counter 1→0; aggregate is failed.
	if _, _, err := eng.RunInstance(context.Background(), oneStepWF("wf-b", "b"), task); err != nil {
		t.Fatalf("RunInstance wf-b: %v", err)
	}
	if len(side.hooks) != 1 || side.hooks[0].SetState != "all_failed" {
		t.Fatalf("expected one on_fail task hook, got %+v", side.hooks)
	}
	if s, _ := tr.state("T1"); s != model.TaskStateFailed {
		t.Errorf("task state = %q, want failed", s)
	}
}

// TestEngine_TaskHookNoTrackerIsNoOp asserts the pre-Phase-8 default: with no
// TaskTracker wired the engine neither decrements nor applies the tasks: hook,
// even when a tasks: block is configured.
func TestEngine_TaskHookNoTrackerIsNoOp(t *testing.T) {
	cfg := taskHookCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{}}
	side := &fakeSide{}
	// No WithTaskTracker.
	var seq atomic.Int64
	eng := NewEngine(cfg, store, exec,
		WithSideEffects(side),
		WithClock(func() time.Time { return time.Unix(1000, 0) }),
		WithIDGen(func(prefix string) string { return prefix + "-" + itoa(int(seq.Add(1))) }),
	)
	if _, _, err := eng.RunInstance(context.Background(), oneStepWF("wf-a", "a"), model.InternalTask{ID: "T1"}); err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if len(side.hooks) != 0 {
		t.Errorf("tasks: hook applied without a tracker: %+v", side.hooks)
	}
}
