package workflow

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

// ciSourceWorkflow waits on CI hosted by a source OTHER than the task's own —
// the Jira-issue/GitHub-PR split (#444).
func ciSourceWorkflow() config.WorkflowConfig {
	return config.WorkflowConfig{ID: "impl", Steps: []config.StepConfig{
		{ID: "implement", Agent: "backend-dev", PullRequestFrom: "pr_url"},
		{ID: "check-ci", Type: config.StepTypeWaitFor, DependsOn: []string{"implement"},
			WaitFor: &config.WaitForConfig{Kind: "ci", CISource: "github", CheckInterval: "30s", MaxDuration: "2h"}},
		{ID: "review", Agent: "backend-dev", DependsOn: []string{"check-ci"}},
	}}
}

// ciSourceEngine is waitForEngine with a checker that sees the whole request, so
// a test can assert on what the engine asked for, not just what it got back.
func ciSourceEngine(store Store, exec StepExecutor, clock *time.Time,
	ci func(CIStatusRequest) (source.CIStatus, error)) *Engine {
	var seq atomic.Int64
	return NewEngine(baseCfg(), store, exec,
		WithSideEffects(&fakeSide{}),
		WithClock(func() time.Time { return *clock }),
		WithIDGen(func(prefix string) string { return prefix + "-" + itoa(int(seq.Add(1))) }),
		WithCIStatusChecker(func(_ context.Context, req CIStatusRequest) (source.CIStatus, error) {
			return ci(req)
		}),
	)
}

// The step's ci_source and the task id both reach the checker: without them the
// daemon cannot find which forge to ask, or about which PR.
func TestWaitCISource_RequestCarriesCISourceAndTask(t *testing.T) {
	clock := time.Unix(1000, 0)
	var got CIStatusRequest
	eng := ciSourceEngine(newFakeStore(), &fakeExecutor{}, &clock,
		func(req CIStatusRequest) (source.CIStatus, error) {
			got = req
			return source.CIStatus{Status: "pending"}, nil
		})

	eng.RunInstance(context.Background(), ciSourceWorkflow(), model.InternalTask{ID: "task-1"})

	if got.CISourceID != "github" {
		t.Errorf("CISourceID = %q, want github (the step's ci_source)", got.CISourceID)
	}
	if got.TaskID != "task-1" {
		t.Errorf("TaskID = %q, want task-1 — the PR is linked to the task, not to the source item", got.TaskID)
	}
}

// A task whose PR has not been reported yet is NOT a failure: the wait parks and
// keeps checking, exactly as it does while CI itself is pending.
func TestWaitCISource_NoLinkedPRStaysParked(t *testing.T) {
	store := newFakeStore()
	clock := time.Unix(1000, 0)
	linked := false
	eng := ciSourceEngine(store, &fakeExecutor{}, &clock,
		func(CIStatusRequest) (source.CIStatus, error) {
			if !linked {
				return source.CIStatus{}, fmt.Errorf("task t1: %w", ErrPRNotLinked)
			}
			return source.CIStatus{Status: "passed"}, nil
		})

	instID, success, _ := eng.RunInstance(context.Background(), ciSourceWorkflow(), model.InternalTask{ID: "t1"})
	if success {
		t.Fatal("instance reported success while no PR was linked yet")
	}
	if state := store.instances[instID].State; state != db.InstanceStateWaiting {
		t.Fatalf("instance state = %q, want waiting — an unlinked PR must not fail the wait", state)
	}

	// Once a step reports the PR, the very next check advances the workflow.
	linked = true
	eng.CheckParkedWaits(context.Background())
	if state := store.instances[instID].State; state != db.InstanceStateDone {
		t.Errorf("instance state = %q, want done once the PR is linked and CI is green", state)
	}
}

// A ci_source that cannot poll (missing source, wrong adapter) is permanent, so
// the wait fails at once instead of polling a capability that will never appear.
func TestWaitCISource_UnsupportedFailsImmediately(t *testing.T) {
	store := newFakeStore()
	clock := time.Unix(1000, 0)
	eng := ciSourceEngine(store, &fakeExecutor{}, &clock,
		func(CIStatusRequest) (source.CIStatus, error) {
			return source.CIStatus{}, fmt.Errorf("ci_source %q is not a configured source: %w", "github", source.ErrUnsupported)
		})

	instID, success, _ := eng.RunInstance(context.Background(), ciSourceWorkflow(), model.InternalTask{ID: "t1"})
	if success {
		t.Fatal("a wait against an unusable ci_source must not pass")
	}
	if state := store.instances[instID].State; state == db.InstanceStateWaiting {
		t.Error("instance parked on a permanently unsupported ci_source; want a terminal failure")
	}
}
