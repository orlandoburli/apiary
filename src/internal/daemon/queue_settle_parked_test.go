package daemon

import (
	"context"
	"path/filepath"
	"strings"
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

// newParkingWorkerDispatcher builds a queue-mode dispatcher with the real
// embedded worker over a workflow that parks at an approval gate after its first
// agent step. The queue job for such a run reaches `succeeded` while its workflow
// instance is still `approval_waiting`: the engine owns the instance and settles
// the task when the approval resolves, long after the job is terminal.
func newParkingWorkerDispatcher(t *testing.T, dbPath string, adapter *lockedAdapter) (*Dispatcher, *db.Client) {
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
		Workflows: []config.WorkflowConfig{{
			ID:      "triage",
			Trigger: &config.TriggerConfig{Match: config.RouteMatch{Labels: []string{"ai-ready"}}},
			Steps: []config.StepConfig{
				{ID: "plan", Agent: "a"},
				{ID: "gate", Type: config.StepTypeApproval, DependsOn: []string{"plan"},
					Message:  "Plan ready.",
					ResumeOn: &config.ApprovalTrigger{CommentContains: "approve"},
					Timeout:  "48h"},
				{ID: "run", Agent: "b", DependsOn: []string{"gate"}},
			},
		}},
	}
	cfg.Settings.Queue.ProjectID = "proj"
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

// waitForInstanceState polls until the item's task has an instance of workflowID
// in the given state (ignoring `queue-` placeholders), or the deadline passes.
func waitForInstanceState(t *testing.T, dbc *db.Client, itemID, workflowID, state string, timeout time.Duration) bool {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		binding, _ := dbc.SourceBindings().GetBindingBySourceItem(ctx, "src", itemID)
		if binding != nil {
			instances, _ := dbc.ListWorkflowInstancesByTask(ctx, binding.TaskID)
			for _, instance := range instances {
				if instance.WorkflowID == workflowID && instance.State == state && !strings.HasPrefix(instance.ID, "queue-") {
					return true
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// waitForTerminalJob polls until at least one dispatch job has reached a
// terminal state, or the deadline passes.
func waitForTerminalJob(t *testing.T, dbc *db.Client, timeout time.Duration) bool {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, state := range []queue.JobState{queue.JobSucceeded, queue.JobFailed, queue.JobCanceled} {
			if jobs, _ := dbc.Queue().ListJobs(ctx, state, 10); len(jobs) > 0 {
				return true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// Issue #375, the settle path. A queue job whose workflow parked at an approval
// reaches `succeeded` while the real instance is still `approval_waiting`. The
// startup reconcile must recognise that the local engine still owns that
// instance and leave it alone. It used to skip only when it found a *terminal*
// instance for the job's workflow, so a parked one fell through and the
// reconcile wrote a `queue-<jobid>` instance in state `done` for the very route
// that had not finished — and decremented the task's outstanding counter that
// the parked instance will decrement again when the approval resolves.
//
// The phantom `done` instance is what makes this a #375 defect rather than a
// bookkeeping wart: HasCompletedInstanceForRoute then reports the route as
// completed, so a `once: true` route and any redelivery of it are silently
// declined for a workflow that never actually finished.
func TestReconcileTerminalQueueJobs_LeavesParkedInstanceAlone(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "queue.db")
	adapter := &lockedAdapter{items: []model.SourceItem{{ID: "c1", SourceID: "src", Title: "Do it", Labels: []string{"ai-ready"}}}}

	d1, dbc1 := newParkingWorkerDispatcher(t, dbPath, adapter)
	stop1 := runWorker(t, d1)
	d1.poll(ctx, d1.cfg.Sources[0], adapter, time.Time{})
	if !waitForInstanceState(t, dbc1, "c1", "triage", db.InstanceStateApprovalWaiting, 15*time.Second) {
		t.Fatalf("workflow never parked at the approval gate:%s", queueDiagnostics(t, dbc1))
	}
	// The job that started the parked run goes terminal even though the run is
	// not: parking is reported as a non-success, so the job settles `failed`.
	if !waitForTerminalJob(t, dbc1, 10*time.Second) {
		t.Fatalf("expected the parked run's job to have gone terminal:%s", queueDiagnostics(t, dbc1))
	}
	binding, _ := dbc1.SourceBindings().GetBindingBySourceItem(ctx, "src", "c1")
	if binding == nil {
		t.Fatal("no source binding for c1")
	}
	taskID := binding.TaskID
	before, err := dbc1.InternalTasks().GetTask(ctx, taskID)
	if err != nil || before == nil {
		t.Fatalf("read task: %v", err)
	}
	stop1()

	// The daemon restarts. Startup reconcile walks every terminal queue job.
	d2, dbc2 := newParkingWorkerDispatcher(t, dbPath, adapter)
	d2.reconcileTerminalQueueJobs(ctx)

	instances, err := dbc2.ListWorkflowInstancesByTask(ctx, taskID)
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	parked := 0
	for _, instance := range instances {
		if strings.HasPrefix(instance.ID, "queue-") {
			t.Errorf("startup reconcile wrote placeholder instance %s (workflow %s, state %s) for a run still parked at an approval",
				instance.ID, instance.WorkflowID, instance.State)
		}
		if instance.State == db.InstanceStateApprovalWaiting {
			parked++
		}
	}
	if parked != 1 {
		t.Errorf("parked instance count = %d, want 1", parked)
	}
	completed, err := dbc2.HasCompletedInstanceForRoute(ctx, taskID, "triage")
	if err != nil {
		t.Fatalf("completed check: %v", err)
	}
	if completed {
		t.Error("route reported as completed while its only instance is still parked at an approval")
	}
	after, err := dbc2.InternalTasks().GetTask(ctx, taskID)
	if err != nil || after == nil {
		t.Fatalf("read task after reconcile: %v", err)
	}
	if after.OutstandingWorkflows != before.OutstandingWorkflows {
		t.Errorf("outstanding counter = %d after reconcile, want %d — the parked instance will decrement it again when the approval resolves",
			after.OutstandingWorkflows, before.OutstandingWorkflows)
	}
	if after.State != before.State {
		t.Errorf("task state = %q after reconcile, want %q", after.State, before.State)
	}
}
