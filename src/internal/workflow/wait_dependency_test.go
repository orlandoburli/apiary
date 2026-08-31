package workflow

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

// dependencyWorkflow: await-blockers (wait_for/dependency) → implement (agent).
func dependencyWorkflow(wait *config.WaitForConfig) config.WorkflowConfig {
	return config.WorkflowConfig{ID: "impl", Steps: []config.StepConfig{
		{ID: "await-blockers", Type: config.StepTypeWaitFor, WaitFor: wait},
		{ID: "implement", Agent: "backend-dev", DependsOn: []string{"await-blockers"}},
	}}
}

// depWaitEngine builds a test engine with a controllable clock and blocker lister.
func depWaitEngine(store Store, exec StepExecutor, clock *time.Time,
	dep func() ([]source.BlockerRef, error)) *Engine {
	var seq atomic.Int64
	return NewEngine(baseCfg(), store, exec,
		WithSideEffects(&fakeSide{}),
		WithClock(func() time.Time { return *clock }),
		WithIDGen(func(prefix string) string { return prefix + "-" + itoa(int(seq.Add(1))) }),
		WithDependencyChecker(func(_ context.Context, _, _, _ string) ([]source.BlockerRef, error) {
			return dep()
		}),
	)
}

func TestDependencyWait_SuspendsWhileBlockerOpen(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	dep := func() ([]source.BlockerRef, error) {
		return []source.BlockerRef{{ID: "10049", Number: "PSP-49", State: "In Progress"}}, nil
	}
	eng := depWaitEngine(store, exec, &clock, dep)

	instID, success, _ := eng.RunInstance(context.Background(), dependencyWorkflow(&config.WaitForConfig{Kind: "dependency"}), model.InternalTask{ID: "c1"})
	if success {
		t.Fatal("a dependency-parked instance should not report success")
	}
	if store.instances[instID].State != db.InstanceStateBlocked {
		t.Errorf("instance state = %q, want waiting", store.instances[instID].State)
	}
	if contains(executedIDs(exec.seen), "implement") {
		t.Error("implement must not run while a blocker is unsatisfied")
	}
	if pp := eng.ParkedWaits(); len(pp) != 1 || pp[0].Step.ID != "await-blockers" {
		t.Fatalf("expected one parked wait for await-blockers, got %+v", pp)
	}
	// The unsatisfied check is recorded for audit, like a CI poll.
	if len(store.ciPolls) == 0 || store.ciPolls[len(store.ciPolls)-1].Status != "pending" {
		t.Errorf("expected a recorded pending poll, got %+v", store.ciPolls)
	}
}

func TestDependencyWait_AutoResumesWhenBlockerDone(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	state := "In Progress"
	dep := func() ([]source.BlockerRef, error) {
		return []source.BlockerRef{{ID: "10049", Number: "PSP-49", State: state}}, nil
	}
	eng := depWaitEngine(store, exec, &clock, dep)

	instID, _, _ := eng.RunInstance(context.Background(), dependencyWorkflow(&config.WaitForConfig{Kind: "dependency"}), model.InternalTask{ID: "c1"})

	// Blocker still open → stays parked.
	eng.CheckParkedWaits(context.Background())
	if store.instances[instID].State != db.InstanceStateBlocked {
		t.Fatalf("expected still waiting, got %q", store.instances[instID].State)
	}

	// Blocker resolves → next check auto-resumes the workflow to done.
	state = "done"
	eng.CheckParkedWaits(context.Background())
	if store.instances[instID].State != db.InstanceStateDone {
		t.Errorf("expected done after blocker resolved, got %q", store.instances[instID].State)
	}
	if !contains(executedIDs(exec.seen), "implement") {
		t.Error("implement should run once the blocker is satisfied")
	}
	if len(eng.ParkedWaits()) != 0 {
		t.Error("instance should no longer be parked")
	}
}

func TestDependencyWait_NoBlockersPassesImmediately(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	dep := func() ([]source.BlockerRef, error) { return nil, nil }
	eng := depWaitEngine(store, exec, &clock, dep)

	instID, success, _ := eng.RunInstance(context.Background(), dependencyWorkflow(&config.WaitForConfig{Kind: "dependency"}), model.InternalTask{ID: "c1"})
	if !success || store.instances[instID].State != db.InstanceStateDone {
		t.Fatalf("expected immediate success with no blockers, got success=%v state=%q", success, store.instances[instID].State)
	}
}

func TestDependencyWait_SatisfiedWhenMergedOnly(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	// Blocker is Done-category but its PR is not merged: with satisfied_when
	// restricted to merged, "done" must NOT satisfy it.
	dep := func() ([]source.BlockerRef, error) {
		return []source.BlockerRef{{ID: "1", Number: "PSP-50", State: "done", Merged: false}}, nil
	}
	eng := depWaitEngine(store, exec, &clock, dep)

	wait := &config.WaitForConfig{Kind: "dependency", SatisfiedWhen: []string{"merged"}}
	instID, _, _ := eng.RunInstance(context.Background(), dependencyWorkflow(wait), model.InternalTask{ID: "c1"})
	if store.instances[instID].State != db.InstanceStateBlocked {
		t.Fatalf("done-but-unmerged blocker should keep waiting under satisfied_when [merged], got %q", store.instances[instID].State)
	}
}

func TestDependencyWait_MergedSatisfiesDefaultConditions(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	// Open blocker with a merged PR: the default [merged, done] is an OR, so
	// merged alone satisfies it.
	dep := func() ([]source.BlockerRef, error) {
		return []source.BlockerRef{{ID: "1", Number: "PSP-50", State: "In Progress", Merged: true}}, nil
	}
	eng := depWaitEngine(store, exec, &clock, dep)

	instID, success, _ := eng.RunInstance(context.Background(), dependencyWorkflow(&config.WaitForConfig{Kind: "dependency"}), model.InternalTask{ID: "c1"})
	if !success || store.instances[instID].State != db.InstanceStateDone {
		t.Fatalf("merged blocker should satisfy the default conditions, got success=%v state=%q", success, store.instances[instID].State)
	}
}

func TestDependencyWait_CheckerErrorKeepsWaiting(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	dep := func() ([]source.BlockerRef, error) { return nil, errors.New("jira down") }
	eng := depWaitEngine(store, exec, &clock, dep)

	instID, _, _ := eng.RunInstance(context.Background(), dependencyWorkflow(&config.WaitForConfig{Kind: "dependency"}), model.InternalTask{ID: "c1"})
	if store.instances[instID].State != db.InstanceStateBlocked {
		t.Fatalf("a transient lookup error should park (retry next cycle), got %q", store.instances[instID].State)
	}
	if len(store.ciPolls) == 0 || store.ciPolls[len(store.ciPolls)-1].Status != "error" {
		t.Errorf("expected a recorded error poll, got %+v", store.ciPolls)
	}
}

func TestDependencyWait_TimeoutHoldStaysParked(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	dep := func() ([]source.BlockerRef, error) {
		return []source.BlockerRef{{ID: "1", Number: "PSP-49", State: "open"}}, nil
	}
	eng := depWaitEngine(store, exec, &clock, dep)

	// on_timeout defaults to hold for kind: dependency.
	wait := &config.WaitForConfig{Kind: "dependency", MaxDuration: "1h"}
	instID, _, _ := eng.RunInstance(context.Background(), dependencyWorkflow(wait), model.InternalTask{ID: "c1"})

	clock = clock.Add(2 * time.Hour)
	eng.CheckParkedWaits(context.Background())
	if store.instances[instID].State != db.InstanceStateBlocked {
		t.Errorf("on_timeout hold should keep the instance parked, got %q", store.instances[instID].State)
	}
	if len(eng.ParkedWaits()) != 1 {
		t.Error("instance should still be parked past the deadline under hold")
	}
}

func TestDependencyWait_TimeoutFailFailsStep(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	dep := func() ([]source.BlockerRef, error) {
		return []source.BlockerRef{{ID: "1", Number: "PSP-49", State: "open"}}, nil
	}
	eng := depWaitEngine(store, exec, &clock, dep)

	wait := &config.WaitForConfig{Kind: "dependency", MaxDuration: "1h", OnTimeout: "fail"}
	instID, _, _ := eng.RunInstance(context.Background(), dependencyWorkflow(wait), model.InternalTask{ID: "c1"})

	clock = clock.Add(2 * time.Hour)
	eng.CheckParkedWaits(context.Background())
	if store.instances[instID].State != db.InstanceStateFailed {
		t.Errorf("on_timeout fail should fail the instance at the deadline, got %q", store.instances[instID].State)
	}
}

func TestDependencyWait_NoCheckerConfiguredFails(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	var seq atomic.Int64
	eng := NewEngine(baseCfg(), store, exec,
		WithSideEffects(&fakeSide{}),
		WithClock(func() time.Time { return clock }),
		WithIDGen(func(prefix string) string { return prefix + "-" + itoa(int(seq.Add(1))) }),
	)

	instID, success, _ := eng.RunInstance(context.Background(), dependencyWorkflow(&config.WaitForConfig{Kind: "dependency"}), model.InternalTask{ID: "c1"})
	if success || store.instances[instID].State != db.InstanceStateFailed {
		t.Fatalf("a dependency wait without a configured checker should fail, got success=%v state=%q", success, store.instances[instID].State)
	}
}

func TestDependencyWait_LinkTypeReachesChecker(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	clock := time.Unix(1000, 0)
	var gotLinkType string
	var seq atomic.Int64
	eng := NewEngine(baseCfg(), store, exec,
		WithSideEffects(&fakeSide{}),
		WithClock(func() time.Time { return clock }),
		WithIDGen(func(prefix string) string { return prefix + "-" + itoa(int(seq.Add(1))) }),
		WithDependencyChecker(func(_ context.Context, _, _, linkType string) ([]source.BlockerRef, error) {
			gotLinkType = linkType
			return nil, nil
		}),
	)

	wait := &config.WaitForConfig{Kind: "dependency", BlockerLinkType: "Depends"}
	_, _, _ = eng.RunInstance(context.Background(), dependencyWorkflow(wait), model.InternalTask{ID: "c1"})
	if gotLinkType != "Depends" {
		t.Errorf("blocker_link_type = %q reached the checker, want \"Depends\"", gotLinkType)
	}
}
