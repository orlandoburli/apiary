package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

// parallelWaitWorkflow: implement (agent) → gate (parallel: review agent +
// await-ci wait_for) → merge (agent). This is the shape from #425: a CI wait
// running beside a code review, joined with the default join: all.
func parallelWaitWorkflow(join string) config.WorkflowConfig {
	return config.WorkflowConfig{ID: "impl", Steps: []config.StepConfig{
		{ID: "implement", Agent: "backend-dev"},
		{ID: "gate", Type: config.StepTypeParallel, DependsOn: []string{"implement"}, Join: join, SubSteps: []config.StepConfig{
			{ID: "review", Agent: "backend-dev"},
			{ID: "await-ci", Type: config.StepTypeWaitFor,
				WaitFor: &config.WaitForConfig{Kind: "ci", CheckInterval: "30s", MaxDuration: "2h"}},
		}},
		{ID: "merge", Agent: "backend-dev", DependsOn: []string{"gate"}},
	}}
}

// countExecuted reports how many times a step id was executed.
func countExecuted(seen []StepRequest, id string) int {
	n := 0
	for _, r := range seen {
		if r.Step.ID == id {
			n++
		}
	}
	return n
}

// A wait_for child with no answer yet parks the whole group; its passing
// sibling is remembered, so waking the group re-polls CI without re-running the
// review agent. Before #425 the wait_for child ran as an agent step and the
// group failed instantly.
func TestParallelWaitChild_ParksGroupAndReusesSiblingResult(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	status := "pending"
	ci := func() (source.CIStatus, error) { return source.CIStatus{Status: status}, nil }
	eng := waitForEngine(baseCfg(), store, exec, &fakeSide{}, &clock, ci)

	instID, success, _ := eng.RunInstance(context.Background(), parallelWaitWorkflow(""), model.InternalTask{ID: "c1"})
	if success {
		t.Fatal("a group parked on a pending CI wait should not report success")
	}
	if got := store.instances[instID].State; got != db.InstanceStateWaiting {
		t.Fatalf("instance state = %q, want %q", got, db.InstanceStateWaiting)
	}
	// The park names the waiting CHILD, so the dispatcher re-checks the right wait.
	parked := eng.ParkedWaits()
	if len(parked) != 1 || parked[0].Step.ID != "await-ci" {
		t.Fatalf("expected one parked wait for await-ci, got %+v", parked)
	}
	if n := countExecuted(exec.seen, "review"); n != 1 {
		t.Fatalf("review should have run exactly once while parked, ran %d times", n)
	}
	if contains(executedIDs(exec.seen), "merge") {
		t.Error("merge must not run while the group is parked")
	}

	// Still pending → stays parked, and the review is NOT re-run.
	eng.CheckParkedWaits(context.Background())
	if got := store.instances[instID].State; got != db.InstanceStateWaiting {
		t.Fatalf("expected still waiting, got %q", got)
	}
	if n := countExecuted(exec.seen, "review"); n != 1 {
		t.Fatalf("review re-ran on a CI re-check (%d times) — the group's finished children must be memoized", n)
	}

	// CI turns green → the join passes and the workflow completes.
	status = "passed"
	eng.CheckParkedWaits(context.Background())
	if got := store.instances[instID].State; got != db.InstanceStateDone {
		t.Fatalf("expected done after CI passed, got %q", got)
	}
	if n := countExecuted(exec.seen, "review"); n != 1 {
		t.Errorf("review ran %d times, want 1", n)
	}
	if n := countExecuted(exec.seen, "merge"); n != 1 {
		t.Errorf("merge ran %d times, want 1", n)
	}
}

// Under join: all a failed sibling already decides the group, so it must not
// park for hours waiting on a CI result that cannot change the outcome.
func TestParallelWaitChild_FailedSiblingDecidesJoinWithoutParking(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"review": {Success: false, Output: "changes requested"},
	}}
	clock := time.Unix(1000, 0)
	ci := func() (source.CIStatus, error) { return source.CIStatus{Status: "pending"}, nil }
	eng := waitForEngine(baseCfg(), store, exec, &fakeSide{}, &clock, ci)

	instID, success, _ := eng.RunInstance(context.Background(), parallelWaitWorkflow(""), model.InternalTask{ID: "c1"})
	if success {
		t.Fatal("a group whose review failed must not report success")
	}
	if got := store.instances[instID].State; got == db.InstanceStateWaiting {
		t.Fatal("group parked on CI although join: all was already decided by the failed review")
	}
	if len(eng.ParkedWaits()) != 0 {
		t.Errorf("expected no parked wait, got %+v", eng.ParkedWaits())
	}
}

// Under join: any a passing sibling decides the group the same way.
func TestParallelWaitChild_PassingSiblingDecidesJoinAny(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	ci := func() (source.CIStatus, error) { return source.CIStatus{Status: "pending"}, nil }
	eng := waitForEngine(baseCfg(), store, exec, &fakeSide{}, &clock, ci)

	instID, _, _ := eng.RunInstance(context.Background(), parallelWaitWorkflow(config.JoinAny), model.InternalTask{ID: "c1"})
	if got := store.instances[instID].State; got != db.InstanceStateDone {
		t.Fatalf("join: any with a passed child should complete, got %q", got)
	}
}

// A CI failure inside the group fails the group (join: all) rather than
// hanging: the wait's terminal result flows through the normal join.
func TestParallelWaitChild_CIFailureFailsGroup(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	status := "pending"
	ci := func() (source.CIStatus, error) { return source.CIStatus{Status: status}, nil }
	eng := waitForEngine(baseCfg(), store, exec, &fakeSide{}, &clock, ci)

	instID, _, _ := eng.RunInstance(context.Background(), parallelWaitWorkflow(""), model.InternalTask{ID: "c1"})
	status = "failed"
	eng.CheckParkedWaits(context.Background())

	if got := store.instances[instID].State; got != db.InstanceStateFailed {
		t.Fatalf("expected failed after CI failed, got %q", got)
	}
	if contains(executedIDs(exec.seen), "merge") {
		t.Error("merge must not run after the gate failed")
	}
}

// A daemon restart while a group is parked must resume the group without
// re-running the review child that already passed: its persisted step run is
// replayed into the group's memoized results.
func TestParallelWaitChild_RehydrateSurvivesRestart(t *testing.T) {
	store := newFakeStore()
	clock := time.Unix(1000, 0)

	exec1 := &fakeExecutor{}
	ci1 := func() (source.CIStatus, error) { return source.CIStatus{Status: "pending"}, nil }
	eng1 := waitForEngine(baseCfg(), store, exec1, &fakeSide{}, &clock, ci1)
	instID, _, _ := eng1.RunInstance(context.Background(), parallelWaitWorkflow(""), model.InternalTask{ID: "c1"})
	if got := store.instances[instID].State; got != db.InstanceStateWaiting {
		t.Fatalf("expected waiting before restart, got %q", got)
	}

	// New engine, empty parked set, CI now green.
	exec2 := &fakeExecutor{}
	ci2 := func() (source.CIStatus, error) { return source.CIStatus{Status: "passed"}, nil }
	eng2 := waitForEngine(baseCfg(), store, exec2, &fakeSide{}, &clock, ci2)

	if err := eng2.RehydrateWait(context.Background(), instID, parallelWaitWorkflow(""),
		model.InternalTask{ID: "c1"}, priorStepsFor(store, instID)); err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if pp := eng2.ParkedWaits(); len(pp) != 1 || pp[0].Step.ID != "await-ci" {
		t.Fatalf("expected rehydrated park at await-ci, got %+v", pp)
	}

	eng2.CheckParkedWaits(context.Background())

	if got := store.instances[instID].State; got != db.InstanceStateDone {
		t.Fatalf("expected done after rehydrated wait passed, got %q", got)
	}
	if contains(executedIDs(exec2.seen), "implement") {
		t.Error("implement must not re-run after rehydration (it already passed)")
	}
	if contains(executedIDs(exec2.seen), "review") {
		t.Error("the parallel group's passed review child must not re-run after rehydration")
	}
	if !contains(executedIDs(exec2.seen), "merge") {
		t.Error("merge should run once the rehydrated wait passed")
	}
}

// An unsupported child kind that slipped past config validation fails loudly
// with the type named, instead of silently running as an agent step.
func TestParallelChild_UnsupportedKindFailsWithDiagnostic(t *testing.T) {
	eng := waitForEngine(baseCfg(), newFakeStore(), &fakeExecutor{}, &fakeSide{},
		func() *time.Time { t := time.Unix(1000, 0); return &t }(),
		func() (source.CIStatus, error) { return source.CIStatus{}, nil })

	res := eng.runParallelChild(context.Background(), "inst-1",
		config.StepConfig{ID: "nested", Type: config.StepTypeForeach},
		model.SourceItem{}, model.InternalTask{}, nil, nil, wfScope{}, time.Time{})

	if res.Success {
		t.Fatal("an unsupported parallel child must not report success")
	}
	if res.Err == nil {
		t.Fatal("an unsupported parallel child must carry an error")
	}
	if !contains([]string{res.Output}, "step type \"foreach\" is not supported inside a parallel group") {
		t.Errorf("error should name the offending type, got %q", res.Output)
	}
}
