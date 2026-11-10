package db

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/queue"
)

// The pending cap must coalesce occurrences of the same workflow, count a leased
// (running) job as pending, and let a new occurrence in once the previous job is
// terminal.
func TestEnqueueCappedCoalescesPerWorkflow(t *testing.T) {
	ctx := context.Background()
	client, _ := queueTestClient(t)
	defer client.Close()
	store := client.Queue()

	first := testJob("task-1:0:sweep")
	first.WorkflowID = "sweep"
	result, err := store.EnqueueCapped(ctx, first, 1)
	if err != nil || !result.Created {
		t.Fatalf("first enqueue: result=%+v err=%v", result, err)
	}

	second := testJob("task-2:0:sweep")
	second.WorkflowID = "sweep"
	result, err = store.EnqueueCapped(ctx, second, 1)
	if err != nil {
		t.Fatalf("second enqueue: %v", err)
	}
	if result.Created || !result.Capped {
		t.Fatalf("second occurrence must be coalesced; result=%+v", result)
	}
	if result.Pending != 1 {
		t.Errorf("capped result must report the pending count; got %d, want 1", result.Pending)
	}

	// A different workflow is unaffected by the cap.
	other := testJob("task-3:0:audit")
	other.WorkflowID = "audit"
	result, err = store.EnqueueCapped(ctx, other, 1)
	if err != nil || !result.Created {
		t.Fatalf("different workflow must enqueue; result=%+v err=%v", result, err)
	}

	// Lease (run) the first job. Pending still includes the now-running job.
	registerQueueWorker(t, store, "worker", 1, nil, []string{"apiary.workflow"})
	claim, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker", LeaseDuration: time.Minute, WorkerTimeout: time.Minute})
	if err != nil || claim == nil {
		t.Fatalf("claim: %v (claim=%v)", err, claim)
	}
	if claim.Job.WorkflowID != "sweep" {
		t.Fatalf("claimed %s, want the oldest sweep job", claim.Job.WorkflowID)
	}
	stillPending := testJob("task-4:0:sweep")
	stillPending.WorkflowID = "sweep"
	result, err = store.EnqueueCapped(ctx, stillPending, 1)
	if err != nil {
		t.Fatalf("enqueue while running: %v", err)
	}
	if !result.Capped {
		t.Fatalf("a leased job counts as pending; result=%+v", result)
	}

	// Finish the running job: its workflow has no pending work again.
	if err := store.Finish(ctx, claim.Job.ID, claim.Attempt.ID, claim.Attempt.ClaimToken, queue.FinishResult{Success: true}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	third := testJob("task-5:0:sweep")
	third.WorkflowID = "sweep"
	result, err = store.EnqueueCapped(ctx, third, 1)
	if err != nil || !result.Created {
		t.Fatalf("after the pending job is terminal a new occurrence must enqueue; result=%+v err=%v", result, err)
	}
}

// Two enqueues racing for the same workflow must not both pass a cap of 1. The
// read-check-insert runs in one immediate transaction, so exactly one wins.
func TestEnqueueCappedConcurrentCreatesOne(t *testing.T) {
	ctx := context.Background()
	client, _ := queueTestClient(t)
	defer client.Close()
	store := client.Queue()

	const racers = 8
	results := make([]EnqueueResult, racers)
	errs := make([]error, racers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			job := testJob(fmt.Sprintf("task-%d:0:sweep", i))
			job.WorkflowID = "sweep"
			results[i], errs[i] = store.EnqueueCapped(ctx, job, 1)
		}(i)
	}
	close(start)
	wg.Wait()

	created := 0
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("racer %d: %v", i, errs[i])
		}
		if results[i].Created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("%d racers created a job, want exactly 1 (cap overshot)", created)
	}
	jobs, err := store.ListJobs(ctx, queue.JobQueued, racers+1)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("%d queued jobs, want 1", len(jobs))
	}
}
