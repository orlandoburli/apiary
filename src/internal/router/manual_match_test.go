package router

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

// A trigger-less workflow is invisible to New (it synthesizes routes from
// triggers only), which is exactly the workflow a manual run has to reach.
func TestManualMatch_TriggerlessWorkflow(t *testing.T) {
	wf := config.WorkflowConfig{
		ID: "nightly-audit",
		Steps: []config.StepConfig{
			{ID: "gate", Type: config.StepTypeApproval},
			{ID: "scan", Agent: "auditor"},
			{ID: "report", Agent: "writer"},
		},
	}

	r, err := New(&config.Config{Workflows: []config.WorkflowConfig{wf}})
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	if len(r.routes) != 0 {
		t.Fatalf("setup: %d route(s) for a trigger-less workflow, want 0", len(r.routes))
	}

	m, ok := ManualMatch(wf)
	if !ok {
		t.Fatal("ManualMatch refused a trigger-less workflow")
	}
	if m.Route.ID != "nightly-audit" {
		t.Errorf("route id = %q, want the workflow id (resolveWorkflow keys on it)", m.Route.ID)
	}
	// The first *agent* step, not the first step: the id feeds the agent
	// semaphore and runner lookup.
	if m.Route.Agent != "auditor" {
		t.Errorf("agent = %q, want %q", m.Route.Agent, "auditor")
	}
}

// A triggered workflow keeps its priority and match block, so a manual dispatch
// logs and queues the same way an automatic one does.
func TestManualMatch_CarriesTriggerAttributes(t *testing.T) {
	wf := config.WorkflowConfig{
		ID: "triage",
		Trigger: &config.TriggerConfig{
			Priority: 7,
			Once:     true,
			Match:    config.RouteMatch{Labels: []string{"ai-ready"}},
		},
		Steps: []config.StepConfig{{ID: "run", Agent: "a"}},
	}

	m, ok := ManualMatch(wf)
	if !ok {
		t.Fatal("ManualMatch refused a triggered workflow")
	}
	if m.Route.Priority != 7 {
		t.Errorf("priority = %d, want 7", m.Route.Priority)
	}
	if len(m.Route.Match.Labels) != 1 || m.Route.Match.Labels[0] != "ai-ready" {
		t.Errorf("match labels = %v, want [ai-ready]", m.Route.Match.Labels)
	}
	// `once` is deliberately NOT carried over: it is a guard the poll loop
	// evaluates, and a manual run must not be droppable by it.
	if m.Route.Once {
		t.Error("Once carried into the manual match — a manual run must not be guarded by it")
	}
}

func TestManualMatch_RejectsWorkflowWithoutID(t *testing.T) {
	if _, ok := ManualMatch(config.WorkflowConfig{Steps: []config.StepConfig{{ID: "run", Agent: "a"}}}); ok {
		t.Fatal("ManualMatch accepted a workflow with no id; resolveWorkflow could not resolve it back")
	}
}
