package workflow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

// waitForWorkflow: implement (agent) → check-ci (poll) → review (agent). The poll
// step loops back to implement on a red CI, up to 3 times.
func waitForWorkflow() config.WorkflowConfig {
	return config.WorkflowConfig{ID: "impl", Steps: []config.StepConfig{
		{ID: "implement", Agent: "backend-dev"},
		{ID: "check-ci", Type: config.StepTypeWaitFor, DependsOn: []string{"implement"},
			WaitFor: &config.WaitForConfig{Kind: "ci", CheckInterval: "30s", MaxDuration: "2h"},
			OnFail:  &config.StepOutcome{Goto: "implement", MaxRetries: 3}},
		{ID: "review", Agent: "backend-dev", DependsOn: []string{"check-ci"}},
	}}
}

// waitForEngine builds a test engine with a controllable clock and CI checker.
func waitForEngine(cfg *config.Config, store Store, exec StepExecutor, side SideEffects,
	clock *time.Time, ci func() (source.CIStatus, error)) *Engine {
	var seq atomic.Int64
	return NewEngine(cfg, store, exec,
		WithSideEffects(side),
		WithClock(func() time.Time { return *clock }),
		WithIDGen(func(prefix string) string { return prefix + "-" + itoa(int(seq.Add(1))) }),
		WithCIStatusChecker(func(_ context.Context, _, _ string) (source.CIStatus, error) {
			return ci()
		}),
	)
}

func TestWaitFor_SuspendsWhilePending(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	ci := func() (source.CIStatus, error) { return source.CIStatus{Status: "pending"}, nil }
	eng := waitForEngine(baseCfg(), store, exec, &fakeSide{}, &clock, ci)

	instID, success, _ := eng.RunInstance(context.Background(), waitForWorkflow(), model.InternalTask{ID: "c1"})
	if success {
		t.Fatal("a poll-parked instance should not report success")
	}
	if store.instances[instID].State != db.InstanceStateWaiting {
		t.Errorf("instance state = %q, want poll_waiting", store.instances[instID].State)
	}
	// implement ran; review must not have.
	ids := executedIDs(exec.seen)
	if !contains(ids, "implement") || contains(ids, "review") {
		t.Errorf("unexpected execution while CI pending: %v", ids)
	}
	// It shows up as a parked poll, not a parked approval.
	if pp := eng.ParkedWaits(); len(pp) != 1 || pp[0].Step.ID != "check-ci" {
		t.Fatalf("expected one parked poll for check-ci, got %+v", pp)
	}
	if len(eng.ParkedApprovals()) != 0 {
		t.Error("a poll park must not surface as an approval park")
	}
}

func TestWaitFor_AdvancesWhenCIPasses(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	status := "pending"
	ci := func() (source.CIStatus, error) { return source.CIStatus{Status: status}, nil }
	eng := waitForEngine(baseCfg(), store, exec, &fakeSide{}, &clock, ci)

	instID, _, _ := eng.RunInstance(context.Background(), waitForWorkflow(), model.InternalTask{ID: "c1"})

	// Still pending → stays parked.
	eng.CheckParkedWaits(context.Background())
	if store.instances[instID].State != db.InstanceStateWaiting {
		t.Fatalf("expected still poll_waiting, got %q", store.instances[instID].State)
	}

	// CI turns green → next check advances the workflow to done.
	status = "passed"
	eng.CheckParkedWaits(context.Background())
	if store.instances[instID].State != db.InstanceStateDone {
		t.Errorf("expected done after CI passed, got %q", store.instances[instID].State)
	}
	if !contains(executedIDs(exec.seen), "review") {
		t.Error("review should run once CI passed")
	}
	if len(eng.ParkedWaits()) != 0 {
		t.Error("instance should no longer be parked")
	}
}

func TestWaitFor_RedCILoopsBackToImplement(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	// Realistic sequence: the first CI run is red → on_fail loops back to implement
	// (a new commit), whose CI is pending → the poll re-parks waiting for it.
	calls := 0
	override := "" // when set, forces the CI status (mutated by the test below)
	ci := func() (source.CIStatus, error) {
		if override != "" {
			return source.CIStatus{Status: override}, nil
		}
		calls++
		if calls == 1 {
			return source.CIStatus{Status: "failed"}, nil // initial run's CI is red
		}
		return source.CIStatus{Status: "pending"}, nil // the loop-back's new CI is still running
	}
	eng := waitForEngine(baseCfg(), store, exec, &fakeSide{}, &clock, ci)

	instID, _, _ := eng.RunInstance(context.Background(), waitForWorkflow(), model.InternalTask{ID: "c1"})
	if got := store.instances[instID].State; got != db.InstanceStateWaiting {
		t.Fatalf("after red CI loop-back, want poll_waiting, got %q", got)
	}
	if n := countID(exec.seen, "implement"); n != 2 {
		t.Errorf("implement should have run twice (initial + loop-back), got %d", n)
	}
	if contains(executedIDs(exec.seen), "review") {
		t.Error("review must not run while the retried CI is still pending")
	}

	// The retried CI goes green → completes.
	override = "passed"
	eng.CheckParkedWaits(context.Background())
	if store.instances[instID].State != db.InstanceStateDone {
		t.Errorf("expected done after green retry, got %q", store.instances[instID].State)
	}
	if !contains(executedIDs(exec.seen), "review") {
		t.Error("review should run once the retried CI passed")
	}
}

// A merge conflict ceases the CI wait immediately and fails the step terminally
// (never parks), so the failure is handed back for the conflict to be resolved
// instead of burning the whole timeout window waiting for CI that can't matter.
func TestWaitFor_ConflictFailsImmediately(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	ci := func() (source.CIStatus, error) {
		return source.CIStatus{Status: "conflict", URL: "https://gh/pr/1"}, nil
	}
	eng := waitForEngine(baseCfg(), store, exec, &fakeSide{}, &clock, ci)

	// Poll with no on_fail loop, so a conflict fails the instance outright.
	wf := config.WorkflowConfig{ID: "impl", Steps: []config.StepConfig{
		{ID: "implement", Agent: "backend-dev"},
		{ID: "check-ci", Type: config.StepTypeWaitFor, DependsOn: []string{"implement"},
			WaitFor: &config.WaitForConfig{Kind: "ci", MaxDuration: "2h"}},
	}}
	instID, success, _ := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if success {
		t.Fatal("a conflicting PR must not report success")
	}
	if got := store.instances[instID].State; got != db.InstanceStateFailed {
		t.Errorf("instance state = %q, want failed (conflict is terminal, not parked)", got)
	}
	if len(eng.ParkedWaits()) != 0 {
		t.Error("a conflict must not leave the instance parked waiting on CI")
	}
}

// on_conflict routes a merge conflict back to a resolve step even when no on_fail
// is declared, with its own retry budget. Here CI conflicts once, loops back to
// implement via on_conflict, then passes on the retry → the workflow completes.
func TestWaitFor_OnConflictLoopsBack(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	calls := 0
	ci := func() (source.CIStatus, error) {
		calls++
		if calls == 1 {
			return source.CIStatus{Status: "conflict", URL: "https://gh/pr/1"}, nil
		}
		return source.CIStatus{Status: "passed"}, nil
	}
	eng := waitForEngine(baseCfg(), store, exec, &fakeSide{}, &clock, ci)

	// No on_fail at all — only on_conflict. Without on_conflict a conflict would be
	// terminal, so a completed run proves on_conflict drove the loop-back.
	wf := config.WorkflowConfig{ID: "impl", Steps: []config.StepConfig{
		{ID: "implement", Agent: "backend-dev"},
		{ID: "check-ci", Type: config.StepTypeWaitFor, DependsOn: []string{"implement"},
			WaitFor:    &config.WaitForConfig{Kind: "ci", MaxDuration: "2h"},
			OnConflict: &config.StepOutcome{Goto: "implement", MaxRetries: 3}},
		{ID: "review", Agent: "backend-dev", DependsOn: []string{"check-ci"}},
	}}
	instID, _, _ := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})

	if got := store.instances[instID].State; got != db.InstanceStateDone {
		t.Fatalf("instance state = %q, want done (conflict resolved on retry)", got)
	}
	if n := countID(exec.seen, "implement"); n != 2 {
		t.Errorf("implement should run twice (initial + on_conflict loop-back), got %d", n)
	}
	if !contains(executedIDs(exec.seen), "review") {
		t.Error("review should run once the retried CI passed")
	}
}

// on_conflict has its own retry budget and, once exhausted, fails terminally —
// it does NOT fall through to on_fail. With a persistent conflict, implement runs
// only initial+1 (on_conflict.max_retries=1), never consuming on_fail's budget.
func TestWaitFor_OnConflictExhaustsWithoutFallthroughToOnFail(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	ci := func() (source.CIStatus, error) { return source.CIStatus{Status: "conflict"}, nil }
	eng := waitForEngine(baseCfg(), store, exec, &fakeSide{}, &clock, ci)

	wf := config.WorkflowConfig{ID: "impl", Steps: []config.StepConfig{
		{ID: "implement", Agent: "backend-dev"},
		{ID: "check-ci", Type: config.StepTypeWaitFor, DependsOn: []string{"implement"},
			WaitFor:    &config.WaitForConfig{Kind: "ci", MaxDuration: "2h"},
			OnConflict: &config.StepOutcome{Goto: "implement", MaxRetries: 1},
			OnFail:     &config.StepOutcome{Goto: "implement", MaxRetries: 5}},
	}}
	instID, success, _ := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})

	if success {
		t.Fatal("a persistent conflict must not report success")
	}
	if got := store.instances[instID].State; got != db.InstanceStateFailed {
		t.Errorf("instance state = %q, want failed (on_conflict budget exhausted)", got)
	}
	// initial + exactly 1 on_conflict retry. If a conflict had fallen through to
	// on_fail (max_retries 5) implement would have run 7 times.
	if n := countID(exec.seen, "implement"); n != 2 {
		t.Errorf("implement should run twice (initial + 1 on_conflict retry), got %d", n)
	}
}

func TestWaitFor_TimesOut(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	ci := func() (source.CIStatus, error) { return source.CIStatus{Status: "pending"}, nil }
	eng := waitForEngine(baseCfg(), store, exec, &fakeSide{}, &clock, ci)

	// Workflow whose poll has no on_fail loop, so a timeout fails the instance.
	wf := config.WorkflowConfig{ID: "impl", Steps: []config.StepConfig{
		{ID: "implement", Agent: "backend-dev"},
		{ID: "check-ci", Type: config.StepTypeWaitFor, DependsOn: []string{"implement"},
			WaitFor: &config.WaitForConfig{Kind: "ci", MaxDuration: "2h"}},
	}}
	instID, _, _ := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if store.instances[instID].State != db.InstanceStateWaiting {
		t.Fatalf("expected poll_waiting, got %q", store.instances[instID].State)
	}

	// Advance past the 2h deadline; the next check times out and fails the step.
	clock = clock.Add(3 * time.Hour)
	eng.CheckParkedWaits(context.Background())
	if store.instances[instID].State != db.InstanceStateFailed {
		t.Errorf("expected failed after timeout, got %q", store.instances[instID].State)
	}
}

// TestWaitFor_RehydrateSurvivesRestart is the core regression: an instance parked at
// a poll step when the daemon stopped must be reconstructed (not restarted from the
// top) so it resumes from the poll and advances past it — without re-running the
// already-passed implement step.
func TestWaitFor_RehydrateSurvivesRestart(t *testing.T) {
	store := newFakeStore()
	clock := time.Unix(1000, 0)

	// First engine: run until parked at the poll step (CI pending).
	exec1 := &fakeExecutor{}
	ci1 := func() (source.CIStatus, error) { return source.CIStatus{Status: "pending"}, nil }
	eng1 := waitForEngine(baseCfg(), store, exec1, &fakeSide{}, &clock, ci1)
	instID, _, _ := eng1.RunInstance(context.Background(), waitForWorkflow(), model.InternalTask{ID: "c1"})
	if store.instances[instID].State != db.InstanceStateWaiting {
		t.Fatalf("expected poll_waiting before restart, got %q", store.instances[instID].State)
	}

	// Simulate a daemon restart: a brand-new engine with an empty parked set, CI now
	// green. Rehydrate from the persisted step runs, then a poll check advances it.
	exec2 := &fakeExecutor{}
	ci2 := func() (source.CIStatus, error) { return source.CIStatus{Status: "passed"}, nil }
	eng2 := waitForEngine(baseCfg(), store, exec2, &fakeSide{}, &clock, ci2)

	if err := eng2.RehydrateWait(context.Background(), instID, waitForWorkflow(),
		model.InternalTask{ID: "c1"}, priorStepsFor(store, instID)); err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if pp := eng2.ParkedWaits(); len(pp) != 1 || pp[0].Step.ID != "check-ci" {
		t.Fatalf("expected rehydrated poll park at check-ci, got %+v", pp)
	}

	eng2.CheckParkedWaits(context.Background())

	if store.instances[instID].State != db.InstanceStateDone {
		t.Errorf("expected done after rehydrated poll passed, got %q", store.instances[instID].State)
	}
	// implement must NOT re-run on the new engine — it was cached/replayed.
	if contains(executedIDs(exec2.seen), "implement") {
		t.Error("implement must not re-run after rehydration (it already passed)")
	}
	// review should run on the new engine once CI passed.
	if !contains(executedIDs(exec2.seen), "review") {
		t.Error("review should run after the rehydrated poll passed")
	}
}

// TestWaitFor_RecordsEachPoll verifies every CI poll is persisted with its
// returned status, so a parked wait reports how many times it polled and what
// each poll returned.
func TestWaitFor_RecordsEachPoll(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	status := "pending"
	url := "https://example.test/pr/1"
	ci := func() (source.CIStatus, error) {
		return source.CIStatus{Status: status, URL: url}, nil
	}
	eng := waitForEngine(baseCfg(), store, exec, &fakeSide{}, &clock, ci)

	// Initial run polls once (pending → park).
	instID, _, _ := eng.RunInstance(context.Background(), waitForWorkflow(), model.InternalTask{ID: "c1"})
	// Two more pending re-checks, then green.
	eng.CheckParkedWaits(context.Background())
	eng.CheckParkedWaits(context.Background())
	status = "passed"
	eng.CheckParkedWaits(context.Background())

	got := store.pollStatuses()
	want := []string{"pending", "pending", "pending", "passed"}
	if len(got) != len(want) {
		t.Fatalf("recorded %d polls (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("poll %d status = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
	// The PR URL and instance/step are carried on each record.
	store.mu.Lock()
	last := store.ciPolls[len(store.ciPolls)-1]
	store.mu.Unlock()
	if last.WorkflowInstanceID != instID || last.StepID != "check-ci" || last.PRURL != url {
		t.Errorf("poll record = %+v, want inst=%s step=check-ci url=%s", last, instID, url)
	}
}

// TestWaitFor_RecordsErrorAndTimeout verifies a failed CI check and a timeout are
// each recorded with a distinct status.
func TestWaitFor_RecordsErrorAndTimeout(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	ci := func() (source.CIStatus, error) {
		return source.CIStatus{}, context.DeadlineExceeded // transient checker error
	}
	eng := waitForEngine(baseCfg(), store, exec, &fakeSide{}, &clock, ci)

	wf := config.WorkflowConfig{ID: "impl", Steps: []config.StepConfig{
		{ID: "implement", Agent: "backend-dev"},
		{ID: "check-ci", Type: config.StepTypeWaitFor, DependsOn: []string{"implement"},
			WaitFor: &config.WaitForConfig{Kind: "ci", MaxDuration: "2h"}},
	}}
	eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})

	// Past the deadline: next check records a timeout.
	clock = clock.Add(3 * time.Hour)
	eng.CheckParkedWaits(context.Background())

	got := store.pollStatuses()
	if len(got) != 2 || got[0] != "error" || got[1] != "timeout" {
		t.Errorf("recorded polls = %v, want [error timeout]", got)
	}
}

// priorStepsFor returns the instance's persisted step runs in creation order,
// mirroring db.Client.ListStepRuns for the rehydration path.
func priorStepsFor(f *fakeStore, instID string) []db.StepRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []db.StepRun
	for _, id := range f.stepOrder {
		if sr, ok := f.stepRuns[id]; ok && sr.WorkflowInstanceID == instID {
			out = append(out, *sr)
		}
	}
	return out
}

func countID(seen []StepRequest, id string) int {
	n := 0
	for _, s := range seen {
		if s.Step.ID == id {
			n++
		}
	}
	return n
}
