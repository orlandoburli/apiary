package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/queue"
	"github.com/orlandoburli/apiary/internal/router"
	_ "github.com/orlandoburli/apiary/internal/runner/providers"
)

// Regression for the v0.11.0 ordering bug: configureDispatchQueue ran before
// agents were loaded, so the embedded worker registered without any
// runner:<adapter> capability while every enqueued job required one — jobs
// stayed queued forever with a ready, idle worker.
func TestEmbeddedWorkerLeasesJobRequiringRunnerCapability(t *testing.T) {
	ctx := context.Background()
	client, err := db.New(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	cfg := &config.Config{
		Runners: []config.RunnerConfig{{ID: "claude", Type: "claude-cli"}},
		Agents:  []config.AgentConfig{{ID: "eng", Model: "claude-sonnet-5", Runner: "claude"}},
	}
	dispatcher, err := New(ctx, cfg, filepath.Join(t.TempDir(), "apiary.yaml"), client, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dispatcher.localWorker == nil {
		t.Fatal("expected embedded worker with default queue settings")
	}
	// Same requirements the enqueue path computes for a job routed to this agent.
	pool, labels, capabilities, _ := dispatcher.requirementsForMatch("task-1", router.Match{Route: config.RouteConfig{ID: "build", Agent: "eng"}})
	if !hasAllStrings(capabilities, []string{"runner:claude-cli"}) {
		t.Fatalf("expected job to require runner:claude-cli, got %v", capabilities)
	}
	job := &queue.Job{IdempotencyKey: "task-1:0:build", ProjectID: dispatcher.queueProjectID, Pool: pool, RequiredLabels: labels, RequiredCapabilities: capabilities, PayloadVersion: 1, Payload: []byte(`{}`), MaxAttempts: 1}
	if _, err := client.Queue().Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	// Register the exact worker spec the embedded worker runs with and lease.
	embedded := dispatcher.queueWorker
	if err := client.Queue().RegisterWorker(ctx, &embedded); err != nil {
		t.Fatal(err)
	}
	claim, err := client.Queue().Claim(ctx, queue.ClaimRequest{WorkerID: embedded.ID, LeaseDuration: time.Minute, WorkerTimeout: time.Minute})
	if err != nil {
		t.Fatalf("embedded worker (capabilities %v) failed to lease job requiring %v: %v", embedded.Capabilities, capabilities, err)
	}
	if claim.Job.ID != job.ID {
		t.Fatalf("leased job %s, want %s", claim.Job.ID, job.ID)
	}
}

// The workflow dimension must reach the policy the claim transaction evaluates;
// config validation alone does not prove queuePolicy wired it in.
func TestQueuePolicyIncludesWorkflowLimits(t *testing.T) {
	cfg := &config.Config{}
	cfg.Settings.Queue.Concurrency = config.QueueConcurrencySettings{
		DefaultWorkflow: 3,
		Workflows:       map[string]int{"sweep": 1},
	}
	policy := (&Dispatcher{cfg: cfg}).queuePolicy()
	if policy.DefaultWorkflow != 3 {
		t.Errorf("DefaultWorkflow = %d, want 3", policy.DefaultWorkflow)
	}
	if policy.Workflows["sweep"] != 1 {
		t.Errorf("Workflows[sweep] = %d, want 1", policy.Workflows["sweep"])
	}
	// The map is copied so the per-agent default loop cannot mutate the config.
	policy.Workflows["sweep"] = 9
	if cfg.Settings.Queue.Concurrency.Workflows["sweep"] != 1 {
		t.Error("queuePolicy must copy the workflows map, not alias the config")
	}
}

func TestSettleRemoteQueueJobIsDurableAndIdempotent(t *testing.T) {
	ctx := context.Background()
	client, err := db.New(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	task := &model.InternalTask{ID: "task-1", Title: "Remote work"}
	if err := client.InternalTasks().CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := client.InternalTasks().IncrementOutstanding(ctx, task.ID, 1); err != nil {
		t.Fatal(err)
	}
	job := &queue.Job{IdempotencyKey: "task-1:0:build", TaskID: task.ID, WorkflowID: "build", SourceID: "github", PayloadVersion: 1, Payload: []byte(`{}`), MaxAttempts: 1}
	if _, err := client.Queue().Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	job.State = queue.JobSucceeded
	dispatcher := &Dispatcher{db: client, dispatchQueue: client.Queue()}
	if err := dispatcher.settleRemoteQueueJob(ctx, *job); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.settleRemoteQueueJob(ctx, *job); err != nil {
		t.Fatal(err)
	}
	stored, err := client.InternalTasks().GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != model.TaskStateDone || stored.OutstandingWorkflows != 0 {
		t.Fatalf("task=%+v", stored)
	}
	instance, err := client.GetWorkflowInstance(ctx, "queue-"+job.ID)
	if err != nil || instance == nil || instance.State != db.InstanceStateDone {
		t.Fatalf("instance=%+v err=%v", instance, err)
	}
}

// TestSettleRemoteQueueJobSurvivesCancelledRunContext is the queue-side #7748
// regression: cancelling a leased job cancels the run context, and the
// settlement used to run on that same context — so `DecrementOutstanding` died
// with `context canceled`, the task kept its hold forever, and the terminal
// state was never mirrored. A cancelled job is already terminal; its
// settlement is teardown and must land.
func TestSettleRemoteQueueJobSurvivesCancelledRunContext(t *testing.T) {
	ctx := context.Background()
	client, err := db.New(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	task := &model.InternalTask{ID: "task-1", Title: "Cancelled work"}
	if err := client.InternalTasks().CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := client.InternalTasks().IncrementOutstanding(ctx, task.ID, 1); err != nil {
		t.Fatal(err)
	}
	job := &queue.Job{IdempotencyKey: "task-1:0:build", TaskID: task.ID, WorkflowID: "build", SourceID: "github", PayloadVersion: 1, Payload: []byte(`{}`), MaxAttempts: 1}
	if _, err := client.Queue().Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	job.State = queue.JobCanceled

	dispatcher := &Dispatcher{db: client, dispatchQueue: client.Queue()}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel() // the operator stop that ended the run
	if err := dispatcher.settleRemoteQueueJob(cancelled, *job); err != nil {
		t.Fatalf("settle a cancelled job: %v", err)
	}

	stored, err := client.InternalTasks().GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.OutstandingWorkflows != 0 {
		t.Fatalf("outstanding workflows = %d, want 0 — the cancelled settlement must release the hold", stored.OutstandingWorkflows)
	}
	instance, err := client.GetWorkflowInstance(ctx, "queue-"+job.ID)
	if err != nil || instance == nil || instance.State != db.InstanceStateCanceled {
		t.Fatalf("instance=%+v err=%v, want state %q", instance, err, db.InstanceStateCanceled)
	}
}
