package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// stepRunsFor returns the persisted step runs for an instance in insertion order,
// mirroring what *db.Client.ListStepRuns hands the daemon at rehydration time.
func (f *fakeStore) stepRunsFor(instID string) []db.StepRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []db.StepRun
	for _, id := range f.stepOrder {
		if sr := f.stepRuns[id]; sr != nil && sr.WorkflowInstanceID == instID {
			out = append(out, *sr)
		}
	}
	return out
}

// TestRehydrateApproval_ResolvesAndSettlesTask is the regression test for the
// approval_waiting restart gap: an instance parked at an approval step is lost
// from the engine's in-memory parked set across a daemon restart. After
// rehydration the next approval check must re-evaluate it, resume it, and settle
// its task — instead of leaving the task stuck in 'registered' forever.
func TestRehydrateApproval_ResolvesAndSettlesTask(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	store.bindings["c1"] = []model.SourceBinding{{TaskID: "c1", SourceID: "s1", SourceItemID: "c1"}}

	// --- before the restart: run the workflow until it parks at the approval. ---
	tr1 := newFakeTracker()
	tr1.outstanding["c1"] = 1 // one outstanding workflow, as fanOut would record
	eng1 := trackerEngine(cfg, store, &fakeExecutor{}, &fakeSide{}, tr1)
	instID, success, _ := eng1.RunInstance(context.Background(), approvalWorkflow(), model.InternalTask{ID: "c1"})
	if success {
		t.Fatal("parked instance should not report success")
	}
	if store.instances[instID].State != db.InstanceStateApprovalWaiting {
		t.Fatalf("instance state = %q, want approval_waiting", store.instances[instID].State)
	}

	// --- simulate the restart: a brand-new engine over the same persisted store,
	// with an empty in-memory parked set. This is exactly the bug. ---
	tr2 := newFakeTracker()
	tr2.outstanding["c1"] = 1
	side2 := &fakeSide{}
	exec2 := &fakeExecutor{}
	eng2 := trackerEngine(cfg, store, exec2, side2, tr2)
	if len(eng2.ParkedApprovals()) != 0 {
		t.Fatal("a fresh engine must start with an empty parked set")
	}

	// Without rehydration the approval check finds nothing to do.
	eng2.CheckParkedApprovals(context.Background(), approvingPoll)
	if store.instances[instID].State != db.InstanceStateApprovalWaiting {
		t.Fatal("pre-rehydration: instance must remain stranded in approval_waiting")
	}

	// --- rehydrate: reconstruct the parked run from persisted state. ---
	prior := store.stepRunsFor(instID)
	if err := eng2.RehydrateApproval(context.Background(), instID, approvalWorkflow(),
		model.InternalTask{ID: "c1"}, prior, time.Unix(1000, 0)); err != nil {
		t.Fatalf("RehydrateApproval: %v", err)
	}
	parked := eng2.ParkedApprovals()
	if len(parked) != 1 || parked[0].InstanceID != instID || parked[0].Step.ID != "gate" {
		t.Fatalf("expected one rehydrated parked approval for gate, got %+v", parked)
	}
	// Rehydration must not re-post the approval message (it was posted at park time).
	if len(side2.comments) != 0 {
		t.Errorf("rehydration must not re-post the approval message, got %v", side2.comments)
	}

	// --- the live source now carries an approving comment: it resolves. ---
	eng2.CheckParkedApprovals(context.Background(), approvingPoll)

	if store.instances[instID].State != db.InstanceStateDone {
		t.Errorf("instance state = %q, want done after rehydrated approval resolved", store.instances[instID].State)
	}
	if !contains(executedIDs(exec2.seen), "implement") {
		t.Error("implement should run after the rehydrated approval resumed")
	}
	if len(eng2.ParkedApprovals()) != 0 {
		t.Error("instance should no longer be parked once resolved")
	}
	// The whole point: the task's outstanding counter drained and it settled.
	if tr2.outstanding["c1"] != 0 {
		t.Errorf("task outstanding = %d, want 0", tr2.outstanding["c1"])
	}
	if s, ok := tr2.state("c1"); !ok || s != model.TaskStateDone {
		t.Errorf("task state = %q (set=%v), want done", s, ok)
	}
}

// TestRehydrateApproval_PreservesTimeout asserts the original park time is carried
// across the restart, so an approval times out relative to when it first parked
// rather than resetting its clock on every boot.
func TestRehydrateApproval_PreservesTimeout(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	store.bindings["c1"] = []model.SourceBinding{{TaskID: "c1", SourceID: "s1", SourceItemID: "c1"}}

	parkedAt := time.Unix(0, 0)
	clock := parkedAt
	eng := NewEngine(cfg, store, &fakeExecutor{},
		WithSideEffects(&fakeSide{}),
		WithClock(func() time.Time { return clock }),
		WithIDGen(func(p string) string { return p + "-1" }),
	)

	// Persist a plan step that already passed; the gate parked with a 48h timeout.
	instID := "wf_rehydrate_timeout"
	store.CreateWorkflowInstance(context.Background(), &db.WorkflowInstance{ //nolint:errcheck
		ID: instID, WorkflowID: "feature", TaskID: "c1", CellID: "c1", State: db.InstanceStateApprovalWaiting})
	prior := []db.StepRun{
		{ID: "sr-plan", WorkflowInstanceID: instID, StepID: "plan", State: db.StepStatePassed},
	}

	if err := eng.RehydrateApproval(context.Background(), instID, approvalWorkflow(),
		model.InternalTask{ID: "c1"}, prior, parkedAt); err != nil {
		t.Fatalf("RehydrateApproval: %v", err)
	}

	// Advance past the 48h timeout; the live item offers no resume signal.
	clock = parkedAt.Add(49 * time.Hour)
	noSignal := func(sourceID, cellID string) (model.SourceItem, error) {
		return model.SourceItem{ID: cellID}, nil
	}
	eng.CheckParkedApprovals(context.Background(), noSignal)

	if store.instances[instID].State != db.InstanceStateFailed {
		t.Errorf("expected failed after rehydrated approval timed out, got %q", store.instances[instID].State)
	}
}

// TestRehydrateApproval_NoApprovalStep guards the malformed/stale-row case: an
// instance recorded as approval_waiting whose steps leave no approval pending is
// rejected with ErrNoApprovalStep rather than silently re-parking a bad run.
func TestRehydrateApproval_NoApprovalStep(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	eng := testEngine(cfg, store, &fakeExecutor{}, &fakeSide{})

	// linearWF has no approval step at all.
	prior := []db.StepRun{
		{ID: "sr-plan", WorkflowInstanceID: "x", StepID: "plan", State: db.StepStatePassed},
	}
	err := eng.RehydrateApproval(context.Background(), "x", linearWF(),
		model.InternalTask{ID: "c1"}, prior, time.Unix(1000, 0))
	if err != ErrNoApprovalStep {
		t.Errorf("expected ErrNoApprovalStep, got %v", err)
	}
	if len(eng.ParkedApprovals()) != 0 {
		t.Error("nothing should be parked when there is no approval step")
	}
}

// approvingPoll returns a live item whose comment satisfies the gate's resume_on.
func approvingPoll(sourceID, cellID string) (model.SourceItem, error) {
	return model.SourceItem{ID: cellID, SourceID: sourceID,
		Comments: []model.Comment{{Body: "approve please"}}}, nil
}
