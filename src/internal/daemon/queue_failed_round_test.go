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

// A task whose round died — an interrupted instance, a failed dispatch job —
// must be able to start a new round.
//
// The queue keys jobs on taskID:generation:routeID, and the generation only
// advanced for a task that IncrementOutstanding could reopen (settled, or
// waiting out a retry backoff). A task blocked on *interrupted* was neither,
// so its key stayed pinned to the dead round's job: every poll re-tried the
// same key, the enqueue was swallowed as a duplicate of a terminal job, and the
// task's work was never picked up again. The daemon logged the drop once a
// minute, forever.
//
// This pins the fix: the failed round's key is superseded, so the next poll
// finds a free key and the work runs.
func TestFailedRoundAdvancesTheDispatchKey(t *testing.T) {
	ctx := context.Background()
	d, dbc := pendingTestDispatcher(t)
	store := dbc.Queue()
	route := config.RouteConfig{ID: "implementation", Agent: "engineer"}
	cell := model.SourceItem{ID: "cell-1", SourceID: "project-erp"}

	task := createPendingTask(t, dbc, "T-stuck")

	// Round 0: the job is enqueued and then fails.
	d.enqueueFanOut(ctx, cell, task, []router.Match{{Route: route}}, "")
	jobs, err := store.ListJobs(ctx, queue.JobQueued, 10)
	if err != nil {
		t.Fatalf("list queued: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("first round must enqueue; jobs=%+v", jobs)
	}
	failFirstJob(t, ctx, store)

	// The task is now blocked/interrupted — the shape the daemon leaves behind
	// when a daemon restart interrupts a run.
	if err := dbc.InternalTasks().UpdateTaskStateReason(ctx, task.ID, model.TaskStateBlocked, "interrupted"); err != nil {
		t.Fatalf("set task state: %v", err)
	}

	genBefore, err := dbc.InternalTasks().Generation(ctx, task.ID)
	if err != nil {
		t.Fatalf("read generation: %v", err)
	}

	// The next poll re-dispatches the task. The key is still held by the
	// failed job, so the dispatch is dropped — but the round is over, so the
	// generation must move.
	d.enqueueFanOut(ctx, cell, task, []router.Match{{Route: route}}, "")

	genAfter, err := dbc.InternalTasks().Generation(ctx, task.ID)
	if err != nil {
		t.Fatalf("read generation: %v", err)
	}
	if genAfter <= genBefore {
		t.Fatalf("the dispatch generation did not advance past the failed round: before=%d after=%d — the key stays pinned to the dead job and the task can never be re-dispatched",
			genBefore, genAfter)
	}

	// And the next poll after that must actually enqueue the work.
	d.enqueueFanOut(ctx, cell, task, []router.Match{{Route: route}}, "")
	jobs, err = store.ListJobs(ctx, queue.JobQueued, 10)
	if err != nil {
		t.Fatalf("list queued: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("the task must be re-dispatched after the failed round was superseded; %d queued jobs", len(jobs))
	}
	if jobs[0].IdempotencyKey == "T-stuck:0:implementation" {
		t.Errorf("the re-dispatched job still uses the dead round's key: %q", jobs[0].IdempotencyKey)
	}
}

// A canceled round must keep its key: cancellation is an explicit operator
// action, and the round stays canceled until a re-dispatch bumps the
// generation. Advancing it here would undo the cancellation on the next poll.
func TestCanceledRoundKeepsItsDispatchKey(t *testing.T) {
	ctx := context.Background()
	d, dbc := pendingTestDispatcher(t)
	store := dbc.Queue()
	route := config.RouteConfig{ID: "implementation", Agent: "engineer"}
	cell := model.SourceItem{ID: "cell-1", SourceID: "project-erp"}

	task := createPendingTask(t, dbc, "T-canceled")
	d.enqueueFanOut(ctx, cell, task, []router.Match{{Route: route}}, "")
	jobs, err := store.ListJobs(ctx, queue.JobQueued, 10)
	if err != nil {
		t.Fatalf("list queued: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("first round must enqueue; jobs=%+v", jobs)
	}
	if err := store.RequestCancel(ctx, jobs[0].ID); err != nil {
		t.Fatalf("cancel the job: %v", err)
	}

	genBefore, err := dbc.InternalTasks().Generation(ctx, task.ID)
	if err != nil {
		t.Fatalf("read generation: %v", err)
	}

	d.enqueueFanOut(ctx, cell, task, []router.Match{{Route: route}}, "")

	genAfter, err := dbc.InternalTasks().Generation(ctx, task.ID)
	if err != nil {
		t.Fatalf("read generation: %v", err)
	}
	if genAfter != genBefore {
		t.Errorf("a canceled round must keep its dispatch key, but the generation moved: before=%d after=%d",
			genBefore, genAfter)
	}
}

// A round that is still pending is the intended de-duplication of an
// overlapping poll: nothing may advance, and nothing may be enqueued twice.
func TestPendingRoundIsStillDeduplicated(t *testing.T) {
	ctx := context.Background()
	d, dbc := pendingTestDispatcher(t)
	store := dbc.Queue()
	route := config.RouteConfig{ID: "implementation", Agent: "engineer"}
	cell := model.SourceItem{ID: "cell-1", SourceID: "project-erp"}

	task := createPendingTask(t, dbc, "T-pending")
	d.enqueueFanOut(ctx, cell, task, []router.Match{{Route: route}}, "")

	genBefore, err := dbc.InternalTasks().Generation(ctx, task.ID)
	if err != nil {
		t.Fatalf("read generation: %v", err)
	}
	d.enqueueFanOut(ctx, cell, task, []router.Match{{Route: route}}, "")

	genAfter, err := dbc.InternalTasks().Generation(ctx, task.ID)
	if err != nil {
		t.Fatalf("read generation: %v", err)
	}
	if genAfter != genBefore {
		t.Errorf("an overlapping poll must not advance the generation: before=%d after=%d", genBefore, genAfter)
	}
	jobs, err := store.ListJobs(ctx, queue.JobQueued, 10)
	if err != nil {
		t.Fatalf("list queued: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("an overlapping poll must not enqueue a second job; %d queued jobs", len(jobs))
	}
}

// The interrupted task's own state must survive the supersede: the instance
// that was interrupted is still on record, and reopening the task would let a
// second run start against a workflow that is already live.
func TestFailedRoundSupersedeLeavesTheTaskStateAlone(t *testing.T) {
	ctx := context.Background()
	d, dbc := pendingTestDispatcher(t)
	store := dbc.Queue()
	route := config.RouteConfig{ID: "implementation", Agent: "engineer"}
	cell := model.SourceItem{ID: "cell-1", SourceID: "project-erp"}

	task := createPendingTask(t, dbc, "T-interrupted")
	d.enqueueFanOut(ctx, cell, task, []router.Match{{Route: route}}, "")
	if _, err := store.ListJobs(ctx, queue.JobQueued, 10); err != nil {
		t.Fatalf("list queued: %v", err)
	}
	failFirstJob(t, ctx, store)
	if err := dbc.InternalTasks().UpdateTaskStateReason(ctx, task.ID, model.TaskStateBlocked, "interrupted"); err != nil {
		t.Fatalf("set task state: %v", err)
	}

	d.enqueueFanOut(ctx, cell, task, []router.Match{{Route: route}}, "")

	got, err := dbc.InternalTasks().GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	if got.State != model.TaskStateBlocked || got.BlockedReason != "interrupted" {
		t.Errorf("the task state was changed by the supersede: state=%q reason=%q, want blocked/interrupted", got.State, got.BlockedReason)
	}
}

// The advance must be reported, not silent: a task whose dispatch is being
// retried after a failed round is worth seeing in the log.
func TestFailedRoundSupersedeIsRecorded(t *testing.T) {
	ctx := context.Background()
	d, dbc := pendingTestDispatcher(t)
	store := dbc.Queue()
	route := config.RouteConfig{ID: "implementation", Agent: "engineer"}
	cell := model.SourceItem{ID: "cell-1", SourceID: "project-erp"}

	task := createPendingTask(t, dbc, "T-recorded")
	d.enqueueFanOut(ctx, cell, task, []router.Match{{Route: route}}, "")
	if _, err := store.ListJobs(ctx, queue.JobQueued, 10); err != nil {
		t.Fatalf("list queued: %v", err)
	}
	failFirstJob(t, ctx, store)

	d.enqueueFanOut(ctx, cell, task, []router.Match{{Route: route}}, "")

	events, err := dbc.ListExecutionEvents(ctx, db.ExecutionEventFilter{TaskID: task.ID, Type: "dispatch.dropped"})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	found := false
	for _, e := range events {
		if e.Metadata["reason"] == "failed round superseded" {
			found = true
		}
	}
	if !found {
		t.Errorf("the supersede must be recorded as an event; got %+v", events)
	}
}

// failFirstJob claims the oldest queued job and fails it, the way a run that
// dies leaves it: the job reaches a terminal 'failed' state.
func failFirstJob(t *testing.T, ctx context.Context, store *db.QueueStore) {
	t.Helper()
	if err := store.RegisterWorker(ctx, &queue.Worker{ID: "worker", ProtocolVersion: queue.WorkerProtocolVersion, Pool: "default", Capabilities: []string{"apiary.workflow"}, Capacity: 1, Ready: true}); err != nil {
		t.Fatalf("register worker: %v", err)
	}
	claim, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker", LeaseDuration: time.Minute, WorkerTimeout: time.Minute})
	if err != nil || claim == nil {
		t.Fatalf("claim: %v (claim=%v)", err, claim)
	}
	if err := store.Finish(ctx, claim.Job.ID, claim.Attempt.ID, claim.Attempt.ClaimToken, queue.FinishResult{Error: "boom"}); err != nil {
		t.Fatalf("fail the job: %v", err)
	}
}
