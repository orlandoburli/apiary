package router_test

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/router"
)

func cfg(routes []config.RouteConfig, _ []config.WorkerConfig) *config.Config {
	// Routes are now expressed as workflow triggers; convert for test compatibility.
	// Worker-based routes are rewritten to agent-based (worker ID becomes agent ID).
	wfs := make([]config.WorkflowConfig, 0, len(routes))
	for _, r := range routes {
		agent := r.Agent
		if agent == "" {
			agent = r.Worker // backward compat: treat worker ID as agent ID
		}
		wfs = append(wfs, config.WorkflowConfig{
			ID:      r.ID,
			Trigger: &config.TriggerConfig{Priority: r.Priority, Match: r.Match},
			Steps:   []config.StepConfig{{ID: "run", Agent: agent}},
		})
	}
	return &config.Config{
		Version:   "1",
		Workflows: wfs,
	}
}

func worker(id string) config.WorkerConfig {
	return config.WorkerConfig{ID: id, Runner: "cli", Model: "test/model"}
}

func cell(opts ...func(*model.SourceItem)) model.SourceItem {
	c := model.SourceItem{SourceID: "src-a", Type: "bug", Priority: "high"}
	for _, o := range opts {
		o(&c)
	}
	return c
}

func withSource(id string) func(*model.SourceItem) {
	return func(c *model.SourceItem) { c.SourceID = id }
}
func withLabels(l ...string) func(*model.SourceItem) {
	return func(c *model.SourceItem) { c.Labels = l }
}
func withType(t string) func(*model.SourceItem) { return func(c *model.SourceItem) { c.Type = t } }
func withPriority(p string) func(*model.SourceItem) {
	return func(c *model.SourceItem) { c.Priority = p }
}
func withTitle(t string) func(*model.SourceItem) { return func(c *model.SourceItem) { c.Title = t } }
func withState(s string) func(*model.SourceItem) { return func(c *model.SourceItem) { c.State = s } }

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
		c     model.SourceItem
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

func assertMatch(t *testing.T, ok bool, m router.Match, wantAgent string) {
	t.Helper()
	if !ok {
		t.Fatal("expected a match, got none")
	}
	if m.Route.Agent != wantAgent {
		t.Errorf("matched agent = %q, want %q", m.Route.Agent, wantAgent)
	}
}

// TestRoute_WorkflowTriggerBecomesRoute verifies a workflow's trigger is
// ingested as a synthetic route whose id equals the workflow id, so the
// dispatcher can upgrade the match to the multi-step definition. The synthetic
// route's agent is the workflow's first agent step (used for admission/logging).
func TestRoute_WorkflowTriggerBecomesRoute(t *testing.T) {
	c := &config.Config{
		Version: "1",
		Workflows: []config.WorkflowConfig{{
			ID: "triage",
			Trigger: &config.TriggerConfig{
				Priority: 100,
				Match:    config.RouteMatch{Source: "src-a", ExcludeLabelPrefix: "agent:"},
			},
			Steps: []config.StepConfig{
				{ID: "classify", Agent: "investigator"},
				{ID: "route", Type: config.StepTypeSplit, Branches: []config.SplitBranch{{Else: true, Goto: "classify"}}},
			},
		}},
	}
	r, err := router.New(c)
	if err != nil {
		t.Fatal(err)
	}

	// An unlabeled cell triggers the workflow.
	m, ok := r.Route(cell(withSource("src-a")))
	if !ok {
		t.Fatal("expected unlabeled cell to match the triage trigger")
	}
	if m.Route.ID != "triage" {
		t.Errorf("Route.ID = %q, want %q (so resolveWorkflow upgrades to the workflow)", m.Route.ID, "triage")
	}
	if m.Route.Agent != "investigator" {
		t.Errorf("Route.Agent = %q, want %q (first agent step)", m.Route.Agent, "investigator")
	}

	// A cell already bearing an agent: label is excluded by the trigger.
	if _, ok := r.Route(cell(withSource("src-a"), withLabels("agent:engineer"))); ok {
		t.Error("expected agent-labeled cell to be excluded from the triage trigger")
	}
}

// TestRoute_ExplicitRouteWinsOverTriggerByPriority verifies explicit routes and
// trigger routes are merged into one priority-ordered set.
func TestRoute_ExplicitRouteWinsOverTriggerByPriority(t *testing.T) {
	c := &config.Config{
		Version: "1",
		Workflows: []config.WorkflowConfig{
			{
				ID:      "direct-engineer",
				Trigger: &config.TriggerConfig{Priority: 10, Match: config.RouteMatch{Source: "src-a", Labels: []string{"agent:engineer"}}},
				Steps:   []config.StepConfig{{ID: "run", Agent: "engineer"}},
			},
			{
				ID:      "triage",
				Trigger: &config.TriggerConfig{Priority: 100, Match: config.RouteMatch{Source: "src-a", ExcludeLabelPrefix: "agent:"}},
				Steps:   []config.StepConfig{{ID: "classify", Agent: "investigator"}},
			},
		},
	}
	r, err := router.New(c)
	if err != nil {
		t.Fatal(err)
	}

	m, ok := r.Route(cell(withSource("src-a"), withLabels("agent:engineer")))
	if !ok {
		t.Fatal("expected a match")
	}
	if m.Route.ID != "direct-engineer" {
		t.Errorf("Route.ID = %q, want the explicit lower-priority route", m.Route.ID)
	}
}
