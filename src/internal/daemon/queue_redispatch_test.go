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
	runnerpkg "github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/source"
)

// mutableAdapter is a poll-only source whose items can be mutated between polls,
// so a test can reproduce a label hand-off performed by an agent.
type mutableAdapter struct{ items []model.SourceItem }

func (a *mutableAdapter) ID() string                                    { return "src" }
func (a *mutableAdapter) Connect(context.Context, map[string]any) error { return nil }
func (a *mutableAdapter) Poll(context.Context, time.Time) ([]model.SourceItem, error) {
	return a.items, nil
}
func (a *mutableAdapter) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (a *mutableAdapter) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}

// newQueueDispatcher builds a dispatcher wired for queue-mode dispatch (durable
// jobs instead of inline goroutines) over the two label-chained workflows the
// README's hand-off pattern uses: `triage` (label ai-ready) hands off to `impl`
// (label ai-implement).
func newQueueDispatcher(t *testing.T, once bool) (*Dispatcher, *mutableAdapter, *db.Client) {
	t.Helper()
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{ID: "src", Type: "fake"}},
		Agents:  []config.AgentConfig{{ID: "a", Model: "test/model"}, {ID: "b", Model: "test/model"}},
		Workflows: []config.WorkflowConfig{
			{ID: "triage", Trigger: &config.TriggerConfig{Once: once, Match: config.RouteMatch{Labels: []string{"ai-ready"}}},
				Steps: []config.StepConfig{{ID: "run", Agent: "a"}}},
			{ID: "impl", Trigger: &config.TriggerConfig{Match: config.RouteMatch{Labels: []string{"ai-implement"}}},
				Steps: []config.StepConfig{{ID: "run", Agent: "b"}}},
		},
	}
	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	adapter := &mutableAdapter{items: []model.SourceItem{{ID: "c1", SourceID: "src", Title: "Do it", Labels: []string{"ai-ready"}}}}
	d := &Dispatcher{
		cfg:            cfg,
		db:             dbc,
		router:         r,
		sources:        map[string]source.Adapter{"src": adapter},
		runners:        map[string]runnerpkg.Runner{"agent-a": &countingRunner{}, "agent-b": &countingRunner{}},
		agentRunner:    map[string]string{"a": "claude-cli", "b": "claude-cli"},
		agentSem:       map[string]chan struct{}{},
		stats:          map[string]*sourceStat{"src": {}},
		dispatchQueue:  dbc.Queue(),
		queueProjectID: "proj",
	}
	d.binder = source.NewSourceBinder(dbc)
	return d, adapter, dbc
}

func queueJobFor(t *testing.T, dbc *db.Client, workflowID string) queue.Job {
	t.Helper()
	jobs, err := dbc.Queue().ListJobs(context.Background(), "", 100)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	for _, job := range jobs {
		if job.WorkflowID == workflowID {
			return job
		}
	}
	t.Fatalf("no queued job for workflow %q (jobs=%d)", workflowID, len(jobs))
	return queue.Job{}
}

func instancesFor(t *testing.T, dbc *db.Client, taskID, workflowID string) []db.WorkflowInstance {
	t.Helper()
	all, err := dbc.ListWorkflowInstancesByTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	var out []db.WorkflowInstance
	for _, instance := range all {
		if instance.WorkflowID == workflowID {
			out = append(out, instance)
		}
	}
	return out
}

// Issue #375: a label hand-off must dispatch on the next incremental poll, with
// no daemon restart. This walks the whole queue-mode cycle — poll, enqueue,
// execute, hand off, poll again — and guards the in-memory in-flight marker:
// the queue path releases it as soon as the job is enqueued, so a later poll
// must never skip the cell as "already in-flight".
func TestQueuePoll_LabelHandoffDispatchesOnNextPoll(t *testing.T) {
	ctx := context.Background()
	d, adapter, dbc := newQueueDispatcher(t, false)
	sc := d.cfg.Sources[0]

	d.poll(ctx, sc, adapter, time.Time{})
	triage := queueJobFor(t, dbc, "triage")

	if _, held := d.inFlight.Load("c1"); held {
		t.Fatal("in-flight marker still held after the queue path enqueued the cell's dispatch")
	}

	// A second poll while the job is still queued must not double-enqueue.
	d.poll(ctx, sc, adapter, time.Now())
	if jobs, _ := dbc.Queue().ListJobs(ctx, "", 100); len(jobs) != 1 {
		t.Fatalf("re-poll while queued created %d job(s), want 1", len(jobs))
	}

	// The worker executes the triage job: its instance completes and the task settles.
	if res := d.ExecuteQueuedJob(ctx, triage, "w1"); !res.Success {
		t.Fatalf("triage job failed: %+v", res)
	}
	task := taskForCell(t, dbc, "src", "c1")
	if task.State != model.TaskStateDone {
		t.Fatalf("task state = %q after triage, want done", task.State)
	}

	// The triage agent handed off by swapping the labels on the source item.
	adapter.items[0].Labels = []string{"ai-implement"}
	d.poll(ctx, sc, adapter, time.Now())

	impl := queueJobFor(t, dbc, "impl")
	if res := d.ExecuteQueuedJob(ctx, impl, "w1"); !res.Success {
		t.Fatalf("impl job failed: %+v", res)
	}
	if got := len(instancesFor(t, dbc, task.ID, "impl")); got != 1 {
		t.Fatalf("hand-off produced %d instance(s) of the impl workflow, want 1", got)
	}
}

// Issue #375: in queue mode a workflow that already completed for a task could
// never run again. ExecuteQueuedJob short-circuited on *any* completed instance
// for the route, so a legitimately re-dispatched job reported success without
// creating an instance — invisibly, since nothing was logged and no run started.
// A re-dispatch is the routing layer's decision (dropActiveMatches /
// dropOnceMatches / dropCappedMatches); a first delivery must honour it.
func TestExecuteQueuedJob_RunsRedispatchAfterEarlierCompletion(t *testing.T) {
	ctx := context.Background()
	d, adapter, dbc := newQueueDispatcher(t, false)
	sc := d.cfg.Sources[0]

	d.poll(ctx, sc, adapter, time.Time{})
	first := queueJobFor(t, dbc, "triage")
	if res := d.ExecuteQueuedJob(ctx, first, "w1"); !res.Success {
		t.Fatalf("first triage job failed: %+v", res)
	}
	task := taskForCell(t, dbc, "src", "c1")
	if got := len(instancesFor(t, dbc, task.ID, "triage")); got != 1 {
		t.Fatalf("first run produced %d instance(s), want 1", got)
	}

	// The item still carries ai-ready, so a later poll re-dispatches triage —
	// exactly what the non-queue path does for a trigger that keeps matching.
	d.poll(ctx, sc, adapter, time.Now())
	jobs, _ := dbc.Queue().ListJobs(ctx, "", 100)
	if len(jobs) != 2 {
		t.Fatalf("re-poll enqueued %d job(s) in total, want 2 (the re-dispatch was dropped)", len(jobs))
	}
	var redispatch queue.Job
	for _, job := range jobs {
		if job.WorkflowID == "triage" && job.ID != first.ID {
			redispatch = job
		}
	}
	if redispatch.ID == "" {
		t.Fatal("re-poll did not enqueue a second triage job")
	}
	redispatch.AttemptCount = 1 // first delivery of the new job

	if res := d.ExecuteQueuedJob(ctx, redispatch, "w1"); !res.Success {
		t.Fatalf("re-dispatched job failed: %+v", res)
	}
	if got := len(instancesFor(t, dbc, task.ID, "triage")); got != 2 {
		t.Fatalf("re-dispatched job produced %d instance(s) of triage, want 2 — the job reported success without running", got)
	}
}

// The redelivery guard must stay: a job re-claimed after its lease expired
// (attempt > 1) must not re-run a workflow that has since completed, or the
// agent's side effects are duplicated.
func TestExecuteQueuedJob_SkipsRedeliveryOfCompletedWorkflow(t *testing.T) {
	ctx := context.Background()
	d, adapter, dbc := newQueueDispatcher(t, false)
	sc := d.cfg.Sources[0]

	d.poll(ctx, sc, adapter, time.Time{})
	job := queueJobFor(t, dbc, "triage")
	if res := d.ExecuteQueuedJob(ctx, job, "w1"); !res.Success {
		t.Fatalf("first delivery failed: %+v", res)
	}
	task := taskForCell(t, dbc, "src", "c1")

	job.AttemptCount = 2 // lease expired, the job was re-claimed
	if res := d.ExecuteQueuedJob(ctx, job, "w1"); !res.Success {
		t.Fatalf("redelivery should settle as success: %+v", res)
	}
	if got := len(instancesFor(t, dbc, task.ID, "triage")); got != 1 {
		t.Fatalf("redelivery produced %d instance(s), want 1 (no duplicate run)", got)
	}
}

// A `once: true` trigger keeps its run-at-most-once guarantee on the queue path
// even for a first delivery.
func TestExecuteQueuedJob_SkipsOnceRouteAfterCompletion(t *testing.T) {
	ctx := context.Background()
	d, adapter, dbc := newQueueDispatcher(t, true)
	sc := d.cfg.Sources[0]

	d.poll(ctx, sc, adapter, time.Time{})
	job := queueJobFor(t, dbc, "triage")
	if res := d.ExecuteQueuedJob(ctx, job, "w1"); !res.Success {
		t.Fatalf("first delivery failed: %+v", res)
	}
	task := taskForCell(t, dbc, "src", "c1")

	job.AttemptCount = 1
	if res := d.ExecuteQueuedJob(ctx, job, "w1"); !res.Success {
		t.Fatalf("once-route replay should settle as success: %+v", res)
	}
	if got := len(instancesFor(t, dbc, task.ID, "triage")); got != 1 {
		t.Fatalf("once route ran %d time(s), want 1", got)
	}
}
