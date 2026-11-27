package config_test

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

// Outcomes are the gate's answers, and their values are what a response and
// memory key off — two outcomes that differ only in case are one answer with
// two spellings, which is a config bug rather than a feature.
func TestWorkflow_ApprovalOutcomeValuesAreUnique(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{{ID: "wf", Steps: []config.StepConfig{
		{ID: "gate", Type: config.StepTypeApproval, Message: "ok",
			ApprovalOutcomes: []config.ApprovalOutcome{{Value: "approve"}, {Value: "APPROVE"}}},
		{ID: "done", Agent: "dev", DependsOn: []string{"gate"}},
	}}}
	assertError(t, cfg, "duplicate approval outcome")
}

// An outcome value is required: it is the answer that is recorded and stored,
// so an outcome with no value is unanswerable.
func TestWorkflow_ApprovalOutcomeValueIsRequired(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{{ID: "wf", Steps: []config.StepConfig{
		{ID: "gate", Type: config.StepTypeApproval, Message: "ok",
			ApprovalOutcomes: []config.ApprovalOutcome{{Label: "no value"}}},
		{ID: "done", Agent: "dev", DependsOn: []string{"gate"}},
	}}}
	assertError(t, cfg, "approval outcome value is required")
}

// A goto is a step reference, checked like on_fail.goto: a typo must fail at
// load, not at the moment a human answers.
func TestWorkflow_ApprovalOutcomeGotoMustExist(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{{ID: "wf", Steps: []config.StepConfig{
		{ID: "gate", Type: config.StepTypeApproval, Message: "ok",
			ApprovalOutcomes: []config.ApprovalOutcome{{Value: "rework", Goto: "typo"}}},
		{ID: "done", Agent: "dev", DependsOn: []string{"gate"}},
	}}}
	assertError(t, cfg, "goto references unknown step")
}

// A gate with declared outcomes and no routing keys is valid: it continues, and
// later steps branch on memory.approval_outcome.
func TestWorkflow_ApprovalOutcomesAreValid(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{{ID: "wf", Steps: []config.StepConfig{
		{ID: "gate", Type: config.StepTypeApproval, Message: "ok",
			ApprovalOutcomes: []config.ApprovalOutcome{
				{Value: "approve", Label: "Ship it"},
				{Value: "rework", Goto: "done"},
				{Value: "abort", Fail: true},
			}},
		{ID: "done", Agent: "architect", DependsOn: []string{"gate"}},
	}}}
	if errs := cfg.Validate(); len(errs) != 0 {
		t.Fatalf("expected a valid config, got %v", errs)
	}
}

// The implicit pair is what a gate without outcomes offers.
func TestApprovalOutcomesOrFallsBackToThePair(t *testing.T) {
	got := config.StepConfig{}.ApprovalOutcomesOr()
	if len(got) != 2 || got[0].Value != "approve" || got[1].Value != "reject" || !got[1].Fail {
		t.Fatalf("default outcomes = %+v, want approve + fail-on-reject", got)
	}
}
