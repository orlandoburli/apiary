package daemon

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/queue"
	"github.com/orlandoburli/apiary/internal/router"
	runnerpkg "github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/source"
)

// lockedAdapter is a poll-only source whose item labels can be swapped between
// polls (the label hand-off an agent performs) without racing the poll loop.
type lockedAdapter struct {
	mu    sync.Mutex
	items []model.SourceItem
}

func (a *lockedAdapter) ID() string                                    { return "src" }
func (a *lockedAdapter) Connect(context.Context, map[string]any) error { return nil }
func (a *lockedAdapter) Poll(context.Context, time.Time) ([]model.SourceItem, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]model.SourceItem, len(a.items))
	copy(out, a.items)
	return out, nil
}
func (a *lockedAdapter) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (a *lockedAdapter) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}
func (a *lockedAdapter) setLabels(labels ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.items[0].Labels = labels
}

// newWorkerDispatcher builds a dispatcher in queue mode with the *real* embedded
// queue worker (default capacity 1) attached to the given database, over the two
// label-chained workflows of the README hand-off pattern: `triage` (label
// ai-ready) hands off to `impl` (label ai-implement). Reusing the same dbPath
// across two calls models a daemon restart over a live database.
func newWorkerDispatcher(t *testing.T, dbPath string, adapter *lockedAdapter) (*Dispatcher, *db.Client) {
	t.Helper()
	ctx := context.Background()
	dbc, err := db.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{ID: "src", Type: "fake"}},
		Agents:  []config.AgentConfig{{ID: "a", Model: "test/model"}, {ID: "b", Model: "test/model"}},
		Workflows: []config.WorkflowConfig{
			{ID: "triage", Trigger: &config.TriggerConfig{Match: config.RouteMatch{Labels: []string{"ai-ready"}}},
				Steps: []config.StepConfig{{ID: "run", Agent: "a"}}},
			{ID: "impl", Trigger: &config.TriggerConfig{Match: config.RouteMatch{Labels: []string{"ai-implement"}}},
				Steps: []config.StepConfig{{ID: "run", Agent: "b"}}},
		},
	}
	cfg.Settings.Queue.ProjectID = "proj"
	// Fast timers so the test exercises the same code paths without waiting on
	// production-scale intervals.
	cfg.Settings.Queue.PollInterval = "20ms"
	cfg.Settings.Queue.LeaseDuration = "2s"
	cfg.Settings.Queue.HeartbeatInterval = "200ms"
	cfg.Settings.Queue.WorkerTimeout = "5s"

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	d := &Dispatcher{
		cfg:         cfg,
		db:          dbc,
		router:      r,
		sources:     map[string]source.Adapter{"src": adapter},
		runners:     map[string]runnerpkg.Runner{"agent-a": &countingRunner{}, "agent-b": &countingRunner{}},
		agentRunner: map[string]string{"a": "claude-cli", "b": "claude-cli"},
		agentSem:    map[string]chan struct{}{},
		stats:       map[string]*sourceStat{"src": {}},
		configFile:  filepath.Join(filepath.Dir(dbPath), "apiary.yaml"),
	}
	d.binder = source.NewSourceBinder(dbc)
	if err := d.configureDispatchQueue(); err != nil {
		t.Fatalf("configure dispatch queue: %v", err)
	}
	if d.localWorker == nil {
		t.Fatal("expected an embedded queue worker")
	}
	return d, dbc
}

// runWorker starts the embedded worker and returns a stop function.
func runWorker(t *testing.T, d *Dispatcher) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := d.localWorker.Run(ctx); err != nil {
			t.Errorf("embedded worker: %v", err)
		}
	}()
	stopped := false
	return func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("embedded worker did not stop")
		}
	}
}

// waitForInstance polls the DB until a real (non-placeholder) instance of
// workflowID for the item's task reaches `done`, or the deadline passes.
func waitForInstance(t *testing.T, dbc *db.Client, itemID, workflowID string, timeout time.Duration) bool {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		binding, _ := dbc.SourceBindings().GetBindingBySourceItem(ctx, "src", itemID)
		if binding != nil {
			instances, _ := dbc.ListWorkflowInstancesByTask(ctx, binding.TaskID)
			for _, instance := range instances {
				if instance.WorkflowID == workflowID && instance.State == db.InstanceStateDone && !strings.HasPrefix(instance.ID, "queue-") {
					return true
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// setStaleActiveJobs writes an orphaned lease count onto a worker registration,
// modelling a hard-killed worker process whose leases were never released.
func setStaleActiveJobs(t *testing.T, dbPath, workerID string, count int) {
	t.Helper()
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`UPDATE worker_registrations SET active_jobs=? WHERE id=?`, count, workerID); err != nil {
		t.Fatalf("set stale active_jobs: %v", err)
	}
}

func queueDiagnostics(t *testing.T, dbc *db.Client) string {
	t.Helper()
	ctx := context.Background()
	out := ""
	jobs, _ := dbc.Queue().ListJobs(ctx, "", 100)
	for _, job := range jobs {
		out += "\n  job " + job.ID + " workflow=" + job.WorkflowID + " state=" + string(job.State)
	}
	workers, _ := dbc.Queue().ListWorkers(ctx)
	for _, w := range workers {
		out += "\n  worker " + w.ID + " active_jobs=" + strconv.Itoa(w.ActiveJobs) + " capacity=" + strconv.Itoa(w.Capacity) + " ready=" + strconv.FormatBool(w.Ready) + " draining=" + strconv.FormatBool(w.Draining)
	}
	return out
}

// Issue #375, the reporter's actual label shape: the triage agent *adds* the
// hand-off label and leaves its own trigger label in place, so the incremental
// poll matches BOTH workflows. The fan-out enqueues two jobs onto a worker whose
// capacity is the production default of 1, and the first of them is a re-dispatch
// of a workflow that has already completed for this task — the exact combination
// that used to short-circuit inside ExecuteQueuedJob. The hand-off must still run.
func TestQueueWorker_HandoffWithBothLabelsRetained(t *testing.T) {
	ctx := context.Background()
	adapter := &lockedAdapter{items: []model.SourceItem{{ID: "c1", SourceID: "src", Title: "Do it", Labels: []string{"ai-ready"}}}}
	d, dbc := newWorkerDispatcher(t, filepath.Join(t.TempDir(), "queue.db"), adapter)
	stop := runWorker(t, d)
	defer stop()

	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})
	if !waitForInstance(t, dbc, "c1", "triage", 15*time.Second) {
		t.Fatalf("triage never completed:%s", queueDiagnostics(t, dbc))
	}

	adapter.setLabels("ai-ready", "ai-implement")
	d.poll(ctx, d.cfg.Sources[0], adapter, time.Now())
	if !waitForInstance(t, dbc, "c1", "impl", 15*time.Second) {
		t.Fatalf("hand-off never ran when the triage label stayed on the item:%s", queueDiagnostics(t, dbc))
	}
}

// Issue #375, first hand-off, end to end through the real embedded worker: the
// triage workflow runs, the agent swaps the labels, and the very next
// incremental poll must enqueue AND the worker must lease and run `impl` — with
// no daemon restart. Capacity is the production default of 1, so the hand-off
// job can only be leased if the earlier job released its slot.
func TestQueueWorker_FirstHandoffDispatchesWithoutRestart(t *testing.T) {
	adapter := &lockedAdapter{items: []model.SourceItem{{ID: "c1", SourceID: "src", Title: "Do it", Labels: []string{"ai-ready"}}}}
	d, dbc := newWorkerDispatcher(t, filepath.Join(t.TempDir(), "queue.db"), adapter)
	stop := runWorker(t, d)
	defer stop()

	ctx := context.Background()
	sc := d.cfg.Sources[0]
	d.poll(ctx, sc, adapter, time.Time{})
	if !waitForInstance(t, dbc, "c1", "triage", 15*time.Second) {
		t.Fatalf("triage never completed:%s", queueDiagnostics(t, dbc))
	}

	// The triage agent hands off by swapping the labels on the source item.
	adapter.setLabels("ai-implement")
	// The incremental poll that observes the update.
	d.poll(ctx, sc, adapter, time.Now())
	if !waitForInstance(t, dbc, "c1", "impl", 15*time.Second) {
		t.Fatalf("hand-off workflow never ran after the incremental poll that saw the label change:%s", queueDiagnostics(t, dbc))
	}
}

// Same hand-off, but the daemon is restarted (new Dispatcher + new embedded
// worker over the same database) between the triage run and the label swap, and
// the pre-restart worker is killed *without* draining — leaving a stale
// active_jobs count and a leased row behind. The hand-off must still dispatch on
// the first poll of the new process.
func TestQueueWorker_HandoffAfterRestartWithStaleWorkerRow(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "queue.db")
	adapter := &lockedAdapter{items: []model.SourceItem{{ID: "c1", SourceID: "src", Title: "Do it", Labels: []string{"ai-ready"}}}}

	d1, dbc1 := newWorkerDispatcher(t, dbPath, adapter)
	stop1 := runWorker(t, d1)
	d1.poll(ctx, d1.cfg.Sources[0], adapter, time.Time{})
	if !waitForInstance(t, dbc1, "c1", "triage", 15*time.Second) {
		t.Fatalf("triage never completed:%s", queueDiagnostics(t, dbc1))
	}
	stop1()

	_ = dbc1.Close()
	// Simulate a hard kill: the worker row keeps a stale lease count that already
	// saturates the default capacity of 1.
	setStaleActiveJobs(t, dbPath, d1.queueWorkerID, 1)

	adapter.setLabels("ai-implement")
	d2, dbc2 := newWorkerDispatcher(t, dbPath, adapter)
	stop2 := runWorker(t, d2)
	defer stop2()
	d2.poll(ctx, d2.cfg.Sources[0], adapter, time.Now())
	if !waitForInstance(t, dbc2, "c1", "impl", 15*time.Second) {
		t.Fatalf("hand-off never ran after restart with a stale worker row:%s", queueDiagnostics(t, dbc2))
	}
}

// A `queue-<jobid>` placeholder instance in state `done` — written by
// settleRemoteQueueJob for a job whose worker keeps its own instance state —
// must not block a later hand-off to a *different* workflow, and must not block
// a re-dispatch of the same workflow on a first delivery.
func TestQueueWorker_PlaceholderInstanceDoesNotBlockHandoff(t *testing.T) {
	ctx := context.Background()
	adapter := &lockedAdapter{items: []model.SourceItem{{ID: "c1", SourceID: "src", Title: "Do it", Labels: []string{"ai-ready"}}}}
	d, dbc := newWorkerDispatcher(t, filepath.Join(t.TempDir(), "queue.db"), adapter)
	stop := runWorker(t, d)
	defer stop()

	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})
	if !waitForInstance(t, dbc, "c1", "triage", 15*time.Second) {
		t.Fatalf("triage never completed:%s", queueDiagnostics(t, dbc))
	}
	binding, _ := dbc.SourceBindings().GetBindingBySourceItem(ctx, "src", "c1")
	if err := dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{ID: "queue-fake", WorkflowID: "impl", TaskID: binding.TaskID, SourceID: "src", State: db.InstanceStateDone}); err != nil {
		t.Fatalf("create placeholder: %v", err)
	}

	adapter.setLabels("ai-implement")
	d.poll(ctx, d.cfg.Sources[0], adapter, time.Now())
	if !waitForInstance(t, dbc, "c1", "impl", 15*time.Second) {
		t.Fatalf("hand-off never ran with a done placeholder instance present:%s", queueDiagnostics(t, dbc))
	}
	jobs, _ := dbc.Queue().ListJobs(ctx, queue.JobSucceeded, 100)
	if len(jobs) == 0 {
		t.Fatal("expected at least one succeeded job")
	}
}

// gatedRunner blocks every run until released, so a test can hold the embedded
// worker's single capacity slot while other work is enqueued.
type gatedRunner struct {
	gate chan struct{}
	runs chan struct{}
}

func newGatedRunner() *gatedRunner {
	return &gatedRunner{gate: make(chan struct{}), runs: make(chan struct{}, 16)}
}
func (r *gatedRunner) ID() string                     { return "gated" }
func (r *gatedRunner) Configure(map[string]any) error { return nil }
func (r *gatedRunner) Run(ctx context.Context, _ model.RunRequest) (model.RunResult, error) {
	select {
	case r.runs <- struct{}{}:
	default:
	}
	select {
	case <-r.gate:
	case <-ctx.Done():
		return model.RunResult{}, ctx.Err()
	}
	return model.RunResult{Success: true}, nil
}
func (r *gatedRunner) release() { close(r.gate) }

// Issue #375: the hand-off job is enqueued while the embedded worker's single
// capacity slot is still held by the earlier workflow. The queued job must be
// leased as soon as the slot frees — not left queued until a restart. This is
// the shape a slow agent produces on the production default (worker_capacity 1).
func TestQueueWorker_HandoffEnqueuedWhileWorkerBusyRunsWhenSlotFrees(t *testing.T) {
	ctx := context.Background()
	adapter := &lockedAdapter{items: []model.SourceItem{{ID: "c1", SourceID: "src", Title: "Do it", Labels: []string{"ai-ready"}}}}
	d, dbc := newWorkerDispatcher(t, filepath.Join(t.TempDir(), "queue.db"), adapter)
	// A lease long enough that the held job is not reclaimed while it blocks.
	d.cfg.Settings.Queue.LeaseDuration = "60s"
	if err := d.configureDispatchQueue(); err != nil {
		t.Fatalf("reconfigure queue: %v", err)
	}
	gated := newGatedRunner()
	d.runners["agent-a"] = gated
	stop := runWorker(t, d)
	defer stop()

	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})
	select {
	case <-gated.runs:
	case <-time.After(15 * time.Second):
		t.Fatalf("triage never started:%s", queueDiagnostics(t, dbc))
	}

	// The hand-off arrives while the worker is saturated: the incremental poll
	// observes it and must enqueue now so it runs as soon as the slot frees.
	adapter.setLabels("ai-ready", "ai-implement")
	d.poll(ctx, d.cfg.Sources[0], adapter, time.Now())
	queued, err := dbc.Queue().ListJobs(ctx, queue.JobQueued, 10)
	if err != nil {
		t.Fatalf("list queued: %v", err)
	}
	if len(queued) == 0 {
		t.Fatalf("hand-off was not enqueued while the worker was busy:%s", queueDiagnostics(t, dbc))
	}
	gated.release()

	if !waitForInstance(t, dbc, "c1", "impl", 20*time.Second) {
		t.Fatalf("hand-off job never ran after the worker's slot freed:%s", queueDiagnostics(t, dbc))
	}
}
