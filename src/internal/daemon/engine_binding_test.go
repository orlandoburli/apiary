package daemon

import (
	"context"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/router"
	runnerpkg "github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/source"
)

// recordingAdapter is a poll-once source that captures every side-effect call so
// a test can assert the engine resolved the adapter (and the SourceItem) through
// the task's source binding. It implements LabelAdder and StateSetter.
type recordingAdapter struct {
	items []model.SourceItem

	mu      sync.Mutex
	acked   []model.SourceItem
	wrote   []model.SourceItem
	labeled [][]string
	states  []string
}

func (a *recordingAdapter) ID() string                                    { return "fake" }
func (a *recordingAdapter) Connect(context.Context, map[string]any) error { return nil }
func (a *recordingAdapter) Poll(context.Context, time.Time) ([]model.SourceItem, error) {
	return a.items, nil
}
func (a *recordingAdapter) Acknowledge(_ context.Context, item model.SourceItem, _ model.AckAction) error {
	a.mu.Lock()
	a.acked = append(a.acked, item)
	a.mu.Unlock()
	return nil
}
func (a *recordingAdapter) WriteResult(_ context.Context, item model.SourceItem, _ model.RunResult) error {
	a.mu.Lock()
	a.wrote = append(a.wrote, item)
	a.mu.Unlock()
	return nil
}
func (a *recordingAdapter) AddLabels(_ context.Context, item model.SourceItem, labels []string) error {
	a.mu.Lock()
	a.labeled = append(a.labeled, labels)
	a.mu.Unlock()
	return nil
}
func (a *recordingAdapter) SetState(_ context.Context, item model.SourceItem, state string) error {
	a.mu.Lock()
	a.states = append(a.states, state)
	a.mu.Unlock()
	return nil
}
func (a *recordingAdapter) WebhookHandler() http.Handler { return nil }

var (
	_ source.Adapter     = (*recordingAdapter)(nil)
	_ source.LabelAdder  = (*recordingAdapter)(nil)
	_ source.StateSetter = (*recordingAdapter)(nil)
)

// publishingRunner is a runner whose every step emits a fixed APIARY_PUBLISH
// payload, so a test can assert the engine writes it back to the source binding.
type publishingRunner struct{ payload string }

func (r *publishingRunner) ID() string                     { return "publishing" }
func (r *publishingRunner) Configure(map[string]any) error { return nil }
func (r *publishingRunner) Run(context.Context, model.RunRequest) (model.RunResult, error) {
	return model.RunResult{Success: true, PublishPayload: r.payload}, nil
}

// spawningRunner emits an APIARY_SPAWN request once (for the first call), then
// returns plain successes — so the parent step spawns a child while the child
// workflow's own step does not recurse.
type spawningRunner struct {
	mu  sync.Mutex
	req *model.SpawnRequest
}

func (r *spawningRunner) ID() string                     { return "spawning" }
func (r *spawningRunner) Configure(map[string]any) error { return nil }
func (r *spawningRunner) Run(context.Context, model.RunRequest) (model.RunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	req := r.req
	r.req = nil
	return model.RunResult{Success: true, SpawnRequest: req}, nil
}

// TestRunOnce_PublishWritesBackToBinding runs a workflow whose agent emits an
// APIARY_PUBLISH payload and asserts the engine writes it back to the task's
// source binding via the adapter (WriteResult), end-to-end (6.2.1, 6.4.1).
func TestRunOnce_PublishWritesBackToBinding(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "publish.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	adapter := &recordingAdapter{items: []model.SourceItem{
		{ID: "ISSUE-9", SourceID: "src", Number: "#9", Title: "Ship it", State: "todo"},
	}}

	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{ID: "src", Type: "fake"}},
		Agents:  []config.AgentConfig{{ID: "a", Model: "test/model"}},
		// result_comment OFF so the only WriteResult comes from the publish payload.
		Settings: config.Settings{StateLock: false, ResultComment: false},
		Workflows: []config.WorkflowConfig{{
			ID:      "wf-pub",
			Trigger: &config.TriggerConfig{Priority: 10, Match: config.RouteMatch{Source: "src"}},
			Steps:   []config.StepConfig{{ID: "run", Agent: "a"}},
		}},
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("router: %v", err)
	}

	d := &Dispatcher{
		cfg:         cfg,
		db:          dbc,
		router:      r,
		sources:     map[string]source.Adapter{"src": adapter},
		runners:     map[string]runnerpkg.Runner{"agent-a": &publishingRunner{payload: "## Result\nshipped"}},
		agentRunner: map[string]string{"a": "claude"},
		agentSem:    map[string]chan struct{}{},
		stats:       map[string]*sourceStat{"src": {}},
	}
	d.binder = source.NewSourceBinder(dbc)

	if err := d.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	adapter.mu.Lock()
	defer adapter.mu.Unlock()

	// The publish payload is written back to the bound source item exactly once.
	if len(adapter.wrote) != 1 {
		t.Fatalf("expected 1 publish WriteResult, got %d", len(adapter.wrote))
	}
	if adapter.wrote[0].ID != "ISSUE-9" || adapter.wrote[0].Number != "#9" {
		t.Errorf("publish item = %+v, want source item ISSUE-9/#9 from binding", adapter.wrote[0])
	}
}

// TestRunOnce_SideEffectsResolveViaBinding runs a single workflow against a
// source-bound task and asserts every side effect (state_lock, result comment,
// on_complete hook) reaches the adapter through the task's binding, carrying the
// binding's source item identity rather than a frozen SourceItem (Phase 5).
func TestRunOnce_SideEffectsResolveViaBinding(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "binding.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	adapter := &recordingAdapter{items: []model.SourceItem{
		{ID: "ISSUE-7", SourceID: "src", Number: "#7", Title: "Fix login", State: "todo"},
	}}

	cfg := &config.Config{
		Version:  "1",
		Sources:  []config.SourceConfig{{ID: "src", Type: "fake"}},
		Agents:   []config.AgentConfig{{ID: "a", Model: "test/model"}},
		Settings: config.Settings{StateLock: true, ResultComment: true},
		Workflows: []config.WorkflowConfig{{
			ID:         "wf-a",
			Trigger:    &config.TriggerConfig{Priority: 10, Match: config.RouteMatch{Source: "src"}},
			Steps:      []config.StepConfig{{ID: "run", Agent: "a"}},
			OnComplete: &config.OnComplete{SetState: "in_review", AddLabels: []string{"reviewed"}},
		}},
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("router: %v", err)
	}

	d := &Dispatcher{
		cfg:         cfg,
		db:          dbc,
		router:      r,
		sources:     map[string]source.Adapter{"src": adapter},
		runners:     map[string]runnerpkg.Runner{"agent-a": &countingRunner{}},
		agentRunner: map[string]string{"a": "claude"},
		agentSem:    map[string]chan struct{}{},
		stats:       map[string]*sourceStat{"src": {}},
	}
	d.binder = source.NewSourceBinder(dbc)

	if err := d.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	adapter.mu.Lock()
	defer adapter.mu.Unlock()

	// state_lock → Acknowledge against the bound source item.
	if len(adapter.acked) != 1 {
		t.Fatalf("expected 1 state_lock Acknowledge, got %d", len(adapter.acked))
	}
	if adapter.acked[0].ID != "ISSUE-7" || adapter.acked[0].SourceID != "src" {
		t.Errorf("state_lock item = %+v, want source item ISSUE-7 from binding", adapter.acked[0])
	}
	if adapter.acked[0].Number != "#7" {
		t.Errorf("state_lock item Number = %q, want #7 (from binding)", adapter.acked[0].Number)
	}

	// result_comment (on_complete) → WriteResult.
	if len(adapter.wrote) != 1 {
		t.Fatalf("expected 1 result-comment WriteResult, got %d", len(adapter.wrote))
	}
	if adapter.wrote[0].ID != "ISSUE-7" {
		t.Errorf("result-comment item id = %q, want ISSUE-7", adapter.wrote[0].ID)
	}

	// on_complete hook → AddLabels + SetState.
	if len(adapter.labeled) != 1 || len(adapter.labeled[0]) != 1 || adapter.labeled[0][0] != "reviewed" {
		t.Errorf("on_complete add_labels = %v, want [[reviewed]]", adapter.labeled)
	}
	if len(adapter.states) != 1 || adapter.states[0] != "in_review" {
		t.Errorf("on_complete set_state = %v, want [in_review]", adapter.states)
	}
}

// TestRunOnce_SpawnCreatesChildTask runs a parent workflow whose agent emits an
// APIARY_SPAWN request and asserts a child InternalTask is persisted with the
// parent's id, end-to-end through the dispatcher's wired spawner (7.3.1).
func TestRunOnce_SpawnCreatesChildTask(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "spawn.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	adapter := &recordingAdapter{items: []model.SourceItem{
		{ID: "INC-1", SourceID: "src", Number: "#1", Title: "Incident", State: "todo"},
	}}

	cfg := &config.Config{
		Version:  "1",
		Sources:  []config.SourceConfig{{ID: "src", Type: "fake"}},
		Agents:   []config.AgentConfig{{ID: "triage", Model: "test/model"}, {ID: "collector", Model: "test/model"}},
		Settings: config.Settings{StateLock: false, ResultComment: false},
		Workflows: []config.WorkflowConfig{
			{
				ID:      "wf-parent",
				Trigger: &config.TriggerConfig{Priority: 10, Match: config.RouteMatch{Source: "src"}},
				Steps:   []config.StepConfig{{ID: "triage", Agent: "triage", Spawn: config.SpawnAwait}},
			},
			{
				// Named child workflow; no trigger (it is dispatched by name, not routing).
				ID:    "collect-logs",
				Steps: []config.StepConfig{{ID: "collect", Agent: "collector"}},
			},
		},
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("router: %v", err)
	}

	d := &Dispatcher{
		cfg:     cfg,
		db:      dbc,
		router:  r,
		sources: map[string]source.Adapter{"src": adapter},
		runners: map[string]runnerpkg.Runner{
			"agent-triage":    &spawningRunner{req: &model.SpawnRequest{WorkflowID: "collect-logs", Title: "Collect logs", Input: map[string]any{"severity": "high"}}},
			"agent-collector": &countingRunner{},
		},
		agentRunner: map[string]string{"triage": "claude", "collector": "claude"},
		agentSem:    map[string]chan struct{}{},
		stats:       map[string]*sourceStat{"src": {}},
	}
	d.binder = source.NewSourceBinder(dbc)

	if err := d.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// The spawned child task must exist with the parent task as its parent and the
	// input carried through. The parent is the source-bound task for INC-1.
	tasks, err := dbc.InternalTasks().ListTasksByState(ctx, model.TaskStateRegistered)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	var child *model.InternalTask
	for i := range tasks {
		if tasks[i].ParentTaskID != "" {
			child = &tasks[i]
			break
		}
	}
	if child == nil {
		t.Fatalf("no spawned child task found among %d tasks", len(tasks))
	}
	if child.Title != "Collect logs" {
		t.Errorf("child Title = %q, want Collect logs", child.Title)
	}
	if child.Input["severity"] != "high" {
		t.Errorf("child Input = %#v, want severity=high", child.Input)
	}

	// The child's parent is the bound task for the polled source item.
	parent, err := dbc.InternalTasks().GetTask(ctx, child.ParentTaskID)
	if err != nil || parent == nil {
		t.Fatalf("parent task %q not found: %v", child.ParentTaskID, err)
	}
}
