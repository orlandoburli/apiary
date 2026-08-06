package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/router"
	runnerpkg "github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/source"
)

// approvingPoller is a poll-only source whose PollTask always reports an approving
// comment, so a parked approval's resume_on condition matches on the next check.
type approvingPoller struct{}

func (approvingPoller) ID() string                                    { return "src" }
func (approvingPoller) Connect(context.Context, map[string]any) error { return nil }
func (approvingPoller) Poll(context.Context, time.Time) ([]model.SourceItem, error) {
	return nil, nil
}
func (approvingPoller) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (approvingPoller) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}

func (approvingPoller) PollTask(_ context.Context, cellID string) (model.SourceItem, error) {
	return model.SourceItem{ID: cellID, SourceID: "src",
		Comments: []model.Comment{{Body: "approve please"}}}, nil
}

var (
	_ source.Adapter    = approvingPoller{}
	_ source.TaskPoller = approvingPoller{}
)

// TestRehydrateParkedApprovals_ResumesAndSettlesTask is the end-to-end regression
// test for the approval_waiting restart gap. It seeds a real DB exactly as a
// previous daemon would have left it — a workflow instance parked at an approval
// step, its task still carrying one outstanding workflow — then exercises the
// startup rehydration path. After rehydration the polling loop's approval check
// must resume the instance and settle the task, instead of leaving it stranded.
func TestRehydrateParkedApprovals_ResumesAndSettlesTask(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "rehydrate.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{ID: "src", Type: "fake"}},
		Agents: []config.AgentConfig{
			{ID: "architect", Model: "test/model"},
			{ID: "backend-dev", Model: "test/model"},
		},
		Settings: config.Settings{StateLock: false, ResultComment: false},
		Workflows: []config.WorkflowConfig{{
			ID:      "feature",
			Trigger: &config.TriggerConfig{Priority: 10, Match: config.RouteMatch{Source: "src"}},
			Steps: []config.StepConfig{
				{ID: "plan", Agent: "architect"},
				{ID: "gate", Type: config.StepTypeApproval, DependsOn: []string{"plan"},
					Message:  "Plan ready.",
					ResumeOn: &config.ApprovalTrigger{CommentContains: "approve"},
					Timeout:  "48h"},
				{ID: "implement", Agent: "backend-dev", DependsOn: []string{"gate"}},
			},
		}},
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("router: %v", err)
	}

	impl := &countingRunner{}
	d := &Dispatcher{
		cfg:     cfg,
		db:      dbc,
		router:  r,
		sources: map[string]source.Adapter{"src": approvingPoller{}},
		runners: map[string]runnerpkg.Runner{
			"agent-architect":   &countingRunner{},
			"agent-backend-dev": impl,
		},
		agentRunner: map[string]string{"architect": "claude", "backend-dev": "claude"},
		agentSem:    map[string]chan struct{}{},
		stats:       map[string]*sourceStat{"src": {}},
	}
	d.binder = source.NewSourceBinder(dbc)

	// --- seed the DB as if a previous daemon parked an instance at the approval. ---
	cell := model.SourceItem{ID: "ISSUE-1", SourceID: "src", Number: "#1", Title: "Add feature", State: "todo"}
	task, err := d.binder.Bind(ctx, cell)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if _, err := dbc.InternalTasks().IncrementOutstanding(ctx, task.ID, 1); err != nil {
		t.Fatalf("increment outstanding: %v", err)
	}
	instID := "wf-parked-1"
	if err := dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{
		ID: instID, WorkflowID: "feature", TaskID: task.ID, CellID: cell.ID, SourceID: "src",
		State: db.InstanceStateApprovalWaiting,
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	started := time.Now()
	if err := dbc.CreateStepRun(ctx, &db.StepRun{
		ID: "sr-plan", WorkflowInstanceID: instID, StepID: "plan", AgentID: "architect",
		State: db.StepStatePassed, Output: "planned", StartedAt: &started, FinishedAt: &started,
	}); err != nil {
		t.Fatalf("create step run: %v", err)
	}

	// --- the fix: a fresh daemon rehydrates the parked approval at startup. ---
	d.rehydrateParkedApprovals(ctx)
	if d.engine == nil {
		t.Fatal("rehydration should have built the engine")
	}
	parked := d.engine.ParkedApprovals()
	if len(parked) != 1 || parked[0].InstanceID != instID || parked[0].Step.ID != "gate" {
		t.Fatalf("expected one rehydrated parked approval at gate, got %+v", parked)
	}

	// --- the poll loop's approval check now sees the approving comment. The check
	// fans each parked approval out onto its own goroutine (so a slow resume never
	// blocks the poll loop), so the resume + settle happen asynchronously. settle
	// marks the instance done first and then transitions the task, so await the task
	// state — the last signal — which implies the whole resume completed.
	d.checkApprovals(ctx)

	waitUntil(t, 3*time.Second, func() bool {
		got, _ := dbc.InternalTasks().GetTask(ctx, task.ID)
		return got != nil && got.State == model.TaskStateDone
	}, "rehydrated approval was not resumed and settled (task left stuck in 'registered')")

	inst, err := dbc.GetWorkflowInstance(ctx, instID)
	if err != nil {
		t.Fatalf("get instance: %v", err)
	}
	if inst.State != db.InstanceStateDone {
		t.Errorf("instance state = %q, want done", inst.State)
	}
	if got := impl.n.Load(); got != 1 {
		t.Errorf("implement runner calls = %d, want 1", got)
	}
}
