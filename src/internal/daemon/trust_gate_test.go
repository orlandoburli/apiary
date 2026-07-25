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

// --- isTrustedAuthor unit tests ---

func TestIsTrustedAuthor(t *testing.T) {
	cases := []struct {
		assoc string
		want  bool
	}{
		{"OWNER", true},
		{"MEMBER", true},
		{"COLLABORATOR", true},
		{"owner", true},                  // case-insensitive
		{"member", true},                 // case-insensitive
		{"CONTRIBUTOR", false},
		{"FIRST_TIME_CONTRIBUTOR", false},
		{"FIRST_TIMER", false},
		{"NONE", false},
		{"MANNEQUIN", false},
		{"", true}, // empty = source doesn't carry association; pass through
	}

	for _, tc := range cases {
		item := model.SourceItem{AuthorAssociation: tc.assoc}
		if got := isTrustedAuthor(item); got != tc.want {
			t.Errorf("isTrustedAuthor(%q) = %v, want %v", tc.assoc, got, tc.want)
		}
	}
}

// --- stub adapter that records label mutations ---

type trustGateAdapter struct {
	mu            sync.Mutex
	items         []model.SourceItem
	removedLabels map[string][]string
	addedLabels   map[string][]string
}

func (a *trustGateAdapter) ID() string                                    { return "tg" }
func (a *trustGateAdapter) Connect(context.Context, map[string]any) error { return nil }
func (a *trustGateAdapter) Poll(_ context.Context, _ time.Time) ([]model.SourceItem, error) {
	return a.items, nil
}
func (a *trustGateAdapter) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (a *trustGateAdapter) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}
func (a *trustGateAdapter) WebhookHandler() http.Handler { return nil }

func (a *trustGateAdapter) RemoveLabels(_ context.Context, cell model.SourceItem, names []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.removedLabels == nil {
		a.removedLabels = map[string][]string{}
	}
	a.removedLabels[cell.ID] = append(a.removedLabels[cell.ID], names...)
	return nil
}

func (a *trustGateAdapter) AddLabels(_ context.Context, cell model.SourceItem, names []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.addedLabels == nil {
		a.addedLabels = map[string][]string{}
	}
	a.addedLabels[cell.ID] = append(a.addedLabels[cell.ID], names...)
	return nil
}

var (
	_ source.LabelRemover = (*trustGateAdapter)(nil)
	_ source.LabelAdder   = (*trustGateAdapter)(nil)
)

// newTrustGateDispatcher builds a minimal Dispatcher for trust-gate tests.
// The source type "fake" is already registered (via fanout_test.go's init);
// we swap in a trustGateAdapter instance after construction.
func newTrustGateDispatcher(t *testing.T, requireTrustedAuthor bool) (*Dispatcher, *countingRunner, *trustGateAdapter) {
	t.Helper()
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "tg.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{
			ID:   "src",
			Type: "fake",
			Security: config.SourceSecurity{
				RequireTrustedAuthor: requireTrustedAuthor,
			},
		}},
		Agents:  []config.AgentConfig{{ID: "a", Model: "test/model"}},
		Workers: nil,
		Workflows: []config.WorkflowConfig{{
			ID: "do-work",
			Trigger: &config.TriggerConfig{
				Match: config.RouteMatch{Source: "src", Labels: []string{"ai-ready"}},
			},
			Steps: []config.StepConfig{{ID: "run", Agent: "a"}},
		}},
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("router: %v", err)
	}

	runner := &countingRunner{}
	adapter := &trustGateAdapter{}

	d := &Dispatcher{
		cfg:             cfg,
		db:              dbc,
		binder:          source.NewSourceBinder(dbc),
		router:          r,
		sources:         map[string]source.Adapter{"src": adapter},
		runners:         map[string]runnerpkg.Runner{"agent-a": runner},
		agentRunner:     map[string]string{"a": "test"},
		agentFallbacks:  map[string][]runnerCandidate{},
		rateLimitPaused: map[string]time.Time{},
		agentSem:        map[string]chan struct{}{"a": make(chan struct{}, 1)},
		stats:           map[string]*sourceStat{"src": {}},
		sem:             make(chan struct{}, 1),
	}

	return d, runner, adapter
}

// TestTrustGate_UntrustedNotDispatched verifies that when RequireTrustedAuthor
// is enabled, items with a non-collaborator association are never dispatched,
// their ai-ready label is removed, and needs-triage is applied.
func TestTrustGate_UntrustedNotDispatched(t *testing.T) {
	d, runner, adapter := newTrustGateDispatcher(t, true)
	adapter.items = []model.SourceItem{{
		ID:                "1",
		SourceID:          "src",
		Title:             "Untrusted issue",
		Labels:            []string{"ai-ready"},
		AuthorAssociation: "NONE",
		State:             "open",
	}}

	sc := d.cfg.Sources[0]
	d.poll(context.Background(), sc, adapter, time.Time{})

	if runner.n.Load() != 0 {
		t.Errorf("dispatch count = %d, want 0 (untrusted item must not be dispatched)", runner.n.Load())
	}

	adapter.mu.Lock()
	removed := adapter.removedLabels["1"]
	added := adapter.addedLabels["1"]
	adapter.mu.Unlock()

	if !sliceContains(removed, "ai-ready") {
		t.Errorf("ai-ready not removed from untrusted item; removed=%v", removed)
	}
	if !sliceContains(added, "needs-triage") {
		t.Errorf("needs-triage not added to untrusted item; added=%v", added)
	}
}

// TestTrustGate_TrustedIsNotBlocked verifies that trusted authors pass through
// the gate without label changes. The item may or may not dispatch (routing
// rules apply after the gate), but ai-ready must not be stripped.
func TestTrustGate_TrustedIsNotBlocked(t *testing.T) {
	d, _, adapter := newTrustGateDispatcher(t, true)
	adapter.items = []model.SourceItem{{
		ID:                "2",
		SourceID:          "src",
		Title:             "Collaborator issue",
		Labels:            []string{"ai-ready"},
		AuthorAssociation: "COLLABORATOR",
		State:             "open",
	}}

	sc := d.cfg.Sources[0]
	d.poll(context.Background(), sc, adapter, time.Time{})

	adapter.mu.Lock()
	removed := adapter.removedLabels["2"]
	adapter.mu.Unlock()

	if sliceContains(removed, "ai-ready") {
		t.Error("ai-ready was stripped from a trusted COLLABORATOR item — gate must not block them")
	}
}

// TestTrustGate_DisabledAllowsAll verifies that with RequireTrustedAuthor=false
// even items with NONE association are not stripped by the gate.
func TestTrustGate_DisabledAllowsAll(t *testing.T) {
	d, _, adapter := newTrustGateDispatcher(t, false)
	adapter.items = []model.SourceItem{{
		ID:                "3",
		SourceID:          "src",
		Title:             "Gate-disabled issue",
		Labels:            []string{"ai-ready"},
		AuthorAssociation: "NONE",
		State:             "open",
	}}

	sc := d.cfg.Sources[0]
	d.poll(context.Background(), sc, adapter, time.Time{})

	adapter.mu.Lock()
	removed := adapter.removedLabels["3"]
	adapter.mu.Unlock()

	if sliceContains(removed, "ai-ready") {
		t.Error("ai-ready was removed even though RequireTrustedAuthor=false")
	}
}

// TestTrustGate_EmptyAssociationPassesThrough verifies that an item without an
// AuthorAssociation (e.g. from a Plane or Jira source) is never blocked.
func TestTrustGate_EmptyAssociationPassesThrough(t *testing.T) {
	d, _, adapter := newTrustGateDispatcher(t, true)
	adapter.items = []model.SourceItem{{
		ID:                "4",
		SourceID:          "src",
		Title:             "Non-GitHub issue",
		Labels:            []string{"ai-ready"},
		AuthorAssociation: "",
		State:             "open",
	}}

	sc := d.cfg.Sources[0]
	d.poll(context.Background(), sc, adapter, time.Time{})

	adapter.mu.Lock()
	removed := adapter.removedLabels["4"]
	adapter.mu.Unlock()

	if sliceContains(removed, "ai-ready") {
		t.Error("empty AuthorAssociation was treated as untrusted; it should pass through the gate")
	}
}

func sliceContains(s []string, target string) bool {
	for _, v := range s {
		if v == target {
			return true
		}
	}
	return false
}
