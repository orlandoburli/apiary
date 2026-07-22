package queuehttp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/queue"
	"github.com/orlandoburli/apiary/internal/queuehttp"
)

func TestAuthenticatedWorkerLifecycle(t *testing.T) {
	ctx := context.Background()
	clientDB, err := db.New(ctx, filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientDB.Close() })
	store := clientDB.Queue()

	server := httptest.NewServer(queuehttp.Server{Store: store, Token: "test-secret", LeaseDuration: time.Minute, WorkerTimeout: time.Minute}.Handler())
	t.Cleanup(server.Close)
	remote := &queuehttp.Client{BaseURL: server.URL, Token: "test-secret", HTTPClient: server.Client()}

	worker := &queue.Worker{ID: "worker-1", ProtocolVersion: queue.WorkerProtocolVersion, Pool: "build", Labels: []string{"linux"}, Capabilities: []string{"runner:codex"}, Capacity: 2, Ready: true}
	if err := remote.RegisterWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}
	job := &queue.Job{IdempotencyKey: "task:1", Pool: "build", RequiredLabels: []string{"linux"}, RequiredCapabilities: []string{"runner:codex"}, PayloadVersion: 1, Payload: []byte(`{"task":"one"}`), MaxAttempts: 2}
	if created, err := store.Enqueue(ctx, job); err != nil || !created {
		t.Fatalf("enqueue created=%v err=%v", created, err)
	}

	claim, err := remote.Claim(ctx, queue.ClaimRequest{WorkerID: worker.ID})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Job.ID != job.ID || claim.Attempt.ClaimToken == "" {
		t.Fatalf("unexpected claim: %+v", claim)
	}
	heartbeat, err := remote.Heartbeat(ctx, job.ID, claim.Attempt.ID, claim.Attempt.ClaimToken, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if heartbeat.CancelRequested {
		t.Fatal("unexpected cancellation")
	}
	if err := remote.RequestCancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	heartbeat, err = remote.Heartbeat(ctx, job.ID, claim.Attempt.ID, claim.Attempt.ClaimToken, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !heartbeat.CancelRequested {
		t.Fatal("cancellation was not delivered")
	}
	if err := remote.Finish(ctx, job.ID, claim.Attempt.ID, claim.Attempt.ClaimToken, queue.FinishResult{Success: true}); err != nil {
		t.Fatal(err)
	}
	got, err := remote.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != queue.JobCanceled {
		t.Fatalf("state=%s, want canceled", got.State)
	}
}

func TestProtocolRejectsInvalidAuthenticationAndVersion(t *testing.T) {
	ctx := context.Background()
	clientDB, err := db.New(ctx, filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientDB.Close() })
	server := httptest.NewServer(queuehttp.Server{Store: clientDB.Queue(), Token: "correct", LeaseDuration: time.Minute, WorkerTimeout: time.Minute}.Handler())
	t.Cleanup(server.Close)

	bad := &queuehttp.Client{BaseURL: server.URL, Token: "wrong", HTTPClient: server.Client()}
	err = bad.RegisterWorker(ctx, &queue.Worker{ID: "worker", ProtocolVersion: queue.WorkerProtocolVersion, Capacity: 1})
	if err == nil {
		t.Fatal("expected authentication failure")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+queuehttp.Prefix+"/workers/register", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer correct")
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", res.StatusCode)
	}

	remote := &queuehttp.Client{BaseURL: server.URL, Token: "correct", HTTPClient: server.Client()}
	err = remote.RegisterWorker(ctx, &queue.Worker{ID: "worker", ProtocolVersion: queue.WorkerProtocolVersion + 1, Capacity: 1})
	if err == nil {
		t.Fatal("expected version rejection")
	}
}
