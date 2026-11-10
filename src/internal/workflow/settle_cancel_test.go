package workflow

import (
	"context"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// cancelAwareStore records the context error each persisted instance-state write
// arrived with, so a test can prove the teardown ran on a live context.
type cancelAwareStore struct {
	*fakeStore
	instanceStateErrs []error
}

func (s *cancelAwareStore) UpdateWorkflowInstanceState(ctx context.Context, id, state, reason string) error {
	s.instanceStateErrs = append(s.instanceStateErrs, ctx.Err())
	return s.fakeStore.UpdateWorkflowInstanceState(ctx, id, state, reason)
}

// cancelAwareTracker is a fakeTracker that records whether the outstanding
// decrement was handed an already-cancelled context.
type cancelAwareTracker struct {
	*fakeTracker
	decrementErrs []error
}

func (c *cancelAwareTracker) DecrementOutstanding(ctx context.Context, id string) (int, error) {
	c.decrementErrs = append(c.decrementErrs, ctx.Err())
	return c.fakeTracker.DecrementOutstanding(ctx, id)
}

// cancelAwareSide records the context error each exit hook was applied with.
type cancelAwareSide struct {
	*fakeSide
	hookErrs []error
}

func (s *cancelAwareSide) ApplyHook(ctx context.Context, task model.InternalTask, bindings []model.SourceBinding, h config.OnComplete) error {
	s.hookErrs = append(s.hookErrs, ctx.Err())
	return s.fakeSide.ApplyHook(ctx, task, bindings, h)
}

// TestSettle_CancelledRunStillSettlesBookkeeping is the #7748 regression.
//
// Stopping a run (`apiary stop`, force-restart, graceful shutdown) cancels the
// context every step shares. The teardown used to reuse that context, so every
// piece of it failed with `context canceled`: the instance state went unwritten,
// the task's outstanding counter was never decremented, and the exit hook —
// which is what applies `needs-human`/`needs-attention` — never reached the
// issue. The issue was left with its `workflow:*` label, no job in the queue,
// and nothing to say why it had stopped.
//
// The teardown must therefore run on a context that outlives the cancellation
// while keeping the run's values.
func TestSettle_CancelledRunStillSettlesBookkeeping(t *testing.T) {
	cfg := taskHookCfg()
	store := &cancelAwareStore{fakeStore: newFakeStore()}
	side := &cancelAwareSide{fakeSide: &fakeSide{}}
	tr := &cancelAwareTracker{fakeTracker: newFakeTracker()}
	tr.outstanding["T1"] = 1
	eng := trackerEngine(cfg, store, &fakeExecutor{results: map[string]StepResult{}}, side, tr)

	r := &dagRun{instID: "i1", task: model.InternalTask{ID: "T1"}, wf: config.WorkflowConfig{ID: "wf"}}
	if err := store.CreateWorkflowInstance(context.Background(), &db.WorkflowInstance{ID: "i1", WorkflowID: "wf", TaskID: "T1", State: db.InstanceStateRunning}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the stop: the run's context is already gone when settle runs

	if completed := eng.settle(ctx, r, outcomeFailed); completed {
		t.Fatal("settle reported success for a failed outcome")
	}

	if got := tr.outstanding["T1"]; got != 0 {
		t.Errorf("outstanding = %d, want 0 — the decrement must survive the cancelled run context", got)
	}
	if len(tr.decrementErrs) != 1 || tr.decrementErrs[0] != nil {
		t.Fatalf("decrement outstanding carried a cancelled context: %v", tr.decrementErrs)
	}
	if len(store.instanceStateErrs) != 1 || store.instanceStateErrs[0] != nil {
		t.Errorf("instance state write carried a cancelled context: %v", store.instanceStateErrs)
	}
	if len(side.hooks) != 1 || side.hooks[0].SetState != "all_failed" {
		t.Fatalf("exit hook not applied on a cancelled run: %+v", side.hooks)
	}
	if len(side.hookErrs) != 1 || side.hookErrs[0] != nil {
		t.Errorf("exit hook carried a cancelled context: %v", side.hookErrs)
	}
	if s, _ := tr.state("T1"); s != model.TaskStateFailed {
		t.Errorf("task state = %q, want failed", s)
	}
	if inst := store.instances["i1"]; inst == nil || inst.State != "failed" {
		t.Errorf("instance = %+v, want state failed", inst)
	}
}
