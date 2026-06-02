package router_test

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/router"
)

func cfg(routes []config.RouteConfig, workers []config.WorkerConfig) *config.Config {
	return &config.Config{
		Version: "1",
		Workers: workers,
		Routes:  routes,
	}
}

func worker(id string) config.WorkerConfig {
	return config.WorkerConfig{ID: id, Runner: "cli", Model: "test/model"}
}

func cell(opts ...func(*model.Cell)) model.Cell {
	c := model.Cell{SourceID: "src-a", Type: "bug", Priority: "high"}
	for _, o := range opts {
		o(&c)
	}
	return c
}

func withSource(id string) func(*model.Cell)   { return func(c *model.Cell) { c.SourceID = id } }
func withLabels(l ...string) func(*model.Cell) { return func(c *model.Cell) { c.Labels = l } }
func withType(t string) func(*model.Cell)      { return func(c *model.Cell) { c.Type = t } }
func withPriority(p string) func(*model.Cell)  { return func(c *model.Cell) { c.Priority = p } }
func withTitle(t string) func(*model.Cell)     { return func(c *model.Cell) { c.Title = t } }
func withState(s string) func(*model.Cell)     { return func(c *model.Cell) { c.State = s } }

// ── states / exclusion matchers ────────────────────────────────────────────────

// agentRoute builds an agent-based route with the given match.
func agentRoute(id string, prio int, agent string, m config.RouteMatch) config.RouteConfig {
	return config.RouteConfig{ID: id, Priority: prio, Agent: agent, Match: m}
}

func TestRoute_StateFilter(t *testing.T) {
	r, err := router.New(cfg([]config.RouteConfig{
		agentRoute("todo-only", 10, "investigator", config.RouteMatch{Source: "src-a", States: []string{"todo"}}),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := r.Route(cell(withState("Todo"))); !ok {
		t.Error("expected match for state Todo (case-insensitive)")
	}
	if _, ok := r.Route(cell(withState("In Progress"))); ok {
		t.Error("expected no match for state In Progress")
	}
}

func TestRoute_ExcludeLabelPrefix(t *testing.T) {
	r, err := router.New(cfg([]config.RouteConfig{
		agentRoute("classify", 10, "investigator", config.RouteMatch{
			Source: "src-a", ExcludeLabelPrefix: "agent:",
		}),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := r.Route(cell(withLabels("type:bug"))); !ok {
		t.Error("expected match when no agent: label present")
	}
	if _, ok := r.Route(cell(withLabels("type:bug", "agent:engineer"))); ok {
		t.Error("expected no match when an agent: label is present")
	}
}

func TestRoute_ExcludeLabels(t *testing.T) {
	r, err := router.New(cfg([]config.RouteConfig{
		agentRoute("no-wip", 10, "investigator", config.RouteMatch{
			Source: "src-a", ExcludeLabels: []string{"wip"},
		}),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Route(cell(withLabels("ready"))); !ok {
		t.Error("expected match without excluded label")
	}
	if _, ok := r.Route(cell(withLabels("WIP"))); ok {
		t.Error("expected no match with excluded label (case-insensitive)")
	}
}

// TestRoute_InvestigatorFallback reproduces the intended pipeline: agent-labeled
// cells go to their agent; only an unlabeled TODO falls through to the
// investigator (highest priority number = last).
func TestRoute_InvestigatorFallback(t *testing.T) {
	routes := []config.RouteConfig{
		agentRoute("engineer", 20, "engineer", config.RouteMatch{Source: "src-a", Labels: []string{"agent:engineer"}}),
		agentRoute("classify", 100, "investigator", config.RouteMatch{
			Source: "src-a", States: []string{"todo"}, ExcludeLabelPrefix: "agent:",
		}),
	}
	r, err := router.New(cfg(routes, nil))
	if err != nil {
		t.Fatal(err)
	}

	// Unlabeled TODO → investigator.
	if m, ok := r.Route(cell(withState("Todo"))); !ok || m.Route.Agent != "investigator" {
		t.Errorf("unlabeled todo: got agent=%q ok=%v, want investigator", m.Route.Agent, ok)
	}
	// Labeled cell → engineer (not investigator), regardless of state.
	if m, ok := r.Route(cell(withState("Todo"), withLabels("agent:engineer"))); !ok || m.Route.Agent != "engineer" {
		t.Errorf("labeled: got agent=%q ok=%v, want engineer", m.Route.Agent, ok)
	}
	// Unlabeled but not TODO → no match (investigator gated to todo).
	if _, ok := r.Route(cell(withState("In Progress"))); ok {
		t.Error("unlabeled in-progress should not match the todo-gated investigator")
	}
}

// ── construction ──────────────────────────────────────────────────────────────

func TestNew_BadRegex(t *testing.T) {
	_, err := router.New(cfg(
		[]config.RouteConfig{{ID: "r1", Priority: 1, Worker: "w1",
			Match: config.RouteMatch{TitleRegex: "[invalid"}}},
		[]config.WorkerConfig{worker("w1")},
	))
	if err == nil {
		t.Fatal("expected error for invalid regex, got nil")
	}
}

func TestNew_ValidRegex(t *testing.T) {
	_, err := router.New(cfg(
		[]config.RouteConfig{{ID: "r1", Priority: 1, Worker: "w1",
			Match: config.RouteMatch{TitleRegex: `^fix:`}}},
		[]config.WorkerConfig{worker("w1")},
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── no match ──────────────────────────────────────────────────────────────────

func TestRoute_NoMatch(t *testing.T) {
	r, _ := router.New(cfg(
		[]config.RouteConfig{{ID: "r1", Priority: 1, Worker: "w1",
			Match: config.RouteMatch{Source: "other-source"}}},
		[]config.WorkerConfig{worker("w1")},
	))
	_, ok := r.Route(cell())
	if ok {
		t.Fatal("expected no match, got one")
	}
}

func TestRoute_EmptyRules_NoMatch(t *testing.T) {
	r, _ := router.New(cfg(nil, nil))
	_, ok := r.Route(cell())
	if ok {
		t.Fatal("expected no match with empty rules")
	}
}

// ── source matching ───────────────────────────────────────────────────────────

func TestRoute_MatchSource(t *testing.T) {
	r, _ := router.New(cfg(
		[]config.RouteConfig{{ID: "r1", Priority: 1, Worker: "w1",
			Match: config.RouteMatch{Source: "src-a"}}},
		[]config.WorkerConfig{worker("w1")},
	))
	m, ok := r.Route(cell(withSource("src-a")))
	assertMatch(t, ok, m, "w1")
}

func TestRoute_NoMatchWrongSource(t *testing.T) {
	r, _ := router.New(cfg(
		[]config.RouteConfig{{ID: "r1", Priority: 1, Worker: "w1",
			Match: config.RouteMatch{Source: "src-b"}}},
		[]config.WorkerConfig{worker("w1")},
	))
	_, ok := r.Route(cell(withSource("src-a")))
	if ok {
		t.Fatal("expected no match for wrong source")
	}
}

// ── label matching ────────────────────────────────────────────────────────────

func TestRoute_MatchAllLabels(t *testing.T) {
	r, _ := router.New(cfg(
		[]config.RouteConfig{{ID: "r1", Priority: 1, Worker: "w1",
			Match: config.RouteMatch{Labels: []string{"backend", "bug"}}}},
		[]config.WorkerConfig{worker("w1")},
	))
	// cell has both required labels plus an extra one
	m, ok := r.Route(cell(withLabels("bug", "backend", "urgent")))
	assertMatch(t, ok, m, "w1")
}

func TestRoute_NoMatchMissingLabel(t *testing.T) {
	r, _ := router.New(cfg(
		[]config.RouteConfig{{ID: "r1", Priority: 1, Worker: "w1",
			Match: config.RouteMatch{Labels: []string{"backend", "bug"}}}},
		[]config.WorkerConfig{worker("w1")},
	))
	_, ok := r.Route(cell(withLabels("bug"))) // missing "backend"
	if ok {
		t.Fatal("expected no match when a required label is absent")
	}
}

func TestRoute_LabelMatchIsCaseInsensitive(t *testing.T) {
	r, _ := router.New(cfg(
		[]config.RouteConfig{{ID: "r1", Priority: 1, Worker: "w1",
			Match: config.RouteMatch{Labels: []string{"Backend"}}}},
		[]config.WorkerConfig{worker("w1")},
	))
	m, ok := r.Route(cell(withLabels("backend")))
	assertMatch(t, ok, m, "w1")
}

// ── type matching ─────────────────────────────────────────────────────────────

func TestRoute_MatchType(t *testing.T) {
	r, _ := router.New(cfg(
		[]config.RouteConfig{{ID: "r1", Priority: 1, Worker: "w1",
			Match: config.RouteMatch{Types: []string{"feature", "improvement"}}}},
		[]config.WorkerConfig{worker("w1")},
	))
	m, ok := r.Route(cell(withType("feature")))
	assertMatch(t, ok, m, "w1")
}

func TestRoute_NoMatchWrongType(t *testing.T) {
	r, _ := router.New(cfg(
		[]config.RouteConfig{{ID: "r1", Priority: 1, Worker: "w1",
			Match: config.RouteMatch{Types: []string{"feature"}}}},
		[]config.WorkerConfig{worker("w1")},
	))
	_, ok := r.Route(cell(withType("bug")))
	if ok {
		t.Fatal("expected no match for wrong type")
	}
}

// ── priority matching ─────────────────────────────────────────────────────────

func TestRoute_MatchPriority(t *testing.T) {
	r, _ := router.New(cfg(
		[]config.RouteConfig{{ID: "r1", Priority: 1, Worker: "w1",
			Match: config.RouteMatch{Priority: []string{"urgent", "high"}}}},
		[]config.WorkerConfig{worker("w1")},
	))
	m, ok := r.Route(cell(withPriority("high")))
	assertMatch(t, ok, m, "w1")
}

// ── title regex ───────────────────────────────────────────────────────────────

func TestRoute_MatchTitleRegex(t *testing.T) {
	r, _ := router.New(cfg(
		[]config.RouteConfig{{ID: "r1", Priority: 1, Worker: "w1",
			Match: config.RouteMatch{TitleRegex: `^fix:`}}},
		[]config.WorkerConfig{worker("w1")},
	))
	m, ok := r.Route(cell(withTitle("fix: login crash")))
	assertMatch(t, ok, m, "w1")
}

func TestRoute_NoMatchTitleRegex(t *testing.T) {
	r, _ := router.New(cfg(
		[]config.RouteConfig{{ID: "r1", Priority: 1, Worker: "w1",
			Match: config.RouteMatch{TitleRegex: `^fix:`}}},
		[]config.WorkerConfig{worker("w1")},
	))
	_, ok := r.Route(cell(withTitle("feat: new button")))
	if ok {
		t.Fatal("expected no match for title not matching regex")
	}
}

// ── priority ordering ─────────────────────────────────────────────────────────

func TestRoute_FirstMatchWins(t *testing.T) {
	r, _ := router.New(cfg(
		[]config.RouteConfig{
			{ID: "low", Priority: 99, Worker: "w2", Match: config.RouteMatch{Source: "src-a"}},
			{ID: "high", Priority: 1, Worker: "w1", Match: config.RouteMatch{Source: "src-a"}},
		},
		[]config.WorkerConfig{worker("w1"), worker("w2")},
	))
	m, ok := r.Route(cell())
	assertMatch(t, ok, m, "w1") // priority 1 wins over priority 99
}

func TestRoute_FallsThrough_WhenHighPriorityNoMatch(t *testing.T) {
	r, _ := router.New(cfg(
		[]config.RouteConfig{
			{ID: "strict", Priority: 1, Worker: "w1",
				Match: config.RouteMatch{Labels: []string{"frontend"}}},
			{ID: "catch-all", Priority: 99, Worker: "w2",
				Match: config.RouteMatch{Source: "src-a"}},
		},
		[]config.WorkerConfig{worker("w1"), worker("w2")},
	))
	// cell has no "frontend" label → falls through to catch-all
	m, ok := r.Route(cell(withLabels("backend")))
	assertMatch(t, ok, m, "w2")
}

// ── combined conditions ───────────────────────────────────────────────────────

func TestRoute_CombinedConditions(t *testing.T) {
	r, _ := router.New(cfg(
		[]config.RouteConfig{{ID: "r1", Priority: 1, Worker: "w1",
			Match: config.RouteMatch{
				Source: "src-a",
				Labels: []string{"bug"},
				Types:  []string{"feature", "bug"},
			}}},
		[]config.WorkerConfig{worker("w1")},
	))
	tests := []struct {
		name  string
		c     model.Cell
		match bool
	}{
		{"all match", cell(withSource("src-a"), withLabels("bug"), withType("bug")), true},
		{"wrong source", cell(withSource("src-b"), withLabels("bug"), withType("bug")), false},
		{"missing label", cell(withSource("src-a"), withLabels(), withType("bug")), false},
		{"wrong type", cell(withSource("src-a"), withLabels("bug"), withType("feature")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := r.Route(tt.c)
			if ok != tt.match {
				t.Errorf("Route() = %v, want %v", ok, tt.match)
			}
		})
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func assertMatch(t *testing.T, ok bool, m router.Match, wantWorker string) {
	t.Helper()
	if !ok {
		t.Fatal("expected a match, got none")
	}
	if m.Worker.ID != wantWorker {
		t.Errorf("matched worker = %q, want %q", m.Worker.ID, wantWorker)
	}
}
