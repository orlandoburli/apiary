package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/queue"
)

// Regression for #369. Multiple workers finishing jobs at the same time used to
// lose the finish write: every transaction here reads and then writes, and under
// the default deferred transaction mode SQLite fails such a transaction with
// SQLITE_BUSY_SNAPSHOT the moment another connection commits first — immediately,
// without consulting busy_timeout. A lost finish leaves the job leased, so it is
// re-run when the lease expires and the agent's work (and its side effects) are
// duplicated.
//
// The test hammers claim/heartbeat/finish from parallel goroutines and asserts
// every job reached a terminal state exactly once, with no lock errors.
func TestQueueConcurrentFinishUnderContention(t *testing.T) {
	ctx := context.Background()
	client, _ := queueTestClient(t)
	defer client.Close()
	store := client.Queue()

	const (
		workers     = 8
		jobsPerWork = 6
		totalJobs   = workers * jobsPerWork
	)

	registerQueueWorker(t, store, "worker", totalJobs, nil, nil)

	for i := 0; i < totalJobs; i++ {
		job := testJob(fmt.Sprintf("task:concurrent:%d", i))
		if _, err := store.Enqueue(ctx, job); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	var (
		mu       sync.Mutex
		failures []error
		finished int
	)
	record := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		failures = append(failures, err)
	}

	// Every goroutine races the others through the full claim → heartbeat →
	// finish cycle, which is where the interleaved read-then-write transactions
	// collide.
	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < jobsPerWork; i++ {
				claim, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker", LeaseDuration: time.Minute})
				if err != nil {
					record(fmt.Errorf("claim: %w", err))
					return
				}
				if claim == nil {
					return // queue drained
				}
				if _, err := store.Heartbeat(ctx, claim.Job.ID, claim.Attempt.ID, claim.Attempt.ClaimToken, time.Minute); err != nil {
					record(fmt.Errorf("heartbeat %s: %w", claim.Job.ID, err))
					return
				}
				if err := store.Finish(ctx, claim.Job.ID, claim.Attempt.ID, claim.Attempt.ClaimToken, queue.FinishResult{Success: true}); err != nil {
					record(fmt.Errorf("finish %s: %w", claim.Job.ID, err))
					return
				}
				mu.Lock()
				finished++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range failures {
		if isBusy(err) {
			t.Errorf("lock contention surfaced to the caller: %v", err)
		} else {
			t.Errorf("unexpected error: %v", err)
		}
	}

	// Every finish must have stuck: a job still leased is one that would be
	// re-run, which is the duplicate-work symptom from the issue.
	succeeded, err := store.ListJobs(ctx, queue.JobSucceeded, totalJobs+1)
	if err != nil {
		t.Fatalf("list succeeded: %v", err)
	}
	if len(succeeded) != finished {
		t.Errorf("succeeded=%d but %d finishes reported success — finish writes were lost", len(succeeded), finished)
	}
	leased, err := store.ListJobs(ctx, queue.JobLeased, totalJobs+1)
	if err != nil {
		t.Fatalf("list leased: %v", err)
	}
	if len(leased) != 0 {
		t.Errorf("%d job(s) left leased after all workers finished; they would be re-run on lease expiry", len(leased))
	}
}

// A busy failure must not leave a half-applied transaction behind: retryOnBusy
// re-runs the whole operation, and a guard-clause transaction whose race was
// lost must still report ErrStaleClaim rather than double-finishing.
func TestQueueDoubleFinishIsStaleNotDuplicated(t *testing.T) {
	ctx := context.Background()
	client, _ := queueTestClient(t)
	defer client.Close()
	store := client.Queue()

	registerQueueWorker(t, store, "worker", 1, nil, nil)
	if _, err := store.Enqueue(ctx, testJob("task:double-finish")); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker", LeaseDuration: time.Minute})
	if err != nil || claim == nil {
		t.Fatalf("claim: %v (claim=%v)", err, claim)
	}

	if err := store.Finish(ctx, claim.Job.ID, claim.Attempt.ID, claim.Attempt.ClaimToken, queue.FinishResult{Success: true}); err != nil {
		t.Fatalf("first finish: %v", err)
	}
	err = store.Finish(ctx, claim.Job.ID, claim.Attempt.ID, claim.Attempt.ClaimToken, queue.FinishResult{Success: true})
	if !errors.Is(err, queue.ErrStaleClaim) {
		t.Errorf("second finish = %v, want ErrStaleClaim", err)
	}
}

// sqliteBusyErr returns a real SQLITE_BUSY from the driver rather than a
// hand-made value: modernc's Error has no exported constructor, and a fabricated
// error would not prove isBusy recognises what SQLite actually returns. A second
// connection with no busy timeout writes while the first holds the write lock.
func sqliteBusyErr(t *testing.T) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "busy.db")

	holder, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(0)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(context.Background(), `CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}

	// Hold the write lock open for the duration of the contending write.
	tx, err := holder.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO t (id) VALUES (1)`); err != nil {
		t.Fatal(err)
	}

	contender, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(0)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	_, err = contender.ExecContext(context.Background(), `INSERT INTO t (id) VALUES (2)`)
	if err == nil {
		t.Fatal("expected the contending write to fail while the write lock is held")
	}
	return err
}

func TestIsBusyClassification(t *testing.T) {
	if !isBusy(sqliteBusyErr(t)) {
		t.Error("a real SQLITE_BUSY from the driver must be classified as busy")
	}
	if isBusy(nil) {
		t.Error("nil is not a busy error")
	}
	if isBusy(errors.New("database is locked")) {
		t.Error("a plain error with a matching message must not be classified as busy")
	}
	if isBusy(queue.ErrStaleClaim) {
		t.Error("ErrStaleClaim is not a busy error")
	}
}

// retryOnBusy must give up on a cancelled context instead of sleeping out the
// whole schedule.
func TestRetryOnBusyHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	err := retryOnBusy(ctx, func() error {
		calls++
		return sqliteBusyErr(t)
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Errorf("op ran %d times, want 1 before the cancelled context stopped it", calls)
	}
}

// retryOnBusy must stop after the schedule is exhausted and return the error.
func TestRetryOnBusyGivesUpAndReturnsTheError(t *testing.T) {
	calls := 0
	err := retryOnBusy(context.Background(), func() error {
		calls++
		return sqliteBusyErr(t)
	})
	if !isBusy(err) {
		t.Errorf("err = %v, want a busy error", err)
	}
	if want := len(busyRetryDelays) + 1; calls != want {
		t.Errorf("op ran %d times, want %d (initial attempt plus the retry schedule)", calls, want)
	}
}

// A non-busy error must not be retried.
func TestRetryOnBusyDoesNotRetryOtherErrors(t *testing.T) {
	calls := 0
	sentinel := errors.New("constraint violation")
	err := retryOnBusy(context.Background(), func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want the original error", err)
	}
	if calls != 1 {
		t.Errorf("op ran %d times, want 1", calls)
	}
}

// Each concurrency dimension must deny a second claim of the same scope while
// the first job is leased, and allow it again once that lease finishes. The
// workflow dimension is the #537 surface 2 addition: a scheduled routine could
// not be capped on its own because the queue had no workflow scope.
func TestQueueConcurrencyPolicyScopes(t *testing.T) {
	scopes := []struct {
		name   string
		job    func(*queue.Job)
		policy queue.ConcurrencyPolicy
	}{
		{"project", func(job *queue.Job) { job.ProjectID = "scoped" }, queue.ConcurrencyPolicy{DefaultProject: 1}},
		{"source", func(job *queue.Job) { job.SourceID = "scoped" }, queue.ConcurrencyPolicy{DefaultSource: 1}},
		{"agent", func(job *queue.Job) { job.AgentID = "scoped" }, queue.ConcurrencyPolicy{DefaultAgent: 1}},
		{"runner", func(job *queue.Job) { job.RunnerID = "scoped" }, queue.ConcurrencyPolicy{DefaultRunner: 1}},
		{"pool", func(job *queue.Job) { job.Pool = "default" }, queue.ConcurrencyPolicy{Pools: map[string]int{"default": 1}}},
		{"default pool", func(job *queue.Job) { job.Pool = "default" }, queue.ConcurrencyPolicy{DefaultPool: 1}},
		{"workflow", func(job *queue.Job) { job.WorkflowID = "sweep" }, queue.ConcurrencyPolicy{Workflows: map[string]int{"sweep": 1}}},
		{"default workflow", func(job *queue.Job) { job.WorkflowID = "sweep" }, queue.ConcurrencyPolicy{DefaultWorkflow: 1}},
	}
	for _, scope := range scopes {
		t.Run(scope.name, func(t *testing.T) {
			ctx := context.Background()
			client, _ := queueTestClient(t)
			defer client.Close()
			store := client.Queue()

			var jobs []*queue.Job
			for i := 0; i < 2; i++ {
				job := testJob(fmt.Sprintf("%s-limit-%d", scope.name, i))
				scope.job(job)
				if _, err := store.Enqueue(ctx, job); err != nil {
					t.Fatal(err)
				}
				jobs = append(jobs, job)
			}
			registerQueueWorker(t, store, "worker", 2, nil, nil)

			first, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker", Policy: scope.policy})
			if err != nil {
				t.Fatalf("first claim: %v", err)
			}
			if _, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker", Policy: scope.policy}); !errors.Is(err, queue.ErrNoJob) {
				t.Fatalf("second claim = %v, want ErrNoJob while the scope is at its limit", err)
			}
			// A concurrency limit delays the sibling job; it must stay queued
			// rather than be failed, canceled, or dropped.
			for _, job := range jobs {
				if job.ID == first.Job.ID {
					continue
				}
				pending, err := store.GetJob(ctx, job.ID)
				if err != nil {
					t.Fatalf("get denied job: %v", err)
				}
				if pending.State != queue.JobQueued {
					t.Fatalf("denied job state = %s, want %s", pending.State, queue.JobQueued)
				}
				if pending.AttemptCount != 0 {
					t.Fatalf("denied job attempt_count = %d, want 0", pending.AttemptCount)
				}
			}
			if err := store.Finish(ctx, first.Job.ID, first.Attempt.ID, first.Attempt.ClaimToken, queue.FinishResult{Success: true}); err != nil {
				t.Fatalf("finish: %v", err)
			}
			if _, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker", Policy: scope.policy}); err != nil {
				t.Fatalf("claim after the lease finished: %v", err)
			}
		})
	}
}

// The point of a workflow scope: capping one workflow must not throttle a
// different workflow that shares every other scope with it, so a slow routine
// can be capped without touching an agent it shares with product work.
func TestQueueWorkflowConcurrencyIsIndependentOfOtherWorkflows(t *testing.T) {
	ctx := context.Background()
	client, _ := queueTestClient(t)
	defer client.Close()
	store := client.Queue()

	policy := queue.ConcurrencyPolicy{Workflows: map[string]int{"sweep": 1}}
	registerQueueWorker(t, store, "worker", 2, nil, nil)

	sweep := testJob("sweep-0")
	sweep.WorkflowID = "sweep"
	if _, err := store.Enqueue(ctx, sweep); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker", Policy: policy}); err != nil {
		t.Fatalf("claim sweep: %v", err)
	}
	// The sweep lease is still active, so another sweep would be denied — but a
	// different workflow must lease normally.
	build := testJob("build-0")
	build.WorkflowID = "build"
	if _, err := store.Enqueue(ctx, build); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker", Policy: policy}); err != nil {
		t.Fatalf("claim build while sweep is at its limit: %v", err)
	}
}
