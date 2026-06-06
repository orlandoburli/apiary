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
