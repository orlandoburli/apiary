package db

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/queue"
)

func queueTestClient(t *testing.T) (*Client, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queue.db")
	client, err := New(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return client, path
}

func testJob(key string) *queue.Job {
	return &queue.Job{IdempotencyKey: key, ProjectID: "project", SourceID: "source", AgentID: "engineer", RunnerID: "codex", Pool: "default", PayloadVersion: 1, Payload: []byte(`{"task":"work"}`), MaxAttempts: 3}
}

func registerQueueWorker(t *testing.T, store *QueueStore, id string, capacity int, labels, capabilities []string) {
	t.Helper()
	err := store.RegisterWorker(context.Background(), &queue.Worker{ID: id, ProtocolVersion: queue.WorkerProtocolVersion, Pool: "default", Labels: labels, Capabilities: capabilities, Capacity: capacity, Ready: true})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRegisterWorkerResetsStaleActiveJobs pins the restart invariant behind
// #375's "enqueued but never leased" residue. active_jobs is a durable counter
// incremented at claim and decremented at finish; a process killed mid-job never
// decrements it. Claim refuses every job while active_jobs >= capacity and the
// embedded worker's default capacity is 1, so a registration that carried the
// stale count forward meant the restarted daemon leased nothing at all — jobs
// piled up queued with no error anywhere. A worker registering owns no leases by
// definition, so re-registration must zero the counter.
func TestRegisterWorkerResetsStaleActiveJobs(t *testing.T) {
	ctx := context.Background()
	client, _ := queueTestClient(t)
	store := client.Queue()
	registerQueueWorker(t, store, "worker-1", 1, nil, nil)

	if _, err := store.Enqueue(ctx, testJob("task:1:implement")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker-1", LeaseDuration: time.Hour, WorkerTimeout: time.Hour}); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// The daemon is killed here: the lease is still live and active_jobs is 1.
	registerQueueWorker(t, store, "worker-1", 1, nil, nil)

	if _, err := store.Enqueue(ctx, testJob("task:1:review")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker-1", LeaseDuration: time.Hour, WorkerTimeout: time.Hour}); err != nil {
		t.Fatalf("a freshly registered worker owns no leases and must be able to claim; got %v", err)
	}
}

// TestFinishRecordsSkipNoteOnAttempt pins #380's "distinguishable outcome"
// requirement without adding a queue job state: a job that succeeded while doing
// no work carries its explanation on the attempt row, so `succeeded` rows can be
// told apart in the DB.
func TestFinishRecordsSkipNoteOnAttempt(t *testing.T) {
	ctx := context.Background()
	client, _ := queueTestClient(t)
	store := client.Queue()
	registerQueueWorker(t, store, "worker-1", 2, nil, nil)
	if _, err := store.Enqueue(ctx, testJob("task:1:implement")); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker-1", LeaseDuration: time.Hour, WorkerTimeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(ctx, claim.Job.ID, claim.Attempt.ID, claim.Attempt.ClaimToken, queue.FinishResult{Success: true, Note: "skipped: redelivery of an already-completed workflow"}); err != nil {
		t.Fatal(err)
	}
	var message string
	if err := client.db.QueryRowContext(ctx, `SELECT COALESCE(error_message,'') FROM dispatch_attempts WHERE id=?`, claim.Attempt.ID).Scan(&message); err != nil {
		t.Fatal(err)
	}
	if message != "skipped: redelivery of an already-completed workflow" {
		t.Errorf("attempt must record the skip note; got %q", message)
	}
}

func TestQueuePersistsAndEnqueueIsIdempotent(t *testing.T) {
	ctx := context.Background()
	client, path := queueTestClient(t)
	job := testJob("task:workflow:generation")
	created, err := client.Queue().Enqueue(ctx, job)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	duplicate := testJob(job.IdempotencyKey)
	created, err = client.Queue().Enqueue(ctx, duplicate)
	if err != nil || created || duplicate.ID != job.ID {
		t.Fatalf("duplicate created=%v id=%s want=%s err=%v", created, duplicate.ID, job.ID, err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	stored, err := reopened.Queue().GetJob(ctx, job.ID)
	if err != nil || stored.State != queue.JobQueued || string(stored.Payload) != string(job.Payload) {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

func TestQueueClaimsOnlyCompatibleWorkerAndHonorsDrain(t *testing.T) {
	ctx := context.Background()
	client, _ := queueTestClient(t)
	defer client.Close()
	store := client.Queue()
	job := testJob("compatible")
	job.RequiredLabels = []string{"linux", "gpu"}
	job.RequiredCapabilities = []string{"docker"}
	if _, err := store.Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	registerQueueWorker(t, store, "cpu", 1, []string{"linux"}, []string{"docker"})
	registerQueueWorker(t, store, "gpu", 1, []string{"gpu", "linux"}, []string{"docker", "git"})
	if _, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "cpu"}); !errors.Is(err, queue.ErrNoJob) {
		t.Fatalf("cpu claim err=%v", err)
	}
	if err := store.SetWorkerDrain(ctx, "gpu", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "gpu"}); !errors.Is(err, queue.ErrWorkerDraining) {
		t.Fatalf("draining claim err=%v", err)
	}
	if err := store.SetWorkerDrain(ctx, "gpu", false); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "gpu", LeaseDuration: time.Minute})
	if err != nil || claim.Job.ID != job.ID {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
}

func TestQueueReclaimsAbandonedLeaseAndFencesStaleWorker(t *testing.T) {
	ctx := context.Background()
	client, _ := queueTestClient(t)
	defer client.Close()
	store := client.Queue()
	job := testJob("reclaim")
	job.AffinityKey = "workspace:task"
	if _, err := store.Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	registerQueueWorker(t, store, "worker-a", 1, nil, nil)
	registerQueueWorker(t, store, "worker-b", 1, nil, nil)
	first, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker-a", LeaseDuration: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	reclaimed, err := store.ReclaimExpired(ctx, first.Attempt.LeaseExpiresAt.Add(time.Millisecond))
	if err != nil || reclaimed != 1 {
		t.Fatalf("reclaimed=%d err=%v", reclaimed, err)
	}
	if _, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker-b"}); !errors.Is(err, queue.ErrNoJob) {
		t.Fatalf("affinity escaped to worker-b: %v", err)
	}
	second, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker-a", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if second.Attempt.Number != 2 {
		t.Fatalf("attempt=%d", second.Attempt.Number)
	}
	if err := store.Finish(ctx, first.Job.ID, first.Attempt.ID, first.Attempt.ClaimToken, queue.FinishResult{Success: true}); !errors.Is(err, queue.ErrStaleClaim) {
		t.Fatalf("stale finish err=%v", err)
	}
	if err := store.Finish(ctx, second.Job.ID, second.Attempt.ID, second.Attempt.ClaimToken, queue.FinishResult{Success: true}); err != nil {
		t.Fatal(err)
	}
	stored, _ := store.GetJob(ctx, job.ID)
	if stored.State != queue.JobSucceeded {
		t.Fatalf("state=%s", stored.State)
	}
}

func TestQueueConcurrencyPolicyAndCapacityAreTransactional(t *testing.T) {
	ctx := context.Background()
	client, _ := queueTestClient(t)
	defer client.Close()
	store := client.Queue()
	for _, key := range []string{"limit-1", "limit-2"} {
		if _, err := store.Enqueue(ctx, testJob(key)); err != nil {
			t.Fatal(err)
		}
	}
	registerQueueWorker(t, store, "worker", 2, nil, nil)
	policy := queue.ConcurrencyPolicy{Agents: map[string]int{"engineer": 1}}
	first, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker", Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker", Policy: policy}); !errors.Is(err, queue.ErrNoJob) {
		t.Fatalf("second claim err=%v", err)
	}
	if err := store.Finish(ctx, first.Job.ID, first.Attempt.ID, first.Attempt.ClaimToken, queue.FinishResult{Success: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker", Policy: policy}); err != nil {
		t.Fatal(err)
	}
}

func TestQueueCancellationPropagatesThroughHeartbeatAndWinsFinish(t *testing.T) {
	ctx := context.Background()
	client, _ := queueTestClient(t)
	defer client.Close()
	store := client.Queue()
	job := testJob("cancel")
	if _, err := store.Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	registerQueueWorker(t, store, "worker", 1, nil, nil)
	claim, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RequestCancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	heartbeat, err := store.Heartbeat(ctx, job.ID, claim.Attempt.ID, claim.Attempt.ClaimToken, time.Minute)
	if err != nil || !heartbeat.CancelRequested {
		t.Fatalf("heartbeat=%+v err=%v", heartbeat, err)
	}
	if err := store.Finish(ctx, job.ID, claim.Attempt.ID, claim.Attempt.ClaimToken, queue.FinishResult{Success: true}); err != nil {
		t.Fatal(err)
	}
	stored, _ := store.GetJob(ctx, job.ID)
	if stored.State != queue.JobCanceled {
		t.Fatalf("state=%s", stored.State)
	}
}

func TestQueueRetriesOnlyExplicitlyRetryableFailures(t *testing.T) {
	ctx := context.Background()
	client, _ := queueTestClient(t)
	defer client.Close()
	store := client.Queue()
	registerQueueWorker(t, store, "worker", 1, nil, nil)
	terminal := testJob("terminal-failure")
	if _, err := store.Enqueue(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(ctx, terminal.ID, claim.Attempt.ID, claim.Attempt.ClaimToken, queue.FinishResult{Error: "workflow failed"}); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetJob(ctx, terminal.ID)
	if got.State != queue.JobFailed {
		t.Fatalf("ordinary failure state=%s", got.State)
	}

	retryable := testJob("retryable-failure")
	if _, err := store.Enqueue(ctx, retryable); err != nil {
		t.Fatal(err)
	}
	claim, err = store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(ctx, retryable.ID, claim.Attempt.ID, claim.Attempt.ClaimToken, queue.FinishResult{Error: "transport lost", Retry: true}); err != nil {
		t.Fatal(err)
	}
	got, _ = store.GetJob(ctx, retryable.ID)
	if got.State != queue.JobQueued {
		t.Fatalf("retryable failure state=%s", got.State)
	}
}

func TestQueueConcurrentClaimHasOneActiveAttempt(t *testing.T) {
	ctx := context.Background()
	client, _ := queueTestClient(t)
	defer client.Close()
	store := client.Queue()
	job := testJob("concurrent")
	if _, err := store.Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	registerQueueWorker(t, store, "worker", 32, nil, nil)
	var won atomic.Int32
	var unexpected atomic.Value
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Claim(ctx, queue.ClaimRequest{WorkerID: "worker", LeaseDuration: time.Minute})
			if err == nil {
				won.Add(1)
				return
			}
			if !errors.Is(err, queue.ErrNoJob) {
				unexpected.Store(err)
			}
		}()
	}
	wg.Wait()
	if value := unexpected.Load(); value != nil {
		t.Fatalf("unexpected claim error: %v", value)
	}
	if won.Load() != 1 {
		t.Fatalf("successful claims=%d", won.Load())
	}
}
