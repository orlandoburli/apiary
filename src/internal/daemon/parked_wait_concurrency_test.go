package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	runnerpkg "github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/source"
)

// ciAdapter is a poll-only source whose per-item CI status the test drives. It
// implements source.CIStatusPoller so wait_for steps can query it, and counts the
// queries per item so the test can assert an instance was (or wasn't) re-checked.
type ciAdapter struct {
	mu     sync.Mutex
	status map[string]string // source item id → "pending"|"passed"|"failed"
	polls  map[string]int    // source item id → number of CI queries
}

func newCIAdapter() *ciAdapter {
	return &ciAdapter{status: map[string]string{}, polls: map[string]int{}}
}

func (a *ciAdapter) ID() string                                                  { return "src" }
func (a *ciAdapter) Connect(context.Context, map[string]any) error               { return nil }
func (a *ciAdapter) Poll(context.Context, time.Time) ([]model.SourceItem, error) { return nil, nil }
func (a *ciAdapter) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (a *ciAdapter) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}
func (a *ciAdapter) PollCIStatus(_ context.Context, cellID string) (source.CIStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.polls[cellID]++
	return source.CIStatus{Status: a.status[cellID]}, nil
}

func (a *ciAdapter) set(cellID, status string) {
	a.mu.Lock()
	a.status[cellID] = status
	a.mu.Unlock()
}

func (a *ciAdapter) pollCount(cellID string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.polls[cellID]
}

// gateRunner blocks the run of one specific (cell, step) until released, signalling
// when it enters; every other run succeeds immediately. It stands in for an agent
// whose follow-on step (e.g. an engineer fix) takes a long time.
type gateRunner struct {
	blockCell string
	blockStep string
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (r *gateRunner) ID() string                     { return "gate" }
func (r *gateRunner) Configure(map[string]any) error { return nil }
func (r *gateRunner) Run(ctx context.Context, rr model.RunRequest) (model.RunResult, error) {
	if rr.Cell.ID == r.blockCell && rr.StepID == r.blockStep {
		r.once.Do(func() { close(r.entered) })
		select {
		case <-r.release:
		case <-ctx.Done():
			return model.RunResult{Success: false}, ctx.Err()
		}
	}
	return model.RunResult{Success: true}, nil
}

func ciWorkflow() config.WorkflowConfig {
	return config.WorkflowConfig{
		ID: "ci-wf",
		Steps: []config.StepConfig{
			{ID: "implement", Agent: "eng"},
			{ID: "check-ci", Type: config.StepTypeWaitFor, DependsOn: []string{"implement"},
				WaitFor: &config.WaitForConfig{Kind: "ci", MaxDuration: "2h"}},
			{ID: "review", Agent: "eng", DependsOn: []string{"check-ci"}},
		},
	}
}

func waitUntil(t *testing.T, within time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}

// TestCheckWaits_SlowAdvanceDoesNotStarveRecheck is the regression for the parked
// wait_for head-of-line blocking: a long-running follow-on agent step on one woken
// instance must NOT delay the cheap CI re-check of other parked instances, and the
// re-check must not be gated behind the (now saturated) per-agent semaphore.
//
// Two instances park on CI. Instance A's CI turns green and it advances into a
// "review" agent step that blocks indefinitely (holding the only "eng" slot).
// Instance B's CI stays pending. The test asserts checkWaits returns promptly and
// that B keeps getting re-checked while A's advance is wedged.
func TestCheckWaits_SlowAdvanceDoesNotStarveRecheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "waits.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	cfg := &config.Config{
		Version:   "1",
		Sources:   []config.SourceConfig{{ID: "src", Type: "fake"}},
		Agents:    []config.AgentConfig{{ID: "eng", Model: "test/model"}},
		Workflows: []config.WorkflowConfig{ciWorkflow()},
	}

	adapter := newCIAdapter()
	// A's slow advance blocks in review; B never reaches review.
	runner := &gateRunner{blockCell: "A", blockStep: "review",
		entered: make(chan struct{}), release: make(chan struct{})}

	d := &Dispatcher{
		cfg:         cfg,
		db:          dbc,
		sources:     map[string]source.Adapter{"src": adapter},
		runners:     map[string]runnerpkg.Runner{"agent-eng": runner},
		agentRunner: map[string]string{"eng": "claude"},
		// Capacity 1: a wedged advance saturates the agent, so a re-check that is
		// (incorrectly) gated behind it would starve.
		agentSem: map[string]chan struct{}{"eng": make(chan struct{}, 1)},
	}
	d.binder = source.NewSourceBinder(dbc)

	// Bind two source items so each instance's cell id matches its CI key.
	taskA, err := d.binder.Bind(ctx, model.SourceItem{ID: "A", SourceID: "src", Title: "A"})
	if err != nil {
		t.Fatalf("bind A: %v", err)
	}
	taskB, err := d.binder.Bind(ctx, model.SourceItem{ID: "B", SourceID: "src", Title: "B"})
	if err != nil {
		t.Fatalf("bind B: %v", err)
	}

	// Both CIs pending: each workflow runs implement then parks at check-ci.
	adapter.set("A", "pending")
	adapter.set("B", "pending")
	eng := d.workflowEngine()
	instA, _, _ := eng.RunInstance(ctx, ciWorkflow(), taskA)
	instB, _, _ := eng.RunInstance(ctx, ciWorkflow(), taskB)

	for _, id := range []string{instA, instB} {
		inst, _ := dbc.GetWorkflowInstance(ctx, id)
		if inst == nil || inst.State != db.InstanceStateWaiting {
			t.Fatalf("instance %s not parked at wait_for (state=%v)", id, inst)
		}
	}

	// A's CI turns green; B stays pending.
	adapter.set("A", "passed")

	// checkWaits must NOT block the poll loop on A's wedged advance.
	done := make(chan struct{})
	go func() { d.checkWaits(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("checkWaits blocked on a slow advance — poll loop would stall (head-of-line blocking)")
	}

	// A advances and wedges in review, holding the only eng slot.
	select {
	case <-runner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("instance A never advanced into its blocking review step")
	}

	// Drive successive poll cycles while A stays wedged. B must keep getting
	// re-checked (its CI polled) on each cycle even though the eng semaphore is fully
	// held by A's advance — the cheap re-check is ungated. Before the fix these
	// re-checks sat behind A on the single poll goroutine and never ran. A itself,
	// being mid-advance, must NOT be re-checked (the waitAdvancing guard skips it).
	aWedged := adapter.pollCount("A")
	bStart := adapter.pollCount("B")
	cycles := 0
	waitUntil(t, 3*time.Second, func() bool {
		d.checkWaits(ctx) // simulate one poll cycle
		cycles++
		return adapter.pollCount("B") >= bStart+3
	}, "instance B was not repeatedly re-checked while a slow advance held the agent slot")

	if got := adapter.pollCount("A"); got != aWedged {
		t.Errorf("wedged instance A was re-checked (polls %d→%d) over %d cycles; it should be skipped via waitAdvancing",
			aWedged, got, cycles)
	}
	if inst, _ := dbc.GetWorkflowInstance(ctx, instB); inst == nil || inst.State != db.InstanceStateWaiting {
		t.Errorf("instance B should still be parked (pending CI), got %v", inst)
	}

	// Release A; it now completes and its review runs.
	close(runner.release)
	waitUntil(t, 3*time.Second, func() bool {
		inst, _ := dbc.GetWorkflowInstance(ctx, instA)
		return inst != nil && inst.State == db.InstanceStateDone
	}, "instance A did not complete after its review was released")
}
