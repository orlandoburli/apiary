package daemon

import (
	"context"
	"net/http"
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

// TestIsTrustedAssociation verifies the GitHub collaborator trust set.
func TestIsTrustedAssociation(t *testing.T) {
	trusted := []string{"OWNER", "MEMBER", "COLLABORATOR"}
	for _, assoc := range trusted {
		if !isTrustedAssociation(assoc) {
			t.Errorf("isTrustedAssociation(%q) = false, want true", assoc)
		}
	}

	untrusted := []string{"CONTRIBUTOR", "FIRST_TIME_CONTRIBUTOR", "FIRST_TIMER", "NONE", ""}
	for _, assoc := range untrusted {
		if isTrustedAssociation(assoc) {
			t.Errorf("isTrustedAssociation(%q) = true, want false", assoc)
		}
	}
}

// trustAdapter is a source that implements LabelAdder and LabelRemover so the
// trust gate can inspect what label operations were requested.
type trustAdapter struct {
	items         []model.SourceItem
	addedLabels   []string
	removedLabels []string
}

func (a *trustAdapter) ID() string                                    { return "gh-trust" }
func (a *trustAdapter) Connect(context.Context, map[string]any) error { return nil }
func (a *trustAdapter) Poll(context.Context, time.Time) ([]model.SourceItem, error) {
	return a.items, nil
}
func (a *trustAdapter) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (a *trustAdapter) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}
func (a *trustAdapter) WebhookHandler() http.Handler { return nil }
func (a *trustAdapter) AddLabels(_ context.Context, _ model.SourceItem, labels []string) error {
	a.addedLabels = append(a.addedLabels, labels...)
	return nil
}
func (a *trustAdapter) RemoveLabels(_ context.Context, _ model.SourceItem, labels []string) error {
	a.removedLabels = append(a.removedLabels, labels...)
	return nil
}

func newTrustDispatcher(t *testing.T, triggerLabels []string) (*Dispatcher, *countingRunner, *db.Client) {
	t.Helper()
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "trust.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{
			ID:   "gh-trust",
			Type: "gh-trust",
			Filters: config.SourceFilters{
				Labels: triggerLabels,
			},
		}},
		Agents: []config.AgentConfig{
			{ID: "bot", Model: "test/model"},
		},
		Workflows: []config.WorkflowConfig{{
			ID: "wf-trust",
			Trigger: &config.TriggerConfig{
				Match: config.RouteMatch{
					Source: "gh-trust",
					Labels: triggerLabels,
				},
			},
			Steps: []config.StepConfig{{ID: "run", Agent: "bot"}},
		}},
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
		sources:     map[string]source.Adapter{},
		runners:     map[string]runnerpkg.Runner{"agent-bot": runner},
		agentRunner: map[string]string{"bot": "test"},
		agentSem:    map[string]chan struct{}{},
		stats:       map[string]*sourceStat{"gh-trust": {}},
	}
	d.binder = source.NewSourceBinder(dbc)
	return d, runner, dbc
}

// TestTrustGate_BlocksUntrustedAuthor verifies that poll() refuses to dispatch
// when author_association is not in the trusted set, and instead applies the
// "needs-triage" label and removes the trigger labels.
func TestTrustGate_BlocksUntrustedAuthor(t *testing.T) {
	d, runner, _ := newTrustDispatcher(t, []string{"ai-ready"})

	adapter := &trustAdapter{
		items: []model.SourceItem{
			{
				ID:       "99",
				SourceID: "gh-trust",
				Title:    "Untrusted issue",
				Labels:   []string{"ai-ready"},
				Metadata: map[string]any{
					"author_association": "NONE",
					"author_login":       "outsider",
				},
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			},
		},
	}
	d.sources["gh-trust"] = adapter

	sc := d.cfg.Sources[0]
	d.poll(context.Background(), sc, adapter, time.Time{})

	if got := runner.n.Load(); got != 0 {
		t.Errorf("dispatch count = %d, want 0: untrusted author must be blocked", got)
	}

	foundNeedsTriage := false
	for _, l := range adapter.addedLabels {
		if l == "needs-triage" {
			foundNeedsTriage = true
		}
	}
	if !foundNeedsTriage {
		t.Errorf("added labels = %v, want needs-triage to be added", adapter.addedLabels)
	}

	foundAiReady := false
	for _, l := range adapter.removedLabels {
		if l == "ai-ready" {
			foundAiReady = true
		}
	}
	if !foundAiReady {
		t.Errorf("removed labels = %v, want ai-ready to be stripped", adapter.removedLabels)
	}
}

// TestTrustGate_AllowsTrustedAuthor verifies that a MEMBER is dispatched normally.
func TestTrustGate_AllowsTrustedAuthor(t *testing.T) {
	d, runner, _ := newTrustDispatcher(t, []string{"ai-ready"})

	adapter := &trustAdapter{
		items: []model.SourceItem{
			{
				ID:       "77",
				SourceID: "gh-trust",
				Title:    "Trusted member issue",
				Labels:   []string{"ai-ready"},
				Metadata: map[string]any{
					"author_association": "MEMBER",
					"author_login":       "team-member",
				},
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			},
		},
	}
	d.sources["gh-trust"] = adapter

	sc := d.cfg.Sources[0]
	d.poll(context.Background(), sc, adapter, time.Time{})

	// Give dispatch goroutines time to run.
	time.Sleep(300 * time.Millisecond)

	if got := runner.n.Load(); got == 0 {
		t.Error("dispatch count = 0: trusted MEMBER must be dispatched")
	}
	if len(adapter.addedLabels) != 0 {
		t.Errorf("added labels = %v for trusted author, want none", adapter.addedLabels)
	}
}

// TestTrustGate_SourceWithoutAssociation verifies that a SourceItem with no
// author_association metadata (Jira/Plane) passes through unblocked.
func TestTrustGate_SourceWithoutAssociation(t *testing.T) {
	d, runner, _ := newTrustDispatcher(t, []string{"ai-ready"})

	adapter := &trustAdapter{
		items: []model.SourceItem{
			{
				ID:        "55",
				SourceID:  "gh-trust",
				Title:     "Plane task without association",
				Labels:    []string{"ai-ready"},
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
				// No Metadata — simulates a non-GitHub source.
			},
		},
	}
	d.sources["gh-trust"] = adapter

	sc := d.cfg.Sources[0]
	d.poll(context.Background(), sc, adapter, time.Time{})

	time.Sleep(300 * time.Millisecond)

	if got := runner.n.Load(); got == 0 {
		t.Error("dispatch count = 0: items without author_association must be dispatched")
	}
}
