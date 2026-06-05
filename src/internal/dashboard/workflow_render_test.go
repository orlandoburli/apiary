package dashboard

import (
	"strings"
	"testing"
)

func TestRenderWorkflowSteps(t *testing.T) {
	inst := &WorkflowInstanceItem{
		ID:       "wf_1",
		Workflow: "feature-development",
		State:    "running",
		Steps: []WorkflowStepItem{
			{StepID: "plan", Agent: "architect", State: "passed", Duration: "12s"},
			{StepID: "implement", Agent: "backend-dev", State: "running", Duration: "3s"},
			{StepID: "review", Agent: "reviewer", State: "pending", Duration: "—"},
			{StepID: "cached-step", Agent: "architect", State: "passed", Duration: "5s", Cached: true},
		},
	}

	out := stripANSI(renderWorkflowSteps(inst))

	for _, want := range []string{"feature-development", "running", "Steps", "plan", "implement", "review", "(cached)"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered steps missing %q:\n%s", want, out)
		}
	}
}

func TestRenderWorkflowSteps_ApprovalBanner(t *testing.T) {
	inst := &WorkflowInstanceItem{
		ID:       "wf_2",
		Workflow: "feature-development",
		State:    "approval_waiting",
		Message:  "Awaiting human approval — reply on the task to resume or abort.",
		Steps: []WorkflowStepItem{
			{StepID: "gate", Agent: "", State: "running", Duration: "—"},
		},
	}

	out := stripANSI(renderWorkflowSteps(inst))
	if !strings.Contains(out, "approval_waiting") {
		t.Errorf("expected approval_waiting badge:\n%s", out)
	}
	if !strings.Contains(out, "Awaiting human approval") {
		t.Errorf("expected approval message banner:\n%s", out)
	}
}

func TestRenderWorkflowSteps_NoSteps(t *testing.T) {
	inst := &WorkflowInstanceItem{ID: "wf_3", Workflow: "single", State: "done"}
	out := stripANSI(renderWorkflowSteps(inst))
	if !strings.Contains(out, "single") || !strings.Contains(out, "done") {
		t.Errorf("expected workflow id and state even with no steps:\n%s", out)
	}
	if strings.Contains(out, "Steps") {
		t.Errorf("should not render a Steps header when there are no steps:\n%s", out)
	}
}
