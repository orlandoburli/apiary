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

// labelTrackingAdapter is a poll-only source that tracks AddLabels / RemoveLabels
// calls so tests can assert on them.
type labelTrackingAdapter struct {
	items   []model.SourceItem
	mu      sync.Mutex
	added   []string
	removed []string
}

func (a *labelTrackingAdapter) ID() string                                    { return "src" }
func (a *labelTrackingAdapter) Connect(context.Context, map[string]any) error { return nil }
func (a *labelTrackingAdapter) Poll(context.Context, time.Time) ([]model.SourceItem, error) {
	return a.items, nil
}
func (a *labelTrackingAdapter) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (a *labelTrackingAdapter) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}
func (a *labelTrackingAdapter) WebhookHandler() http.Handler { return nil }

func (a *labelTrackingAdapter) AddLabels(_ context.Context, _ model.SourceItem, names []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.added = append(a.added, names...)
	return nil
}

func (a *labelTrackingAdapter) RemoveLabels(_ context.Context, _ model.SourceItem, names []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.removed = append(a.removed, names...)
	return nil
}

func newTrustGateDispatcher(t *testing.T, items []model.SourceItem) (*Dispatcher, *countingRunner, *labelTrackingAdapter) {
	t.Helper()
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "trust.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{ID: "src", Type: "fake"}},
		Agents:  []config.AgentConfig{{ID: "a", Model: "test/model"}},
		Workflows: []config.WorkflowConfig{
			fanoutWorkflow("wf-a", "a", 10, false),
		},
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("router: %v", err)
	}

	adapter := &labelTrackingAdapter{items: items}
	runner := &countingRunner{}
	d := &Dispatcher{
		cfg:         cfg,
		db:          dbc,
		router:      r,
		sources:     map[string]source.Adapter{"src": adapter},
		runners:     map[string]runnerpkg.Runner{"agent-a": runner},
		agentRunner: map[string]string{"a": "claude"},
		agentSem:    map[string]chan struct{}{},
		stats:       map[string]*sourceStat{"src": {}},
	}
	d.binder = source.NewSourceBinder(dbc)
	return d, runner, adapter
}

func issueWithAssociation(assoc string) model.SourceItem {
	return model.SourceItem{
		ID:       "42",
		SourceID: "src",
		Title:    "Test issue",
		Metadata: map[string]any{
			"author_login":       "octocat",
			"author_association": assoc,
		},
	}
}

// TestTrustGate_TrustedAuthorsAreDispatched verifies that OWNER, MEMBER, and
// COLLABORATOR associations all proceed to dispatch without label changes.
func TestTrustGate_TrustedAuthorsAreDispatched(t *testing.T) {
	for _, assoc := range []string{"OWNER", "MEMBER", "COLLABORATOR"} {
		t.Run(assoc, func(t *testing.T) {
			d, runner, adapter := newTrustGateDispatcher(t, []model.SourceItem{
				issueWithAssociation(assoc),
			})

			if err := d.RunOnce(context.Background()); err != nil {
				t.Fatalf("RunOnce: %v", err)
			}

			if got := runner.n.Load(); got != 1 {
				t.Errorf("association %q: expected 1 dispatch, runner ran %d time(s)", assoc, got)
			}
			adapter.mu.Lock()
			defer adapter.mu.Unlock()
			if len(adapter.removed) > 0 || len(adapter.added) > 0 {
				t.Errorf("association %q: unexpected label changes removed=%v added=%v", assoc, adapter.removed, adapter.added)
			}
		})
	}
}

// TestTrustGate_UntrustedAuthorsAreParked verifies that non-collaborator
// associations are blocked: no workflow runs, ai-ready is removed, and
// needs-triage is added.
func TestTrustGate_UntrustedAuthorsAreParked(t *testing.T) {
	for _, assoc := range []string{"CONTRIBUTOR", "FIRST_TIMER", "FIRST_TIME_CONTRIBUTOR", "MANNEQUIN", "NONE", ""} {
		t.Run(assoc, func(t *testing.T) {
			d, runner, adapter := newTrustGateDispatcher(t, []model.SourceItem{
				issueWithAssociation(assoc),
			})

			if err := d.RunOnce(context.Background()); err != nil {
				t.Fatalf("RunOnce: %v", err)
			}

			if got := runner.n.Load(); got != 0 {
				t.Errorf("association %q: expected 0 dispatches (untrusted), runner ran %d time(s)", assoc, got)
			}
			adapter.mu.Lock()
			defer adapter.mu.Unlock()
			hasRemoveAIReady := false
			for _, l := range adapter.removed {
				if l == "ai-ready" {
					hasRemoveAIReady = true
				}
			}
			hasAddNeedsTriage := false
			for _, l := range adapter.added {
				if l == "needs-triage" {
					hasAddNeedsTriage = true
				}
			}
			if !hasRemoveAIReady {
				t.Errorf("association %q: expected ai-ready label to be removed, removed=%v", assoc, adapter.removed)
			}
			if !hasAddNeedsTriage {
				t.Errorf("association %q: expected needs-triage label to be added, added=%v", assoc, adapter.added)
			}
		})
	}
}

// TestTrustGate_MissingAssociationBypassesGate verifies that sources which do
// not populate author_association (e.g. Jira, Plane) are not blocked.
func TestTrustGate_MissingAssociationBypassesGate(t *testing.T) {
	item := model.SourceItem{
		ID:       "42",
		SourceID: "src",
		Title:    "No association field",
		// No Metadata at all — simulates a non-GitHub source.
	}
	d, runner, adapter := newTrustGateDispatcher(t, []model.SourceItem{item})

	if err := d.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if got := runner.n.Load(); got != 1 {
		t.Errorf("missing association: expected 1 dispatch (gate bypassed), runner ran %d time(s)", got)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.removed) > 0 || len(adapter.added) > 0 {
		t.Errorf("missing association: unexpected label changes removed=%v added=%v", adapter.removed, adapter.added)
	}
}

// TestIsTrustedAssociation unit-tests the helper in isolation.
func TestIsTrustedAssociation(t *testing.T) {
	trusted := []string{"OWNER", "MEMBER", "COLLABORATOR"}
	for _, a := range trusted {
		if !isTrustedAssociation(a) {
			t.Errorf("isTrustedAssociation(%q) = false, want true", a)
		}
	}
	untrusted := []string{"CONTRIBUTOR", "FIRST_TIMER", "FIRST_TIME_CONTRIBUTOR", "MANNEQUIN", "NONE", "", "unknown"}
	for _, a := range untrusted {
		if isTrustedAssociation(a) {
			t.Errorf("isTrustedAssociation(%q) = true, want false", a)
		}
	}
}
