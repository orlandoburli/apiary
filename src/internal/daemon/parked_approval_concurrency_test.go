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

// approvalAdapter is a poll-only source whose per-task comments the test drives. It
// implements source.TaskPoller so approval steps can re-evaluate it, and counts the
// PollTask calls per item so the test can assert an instance was (or wasn't)
// re-evaluated.
type approvalAdapter struct {
	mu       sync.Mutex
	comments map[string][]string // source item id → comment bodies
	polls    map[string]int      // source item id → number of PollTask calls
}

func newApprovalAdapter() *approvalAdapter {
	return &approvalAdapter{comments: map[string][]string{}, polls: map[string]int{}}
}

func (a *approvalAdapter) ID() string                                    { return "src" }
func (a *approvalAdapter) Connect(context.Context, map[string]any) error { return nil }
func (a *approvalAdapter) Poll(context.Context, time.Time) ([]model.SourceItem, error) {
	return nil, nil
}
func (a *approvalAdapter) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (a *approvalAdapter) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}
func (a *approvalAdapter) PollTask(_ context.Context, cellID string) (model.SourceItem, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.polls[cellID]++
	item := model.SourceItem{ID: cellID, SourceID: "src"}
	for _, body := range a.comments[cellID] {
		item.Comments = append(item.Comments, model.Comment{Body: body})
	}
	return item, nil
}

func (a *approvalAdapter) addComment(cellID, body string) {
	a.mu.Lock()
	a.comments[cellID] = append(a.comments[cellID], body)
	a.mu.Unlock()
}

func (a *approvalAdapter) pollCount(cellID string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.polls[cellID]
}

func approvalWorkflow() config.WorkflowConfig {
	return config.WorkflowConfig{
		ID: "appr-wf",
		Steps: []config.StepConfig{
			{ID: "plan", Agent: "eng"},
			{ID: "gate", Type: config.StepTypeApproval, DependsOn: []string{"plan"},
				ResumeOn: &config.ApprovalTrigger{CommentContains: "approve"}},
			{ID: "build", Agent: "eng", DependsOn: []string{"gate"}},
		},
	}
}

// TestCheckApprovals_SlowAdvanceDoesNotStarveRecheck is the regression for the
// parked-approval head-of-line blocking: a long-running follow-on agent step on one
// resumed instance must NOT delay the cheap re-evaluation of other parked approvals,
// and the re-evaluation must not be gated behind the (now saturated) per-agent
// semaphore.
//
// Two instances park at an approval gate. Instance A is approved and advances into a
// "build" agent step that blocks indefinitely (holding the only "eng" slot).
// Instance B stays un-approved. The test asserts checkApprovals returns promptly and
// that B keeps getting re-evaluated (its task re-polled) while A's advance is wedged.
//
// Against the old sequential checkApprovals (CheckParkedApprovals calling
// ResolveApproval inline on the poll goroutine), A's wedged advance blocks
// checkApprovals itself, so it never returns and B is never re-evaluated — both
// assertions below fail.
func TestCheckApprovals_SlowAdvanceDoesNotStarveRecheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "approvals.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	cfg := &config.Config{
		Version:   "1",
		Sources:   []config.SourceConfig{{ID: "src", Type: "fake"}},
		Agents:    []config.AgentConfig{{ID: "eng", Model: "test/model"}},
		Workflows: []config.WorkflowConfig{approvalWorkflow()},
	}

	adapter := newApprovalAdapter()
	// A's resumed advance blocks in build; B never reaches build.
	runner := &gateRunner{blockCell: "A", blockStep: "build",
		entered: make(chan struct{}), release: make(chan struct{})}

	d := &Dispatcher{
		cfg:         cfg,
		db:          dbc,
		sources:     map[string]source.Adapter{"src": adapter},
		runners:     map[string]runnerpkg.Runner{"agent-eng": runner},
		agentRunner: map[string]string{"eng": "claude"},
		// Capacity 1: a wedged advance saturates the agent, so a re-evaluation that is
		// (incorrectly) gated behind it would starve.
		agentSem: map[string]chan struct{}{"eng": make(chan struct{}, 1)},
	}
	d.binder = source.NewSourceBinder(dbc)

	// Bind two source items so each instance's cell id matches its poll key.
	taskA, err := d.binder.Bind(ctx, model.SourceItem{ID: "A", SourceID: "src", Title: "A"})
	if err != nil {
		t.Fatalf("bind A: %v", err)
	}
	taskB, err := d.binder.Bind(ctx, model.SourceItem{ID: "B", SourceID: "src", Title: "B"})
	if err != nil {
		t.Fatalf("bind B: %v", err)
	}

	// Each workflow runs plan then parks at the approval gate.
	eng := d.workflowEngine()
	instA, _, _ := eng.RunInstance(ctx, approvalWorkflow(), taskA)
	instB, _, _ := eng.RunInstance(ctx, approvalWorkflow(), taskB)

	for _, id := range []string{instA, instB} {
		inst, _ := dbc.GetWorkflowInstance(ctx, id)
		if inst == nil || inst.State != db.InstanceStateBlocked {
			t.Fatalf("instance %s not parked at approval (state=%v)", id, inst)
		}
	}

	// A is approved; B stays un-approved.
	adapter.addComment("A", "please approve this")

	// checkApprovals must NOT block the poll loop on A's wedged advance.
	done := make(chan struct{})
	go func() { d.checkApprovals(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("checkApprovals blocked on a slow advance — poll loop would stall (head-of-line blocking)")
	}

	// A resumes and wedges in build, holding the only eng slot.
	select {
	case <-runner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("instance A never advanced into its blocking build step")
	}

	// Drive successive poll cycles while A stays wedged. B must keep getting
	// re-evaluated (its task re-polled) on each cycle even though the eng semaphore is
	// fully held by A's advance — the cheap re-evaluation is ungated. Before the fix
	// these re-checks sat behind A on the single poll goroutine and never ran. A
	// itself, being mid-advance, must NOT be re-checked (the approvalAdvancing guard
	// skips it).
	aWedged := adapter.pollCount("A")
	bStart := adapter.pollCount("B")
	cycles := 0
	waitUntil(t, 3*time.Second, func() bool {
		d.checkApprovals(ctx) // simulate one poll cycle
		cycles++
		return adapter.pollCount("B") >= bStart+3
	}, "instance B was not repeatedly re-evaluated while a slow advance held the agent slot")

	if got := adapter.pollCount("A"); got != aWedged {
		t.Errorf("wedged instance A was re-evaluated (polls %d→%d) over %d cycles; it should be skipped via approvalAdvancing",
			aWedged, got, cycles)
	}
	if inst, _ := dbc.GetWorkflowInstance(ctx, instB); inst == nil || inst.State != db.InstanceStateBlocked {
		t.Errorf("instance B should still be parked (un-approved), got %v", inst)
	}

	// Release A; it now completes and its build runs.
	close(runner.release)
	waitUntil(t, 3*time.Second, func() bool {
		inst, _ := dbc.GetWorkflowInstance(ctx, instA)
		return inst != nil && inst.State == db.InstanceStateDone
	}, "instance A did not complete after its build was released")
}
