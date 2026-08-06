package daemon

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/router"
	runnerpkg "github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/source"
)

// fanoutAdapter is a poll-only source returning a fixed set of items.
type fanoutAdapter struct{ items []model.SourceItem }

func (a *fanoutAdapter) ID() string                                    { return "fake" }
func (a *fanoutAdapter) Connect(context.Context, map[string]any) error { return nil }
func (a *fanoutAdapter) Poll(context.Context, time.Time) ([]model.SourceItem, error) {
	return a.items, nil
}
func (a *fanoutAdapter) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (a *fanoutAdapter) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}

// countingRunner records how many times it ran; all steps succeed.
type countingRunner struct{ n atomic.Int32 }

func (r *countingRunner) ID() string                     { return "counting" }
func (r *countingRunner) Configure(map[string]any) error { return nil }
func (r *countingRunner) Run(context.Context, model.RunRequest) (model.RunResult, error) {
	r.n.Add(1)
	return model.RunResult{Success: true}, nil
}

func fanoutWorkflow(id, agent string, prio int, exclusive bool) config.WorkflowConfig {
	return config.WorkflowConfig{
		ID: id,
		Trigger: &config.TriggerConfig{
			Priority:  prio,
			Exclusive: exclusive,
			Match:     config.RouteMatch{Source: "src"},
		},
		Steps: []config.StepConfig{{ID: "run", Agent: agent}},
	}
}

func newFanoutDispatcher(t *testing.T, workflows []config.WorkflowConfig) (*Dispatcher, *countingRunner, *db.Client) {
	t.Helper()
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "fanout.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{ID: "src", Type: "fake"}},
		Agents: []config.AgentConfig{
			{ID: "a", Model: "test/model"},
			{ID: "b", Model: "test/model"},
		},
		Workflows: workflows,
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("router: %v", err)
	}

	runner := &countingRunner{}
	d := &Dispatcher{
		cfg:         cfg,
		db:          dbc,
		router:      r,
		sources:     map[string]source.Adapter{"src": &fanoutAdapter{items: []model.SourceItem{{ID: "c1", SourceID: "src", Title: "Do it"}}}},
		runners:     map[string]runnerpkg.Runner{"agent-a": runner, "agent-b": runner},
		agentRunner: map[string]string{"a": "claude", "b": "claude"},
		agentSem:    map[string]chan struct{}{},
		stats:       map[string]*sourceStat{"src": {}},
	}
	d.binder = source.NewSourceBinder(dbc)
	return d, runner, dbc
}

func taskForCell(t *testing.T, dbc *db.Client, sourceID, itemID string) *model.InternalTask {
	t.Helper()
	ctx := context.Background()
	binding, err := dbc.SourceBindings().GetBindingBySourceItem(ctx, sourceID, itemID)
	if err != nil {
		t.Fatalf("get binding: %v", err)
	}
	if binding == nil {
		t.Fatal("expected a binding for the polled item")
	}
	task, err := dbc.InternalTasks().GetTask(ctx, binding.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task == nil {
		t.Fatal("expected the bound task to exist")
	}
	return task
}

func TestRunOnce_FanOutTwoWorkflows(t *testing.T) {
	d, runner, dbc := newFanoutDispatcher(t, []config.WorkflowConfig{
		fanoutWorkflow("wf-a", "a", 10, false),
		fanoutWorkflow("wf-b", "b", 20, false),
	})

	if err := d.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if got := runner.n.Load(); got != 2 {
		t.Errorf("expected 2 workflow dispatches, runner ran %d time(s)", got)
	}
	// Both fanned-out workflows reached a terminal state, so the outstanding
	// counter (bumped to 2 at fan-out) has drained back to 0 and the task has
	// transitioned to done.
	task := taskForCell(t, dbc, "src", "c1")
	if task.OutstandingWorkflows != 0 {
		t.Errorf("outstanding_workflows = %d, want 0 after both completed", task.OutstandingWorkflows)
	}
	if task.State != model.TaskStateDone {
		t.Errorf("task state = %q, want done", task.State)
	}
}

func TestRunOnce_ExclusiveDispatchesOne(t *testing.T) {
	d, runner, dbc := newFanoutDispatcher(t, []config.WorkflowConfig{
		fanoutWorkflow("wf-a", "a", 10, true), // exclusive: claims the task alone
		fanoutWorkflow("wf-b", "b", 20, false),
	})

	if err := d.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if got := runner.n.Load(); got != 1 {
		t.Errorf("exclusive trigger should dispatch once, runner ran %d time(s)", got)
	}
	// The single (exclusive) workflow completed, so the counter drained from 1
	// back to 0 and the task is done.
	task := taskForCell(t, dbc, "src", "c1")
	if task.OutstandingWorkflows != 0 {
		t.Errorf("outstanding_workflows = %d, want 0 after completion", task.OutstandingWorkflows)
	}
	if task.State != model.TaskStateDone {
		t.Errorf("task state = %q, want done", task.State)
	}
}
