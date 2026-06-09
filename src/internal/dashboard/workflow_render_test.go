package dashboard

import (
	"strings"
	"testing"
	"time"
)

func TestRenderWorkflowSteps_CIPolls(t *testing.T) {
	base := time.Date(2026, 6, 8, 17, 40, 0, 0, time.UTC)
	inst := &WorkflowInstanceItem{
		ID:       "wf_ci",
		Workflow: "implementation",
		State:    "waiting",
		Steps: []WorkflowStepItem{
			{StepID: "implement", Agent: "engineer", State: "passed", Duration: "30s"},
		},
		CIPolls: []CIPollItem{
			{StepID: "check-ci", Status: "pending", CheckedAt: base},
			{StepID: "check-ci", Status: "pending", CheckedAt: base.Add(time.Minute)},
			{StepID: "check-ci", Status: "failed", Detail: `{"build":"failure"}`, CheckedAt: base.Add(2 * time.Minute)},
		},
	}
	out := stripANSI(renderWorkflowSteps(inst))
	if !strings.Contains(out, "CI Polls (3)") {
		t.Errorf("expected CI poll count header; got:\n%s", out)
	}
	if !strings.Contains(out, "last: failed") {
		t.Errorf("expected latest poll status in header; got:\n%s", out)
	}
	if !strings.Contains(out, `{"build":"failure"}`) {
		t.Errorf("expected per-check detail in a poll row; got:\n%s", out)
	}
}

func TestRenderWorkflowSteps_NoCIPolls(t *testing.T) {
	inst := &WorkflowInstanceItem{
		ID:       "wf_plain",
		Workflow: "implementation",
		State:    "done",
		Steps:    []WorkflowStepItem{{StepID: "implement", Agent: "engineer", State: "passed"}},
	}
	out := stripANSI(renderWorkflowSteps(inst))
	if strings.Contains(out, "CI Polls") {
		t.Errorf("instance with no polls should not render a CI Polls section:\n%s", out)
	}
}

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

// A failed instance whose recorded steps all passed (the run died in a later
// step that never persisted a step_run) must not read as a completed run — a
// marker should call out where it failed.
func TestRenderWorkflowSteps_FailedAfterPassedSteps(t *testing.T) {
	inst := &WorkflowInstanceItem{
		ID:       "wf_f1",
		Workflow: "implementation",
		State:    "failed",
		Steps: []WorkflowStepItem{
			{StepID: "implement", Agent: "engineer", State: "passed", Duration: "30s"},
		},
	}
	out := stripANSI(renderWorkflowSteps(inst))
	if !strings.Contains(out, "no further steps recorded") {
		t.Errorf("expected a failure marker for an all-passed failed instance:\n%s", out)
	}
	if !strings.Contains(out, "after step 'implement'") {
		t.Errorf("marker should name the last recorded step:\n%s", out)
	}
}

// When the failing step is itself recorded, the list already shows it — no
// redundant marker.
func TestRenderWorkflowSteps_FailedStepVisible(t *testing.T) {
	inst := &WorkflowInstanceItem{
		ID:       "wf_f2",
		Workflow: "implementation",
		State:    "failed",
		Steps: []WorkflowStepItem{
			{StepID: "implement", Agent: "engineer", State: "passed", Duration: "30s"},
			{StepID: "review", Agent: "reviewer", State: "failed", Duration: "12s"},
		},
	}
	out := stripANSI(renderWorkflowSteps(inst))
	if strings.Contains(out, "no further steps recorded") {
		t.Errorf("should not add a marker when a failed step is already visible:\n%s", out)
	}
}

// A failed instance with zero recorded steps still gets a marker.
func TestRenderWorkflowSteps_FailedNoSteps(t *testing.T) {
	inst := &WorkflowInstanceItem{ID: "wf_f3", Workflow: "implementation", State: "failed"}
	out := stripANSI(renderWorkflowSteps(inst))
	if !strings.Contains(out, "before any step completed") {
		t.Errorf("expected a failure marker for a failed instance with no steps:\n%s", out)
	}
}

// A non-failed instance never gets the marker.
func TestRenderWorkflowSteps_NoMarkerWhenDone(t *testing.T) {
	inst := &WorkflowInstanceItem{
		ID:       "wf_ok",
		Workflow: "implementation",
		State:    "done",
		Steps:    []WorkflowStepItem{{StepID: "implement", Agent: "engineer", State: "passed"}},
	}
	out := stripANSI(renderWorkflowSteps(inst))
	if strings.Contains(out, "workflow failed") {
		t.Errorf("done instance should not show a failure marker:\n%s", out)
	}
}
