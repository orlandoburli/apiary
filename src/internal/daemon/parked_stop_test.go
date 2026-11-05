package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	runnerpkg "github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/source"
	"github.com/orlandoburli/apiary/internal/state"
)

// blockingCIAdapter is a ciAdapter whose next poll can be held open, to put a stop
// in the middle of a CI re-check.
type blockingCIAdapter struct {
	*ciAdapter
	mu      sync.Mutex
	entered chan struct{} // non-nil while armed; closed when the held poll starts
	release chan struct{}
}

func (a *blockingCIAdapter) holdNextPoll() (entered, release chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entered, a.release = make(chan struct{}), make(chan struct{})
	return a.entered, a.release
}

func (a *blockingCIAdapter) PollCIStatus(ctx context.Context, cellID string) (source.CIStatus, error) {
	a.mu.Lock()
	entered, release := a.entered, a.release
	a.entered = nil
	a.mu.Unlock()
	if entered != nil {
		close(entered)
		<-release
	}
	return a.ciAdapter.PollCIStatus(ctx, cellID)
}

// stepCountRunner records how many times each step ran.
type stepCountRunner struct {
	mu   sync.Mutex
	runs map[string]int
}

func (r *stepCountRunner) ID() string                     { return "counting" }
func (r *stepCountRunner) Configure(map[string]any) error { return nil }
func (r *stepCountRunner) Run(_ context.Context, rr model.RunRequest) (model.RunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[rr.StepID]++
	return model.RunResult{Success: true}, nil
}

func (r *stepCountRunner) count(step string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[step]
}

type parkedStopFixture struct {
	d       *Dispatcher
	dbc     *db.Client
	adapter *blockingCIAdapter
	runner  *stepCountRunner
	task    model.InternalTask
}

func newParkedStopFixture(t *testing.T, ctx context.Context, wf config.WorkflowConfig) *parkedStopFixture {
	t.Helper()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "stop.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	f := &parkedStopFixture{
		dbc:     dbc,
		adapter: &blockingCIAdapter{ciAdapter: newCIAdapter()},
		runner:  &stepCountRunner{runs: map[string]int{}},
	}
	f.d = f.dispatcher(wf)
	f.task, err = f.d.binder.Bind(ctx, model.SourceItem{ID: "A", SourceID: "src", Title: "A"})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if _, err := dbc.InternalTasks().IncrementOutstanding(ctx, f.task.ID, 1); err != nil {
		t.Fatalf("increment outstanding: %v", err)
	}
	f.adapter.set("A", "pending")
	return f
}

// dispatcher builds a Dispatcher over the fixture's database. Calling it again
// models a daemon restart: the new engine starts with an empty parked set.
func (f *parkedStopFixture) dispatcher(wf config.WorkflowConfig) *Dispatcher {
	d := &Dispatcher{
		cfg: &config.Config{
			Version:   "1",
			Sources:   []config.SourceConfig{{ID: "src", Type: "fake"}},
			Agents:    []config.AgentConfig{{ID: "eng", Model: "test/model"}},
			Workflows: []config.WorkflowConfig{wf},
		},
		db:          f.dbc,
		sources:     map[string]source.Adapter{"src": f.adapter},
		runners:     map[string]runnerpkg.Runner{"agent-eng": f.runner},
		agentRunner: map[string]string{"eng": "claude"},
		agentSem:    map[string]chan struct{}{"eng": make(chan struct{}, 1)},
	}
	d.binder = source.NewSourceBinder(f.dbc)
	return d
}

// parkAtCI runs the workflow until it parks at check-ci and returns the instance id.
func (f *parkedStopFixture) parkAtCI(t *testing.T, ctx context.Context, wf config.WorkflowConfig) string {
	t.Helper()
	id, _, _ := f.d.workflowEngine().RunInstance(ctx, wf, f.task)
	if !f.d.engine.IsParked(id) {
		t.Fatalf("instance %s did not park at wait_for", id)
	}
	return id
}

// assertStopped pins everything a stop must leave behind: interrupted in the store,
// out of the parked set, no re-check bookkeeping, the follow-on step never run, and
// exactly one outstanding slot released.
func (f *parkedStopFixture) assertStopped(t *testing.T, ctx context.Context, id string) {
	t.Helper()
	inst, err := f.dbc.GetWorkflowInstance(ctx, id)
	if err != nil || inst == nil {
		t.Fatalf("get instance: %v (%v)", inst, err)
	}
	if !db.InterruptedInstance(inst.State, inst.BlockedReason) {
		t.Errorf("instance state = %s/%s, want blocked/interrupted", inst.State, inst.BlockedReason)
	}
	if f.d.engine.IsParked(id) {
		t.Error("stopped instance is still in the engine's parked set")
	}
	if _, ok := f.d.waitLastCheck.Load(id); ok {
		t.Error("waitLastCheck still holds the stopped instance")
	}
	if _, ok := f.d.waitAdvancing.Load(id); ok {
		t.Error("waitAdvancing still holds the stopped instance")
	}
	if got := f.runner.count("review"); got != 0 {
		t.Errorf("review ran %d time(s) on a stopped instance", got)
	}
	task, err := f.dbc.InternalTasks().GetTask(ctx, f.task.ID)
	if err != nil || task == nil {
		t.Fatalf("get task: %v (%v)", task, err)
	}
	if task.OutstandingWorkflows != 0 {
		t.Errorf("outstanding workflows = %d, want 0", task.OutstandingWorkflows)
	}
}

func (f *parkedStopFixture) waitBackground(t *testing.T) {
	t.Helper()
	if !f.d.WaitBackground(5 * time.Second) {
		t.Fatal("background re-checks did not finish")
	}
}

// drivePollCycles runs checkWaits repeatedly with CI green, giving a stale parked
// wait every chance to advance.
func (f *parkedStopFixture) drivePollCycles(t *testing.T, ctx context.Context) {
	t.Helper()
	f.adapter.set("A", "passed")
	for i := 0; i < 5; i++ {
		f.d.checkWaits(ctx)
		f.waitBackground(t)
	}
}

// TestStopInstance_ParkedWaitIsNotAdvanced is the #517 regression: a stopped
// instance stayed in the parked set, the next re-check saw CI green, and the
// workflow carried on, restarting an agent on a closed issue.
func TestStopInstance_ParkedWaitIsNotAdvanced(t *testing.T) {
	ctx := context.Background()
	wf := ciWorkflow()
	f := newParkedStopFixture(t, ctx, wf)
	id := f.parkAtCI(t, ctx, wf)

	// A first cycle leaves the bookkeeping a live parked wait really has.
	f.d.checkWaits(ctx)
	f.waitBackground(t)

	if err := f.d.StopInstance(ctx, id); err != nil {
		t.Fatalf("stop: %v", err)
	}
	f.drivePollCycles(t, ctx)
	f.assertStopped(t, ctx, id)
}

// TestCancelInstance_ParkedWaitIsNotAdvanced drives the same case the way
// `apiary instances <id> --cancel` does, through the IPC endpoint.
func TestCancelInstance_ParkedWaitIsNotAdvanced(t *testing.T) {
	ctx := context.Background()
	wf := ciWorkflow()
	f := newParkedStopFixture(t, ctx, wf)
	id := f.parkAtCI(t, ctx, wf)

	rec := httptest.NewRecorder()
	f.d.stopInstanceHandler(ctx)(rec, httptest.NewRequest(http.MethodPost, "/instances/stop/"+id, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("stop endpoint: %d %s", rec.Code, rec.Body)
	}

	f.drivePollCycles(t, ctx)
	f.assertStopped(t, ctx, id)
}

// TestStopInstance_DuringUngatedRecheck stops while RecheckWait is out at the forge:
// the claim is held, the instance still looks parked, and the poll then comes back
// green. The waiting advance must find nothing left to wake.
func TestStopInstance_DuringUngatedRecheck(t *testing.T) {
	ctx := context.Background()
	wf := ciWorkflow()
	f := newParkedStopFixture(t, ctx, wf)
	id := f.parkAtCI(t, ctx, wf)

	f.adapter.set("A", "passed")
	entered, release := f.adapter.holdNextPoll()
	f.d.checkWaits(ctx)
	<-entered

	if err := f.d.StopInstance(ctx, id); err != nil {
		t.Fatalf("stop: %v", err)
	}
	close(release)
	f.waitBackground(t)

	f.drivePollCycles(t, ctx)
	f.assertStopped(t, ctx, id)
}

// TestStopInstance_DuringWake stops after WakeWait has already claimed the run, while
// its re-dispatched CI check is in flight. The run is out of the parked set by then,
// so only the flag can reach it: the green result must be discarded and must not
// overwrite the interrupted state.
func TestStopInstance_DuringWake(t *testing.T) {
	ctx := context.Background()
	wf := ciWorkflow()
	f := newParkedStopFixture(t, ctx, wf)
	id := f.parkAtCI(t, ctx, wf)

	f.adapter.set("A", "passed")
	entered, release := f.adapter.holdNextPoll()
	woken := make(chan struct{})
	go func() { defer close(woken); f.d.engine.WakeWait(ctx, id) }()
	<-entered

	if err := f.d.StopInstance(ctx, id); err != nil {
		t.Fatalf("stop: %v", err)
	}
	close(release)
	<-woken

	f.drivePollCycles(t, ctx)
	f.assertStopped(t, ctx, id)
}

// TestStopInstance_DuringWakeThatReparks covers the same window with CI still
// pending: settle must not re-park the stopped run or restore its waiting state.
func TestStopInstance_DuringWakeThatReparks(t *testing.T) {
	ctx := context.Background()
	wf := ciWorkflow()
	f := newParkedStopFixture(t, ctx, wf)
	id := f.parkAtCI(t, ctx, wf)

	entered, release := f.adapter.holdNextPoll()
	woken := make(chan struct{})
	go func() { defer close(woken); f.d.engine.WakeWait(ctx, id) }()
	<-entered

	if err := f.d.StopInstance(ctx, id); err != nil {
		t.Fatalf("stop: %v", err)
	}
	close(release)
	<-woken

	f.assertStopped(t, ctx, id)
}

// TestWakeWait_RefusesAnInstanceNoLongerParkedInTheStore pins the defensive read for
// a stop the engine never heard about: only the row says the instance was stopped,
// and the row wins.
func TestWakeWait_RefusesAnInstanceNoLongerParkedInTheStore(t *testing.T) {
	ctx := context.Background()
	wf := ciWorkflow()
	f := newParkedStopFixture(t, ctx, wf)
	id := f.parkAtCI(t, ctx, wf)

	if err := f.dbc.UpdateWorkflowInstanceState(ctx, id, db.InstanceStateBlocked, string(state.ReasonInterrupted)); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	f.adapter.set("A", "passed")
	f.d.engine.WakeWait(ctx, id)

	f.assertStopped(t, ctx, id)
}

// TestStopInstance_NotRehydratedAfterRestart pins that a stop survives a daemon
// restart: the rehydration pass must not bring the wait back.
func TestStopInstance_NotRehydratedAfterRestart(t *testing.T) {
	ctx := context.Background()
	wf := ciWorkflow()
	f := newParkedStopFixture(t, ctx, wf)
	id := f.parkAtCI(t, ctx, wf)

	if err := f.d.StopInstance(ctx, id); err != nil {
		t.Fatalf("stop: %v", err)
	}

	f.d = f.dispatcher(wf)
	f.d.rehydrateParkedWaits(ctx)
	f.d.rehydrateParkedApprovals(ctx)
	f.d.workflowEngine()
	f.drivePollCycles(t, ctx)
	f.assertStopped(t, ctx, id)
}

// TestStopInstance_LeavesSiblingParkedWaitAlone guards the blast radius: stopping one
// parked instance must not disturb another.
func TestStopInstance_LeavesSiblingParkedWaitAlone(t *testing.T) {
	ctx := context.Background()
	wf := ciWorkflow()
	f := newParkedStopFixture(t, ctx, wf)
	stopped := f.parkAtCI(t, ctx, wf)
	sibling := f.parkAtCI(t, ctx, wf)

	if err := f.d.StopInstance(ctx, stopped); err != nil {
		t.Fatalf("stop: %v", err)
	}
	f.drivePollCycles(t, ctx)

	inst, err := f.dbc.GetWorkflowInstance(ctx, sibling)
	if err != nil || inst == nil {
		t.Fatalf("get sibling: %v (%v)", inst, err)
	}
	if inst.State != db.InstanceStateDone {
		t.Errorf("sibling state = %s, want done", inst.State)
	}
	if got := f.runner.count("review"); got != 1 {
		t.Errorf("review ran %d time(s), want once for the sibling alone", got)
	}
}

// stoppableApprovalWorkflow parks at an approval gate, then reviews.
func stoppableApprovalWorkflow() config.WorkflowConfig {
	return config.WorkflowConfig{
		ID: "approval-wf",
		Steps: []config.StepConfig{
			{ID: "implement", Agent: "eng"},
			{ID: "gate", Type: config.StepTypeApproval, DependsOn: []string{"implement"}},
			{ID: "review", Agent: "eng", DependsOn: []string{"gate"}},
		},
	}
}

// TestStopInstance_ParkedApprovalIsNotAdvanced is the approval twin of #517: parked
// approvals share the parked set, so a response landing after the stop must not
// resume the instance.
func TestStopInstance_ParkedApprovalIsNotAdvanced(t *testing.T) {
	ctx := context.Background()
	wf := stoppableApprovalWorkflow()
	f := newParkedStopFixture(t, ctx, wf)
	id, _, _ := f.d.workflowEngine().RunInstance(ctx, wf, f.task)
	if !f.d.engine.IsParked(id) {
		t.Fatalf("instance %s did not park at approval", id)
	}
	f.d.approvalAdvancing.Store(id, struct{}{})

	if err := f.d.StopInstance(ctx, id); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, ok := f.d.approvalAdvancing.Load(id); ok {
		t.Error("approvalAdvancing still holds the stopped instance")
	}

	if _, err := f.d.engine.ResolveApprovalResponse(ctx, id, db.ApprovalResponse{Decision: "approve"}); err == nil {
		t.Error("a stopped instance accepted an approval response")
	}
	f.d.checkApprovals(ctx)
	f.waitBackground(t)
	f.assertStopped(t, ctx, id)

	// Nor does the approval come back after a restart.
	f.d = f.dispatcher(wf)
	f.d.rehydrateParkedApprovals(ctx)
	f.d.workflowEngine()
	f.assertStopped(t, ctx, id)
}

// TestForceRestart_DropsParkedWait pins that the other path interrupting instances
// shares the fix: `apiary restart` left the old wait parked beside the new dispatch.
func TestForceRestart_DropsParkedWait(t *testing.T) {
	ctx := context.Background()
	wf := ciWorkflow()
	f := newParkedStopFixture(t, ctx, wf)
	id := f.parkAtCI(t, ctx, wf)

	if _, err := f.d.ForceRestart(ctx, "A"); err != nil {
		t.Fatalf("force restart: %v", err)
	}
	if f.d.engine.IsParked(id) {
		t.Error("force-restarted instance is still parked")
	}
}
