package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/queue"
	"github.com/orlandoburli/apiary/internal/router"
)

func pendingTestDispatcher(t *testing.T) (*Dispatcher, *db.Client) {
	t.Helper()
	dbc := openDropTestDB(t, "pending.db")
	d := &Dispatcher{
		db:             dbc,
		dispatchQueue:  dbc.Queue(),
		queueProjectID: "project",
		cfg:            &config.Config{},
		agentRunner:    map[string]string{},
	}
	return d, dbc
}

func createPendingTask(t *testing.T, dbc *db.Client, id string) model.InternalTask {
	t.Helper()
	task := &model.InternalTask{ID: id, Title: id, State: model.TaskStateRunning}
	if err := dbc.InternalTasks().CreateTask(context.Background(), task); err != nil {
		t.Fatalf("create task %s: %v", id, err)
	}
	return *task
}

// Issue #537 surface 1: an occurrence that would exceed the workflow's pending
// cap is not enqueued, the drop is recorded and logged, and a third occurrence
// succeeds once the first job reaches a terminal state.
func TestEnqueueFanOutCoalescesPendingPerWorkflow(t *testing.T) {
	ctx := context.Background()
	d, dbc := pendingTestDispatcher(t)
	store := dbc.Queue()
	route := config.RouteConfig{ID: "nightly-sweep", Agent: "eng", MaxPending: 1}
	cell := model.SourceItem{ID: "cron-1", SourceID: "cron"}

	first := createPendingTask(t, dbc, "T1")
	d.enqueueFanOut(ctx, cell, first, []router.Match{{Route: route}}, "")
	jobs, err := store.ListJobs(ctx, queue.JobQueued, 10)
	if err != nil {
		t.Fatalf("list queued: %v", err)
	}
	if len(jobs) != 1 || jobs[0].WorkflowID != "nightly-sweep" {
		t.Fatalf("first occurrence must enqueue; jobs=%+v", jobs)
	}

	// A distinct occurrence (different task, same workflow) over the cap.
	second := createPendingTask(t, dbc, "T2")
	d.enqueueFanOut(ctx, model.SourceItem{ID: "cron-2", SourceID: "cron"}, second, []router.Match{{Route: route}}, "")
	jobs, err = store.ListJobs(ctx, queue.JobQueued, 10)
	if err != nil {
		t.Fatalf("list queued: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("second occurrence must be coalesced; %d queued jobs", len(jobs))
	}
	events, err := dbc.ListExecutionEvents(ctx, db.ExecutionEventFilter{TaskID: "T2", Type: "dispatch.dropped"})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 dispatch.dropped event, got %d", len(events))
	}
	if events[0].WorkflowID != "nightly-sweep" || events[0].Metadata["reason"] != "pending cap" {
		t.Errorf("event must name the workflow and the pending cap reason; got %+v", events[0])
	}
	// The drop rolls the outstanding counter back, exactly like the idempotency drop.
	stored, err := dbc.InternalTasks().GetTask(ctx, "T2")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if stored.OutstandingWorkflows != 0 {
		t.Errorf("dropped occurrence left outstanding=%d, want 0", stored.OutstandingWorkflows)
	}

	// Finish the first job, then the next occurrence is admitted again.
	if err := store.RegisterWorker(ctx, &queue.Worker{ID: "worker", ProtocolVersion: queue.WorkerProtocolVersion, Pool: "default", Capabilities: []string{"apiary.workflow"}, Capacity: 1, Ready: true}); err != nil {
		t.Fatalf("register worker: %v", err)
	}
	claim, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker", LeaseDuration: time.Minute, WorkerTimeout: time.Minute})
	if err != nil || claim == nil {
		t.Fatalf("claim: %v (claim=%v)", err, claim)
	}
	if err := store.Finish(ctx, claim.Job.ID, claim.Attempt.ID, claim.Attempt.ClaimToken, queue.FinishResult{Success: true}); err != nil {
		t.Fatalf("finish: %v", err)
	}

	third := createPendingTask(t, dbc, "T3")
	d.enqueueFanOut(ctx, model.SourceItem{ID: "cron-3", SourceID: "cron"}, third, []router.Match{{Route: route}}, "")
	jobs, err = store.ListJobs(ctx, queue.JobQueued, 10)
	if err != nil {
		t.Fatalf("list queued: %v", err)
	}
	if len(jobs) != 1 || jobs[0].TaskID != "T3" {
		t.Fatalf("after the first job is terminal the next occurrence must enqueue; jobs=%+v", jobs)
	}
}

// The cap is per workflow, not global: another workflow still enqueues while one
// is at its cap.
func TestEnqueueFanOutPendingCapIsPerWorkflow(t *testing.T) {
	ctx := context.Background()
	d, dbc := pendingTestDispatcher(t)

	cappedRoute := config.RouteConfig{ID: "nightly-sweep", Agent: "eng", MaxPending: 1}
	otherRoute := config.RouteConfig{ID: "nightly-audit", Agent: "eng", MaxPending: 1}

	first := createPendingTask(t, dbc, "T1")
	d.enqueueFanOut(ctx, model.SourceItem{ID: "cron-1", SourceID: "cron"}, first, []router.Match{{Route: cappedRoute}}, "")

	// Same workflow on another task → dropped.
	second := createPendingTask(t, dbc, "T2")
	d.enqueueFanOut(ctx, model.SourceItem{ID: "cron-2", SourceID: "cron"}, second, []router.Match{{Route: cappedRoute}}, "")

	// A different workflow is unaffected.
	other := createPendingTask(t, dbc, "T3")
	d.enqueueFanOut(ctx, model.SourceItem{ID: "cron-3", SourceID: "cron"}, other, []router.Match{{Route: otherRoute}}, "")

	jobs, err := dbc.Queue().ListJobs(ctx, queue.JobQueued, 10)
	if err != nil {
		t.Fatalf("list queued: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("%d queued jobs, want 2 (one per workflow)", len(jobs))
	}
	workflows := map[string]bool{}
	for _, job := range jobs {
		workflows[job.WorkflowID] = true
	}
	if !workflows["nightly-sweep"] || !workflows["nightly-audit"] {
		t.Errorf("queued workflows = %v, want nightly-sweep and nightly-audit", workflows)
	}
}

// Regression: without max_pending the fan-out still enqueues every occurrence,
// exactly as it did before the option existed.
func TestEnqueueFanOutWithoutMaxPendingEnqueuesEveryOccurrence(t *testing.T) {
	ctx := context.Background()
	d, dbc := pendingTestDispatcher(t)
	route := config.RouteConfig{ID: "nightly-sweep", Agent: "eng"}

	first := createPendingTask(t, dbc, "T1")
	second := createPendingTask(t, dbc, "T2")
	d.enqueueFanOut(ctx, model.SourceItem{ID: "cron-1", SourceID: "cron"}, first, []router.Match{{Route: route}}, "")
	d.enqueueFanOut(ctx, model.SourceItem{ID: "cron-2", SourceID: "cron"}, second, []router.Match{{Route: route}}, "")

	jobs, err := dbc.Queue().ListJobs(ctx, queue.JobQueued, 10)
	if err != nil {
		t.Fatalf("list queued: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("%d queued jobs, want 2 (default is unlimited)", len(jobs))
	}
	events, err := dbc.ListExecutionEvents(ctx, db.ExecutionEventFilter{Type: "dispatch.dropped"})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("default fan-out must not drop anything; got %+v", events)
	}
}
