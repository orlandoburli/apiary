package worker_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/queue"
	"github.com/orlandoburli/apiary/internal/worker"
)

func workerTestStore(t *testing.T) (*db.Client, *db.QueueStore) {
	t.Helper()
	client, err := db.New(context.Background(), filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, client.Queue()
}

func enqueueWorkerJob(t *testing.T, store *db.QueueStore, key string) *queue.Job {
	t.Helper()
	job := &queue.Job{IdempotencyKey: key, PayloadVersion: 1, Payload: []byte(`{"work":true}`)}
	if _, err := store.Enqueue(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	return job
}

func TestRuntimeGracefulDrainWaitsForActiveJob(t *testing.T) {
	_, store := workerTestStore(t)
	job := enqueueWorkerJob(t, store, "drain")
	started, release := make(chan struct{}), make(chan struct{})
	runtime, err := worker.New(store, worker.ExecutorFunc(func(context.Context, queue.Job) queue.FinishResult {
		close(started)
		<-release
		return queue.FinishResult{Success: true}
	}), worker.Config{Worker: queue.Worker{ID: "local", Capacity: 1}, PollInterval: 5 * time.Millisecond, HeartbeatInterval: 10 * time.Millisecond, LeaseDuration: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("job did not start")
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("worker returned before active job drained: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	workers, err := store.ListWorkers(context.Background())
	if err != nil || len(workers) != 1 || !workers[0].Draining {
		t.Fatalf("workers=%+v err=%v", workers, err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not finish drain")
	}
	stored, _ := store.GetJob(context.Background(), job.ID)
	if stored.State != queue.JobSucceeded {
		t.Fatalf("job state=%s", stored.State)
	}
	workers, _ = store.ListWorkers(context.Background())
	if workers[0].Ready || !workers[0].Draining || workers[0].ActiveJobs != 0 {
		t.Fatalf("worker after drain=%+v", workers[0])
	}
}

func TestRuntimePropagatesQueueCancellation(t *testing.T) {
	_, store := workerTestStore(t)
	job := enqueueWorkerJob(t, store, "cancel")
	started, canceled := make(chan struct{}), make(chan struct{})
	runtime, err := worker.New(store, worker.ExecutorFunc(func(ctx context.Context, _ queue.Job) queue.FinishResult {
		close(started)
		<-ctx.Done()
		close(canceled)
		return queue.FinishResult{Error: ctx.Err().Error()}
	}), worker.Config{Worker: queue.Worker{ID: "local", Capacity: 1}, PollInterval: 5 * time.Millisecond, HeartbeatInterval: 5 * time.Millisecond, LeaseDuration: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("job did not start")
	}
	if err := store.RequestCancel(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach executor")
	}
	deadline := time.Now().Add(time.Second)
	for {
		stored, _ := store.GetJob(context.Background(), job.ID)
		if stored.State == queue.JobCanceled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job state=%s", stored.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}
