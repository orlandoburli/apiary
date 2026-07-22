package config_test

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

func TestWorkflowContractRequiredInput(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "child", Inputs: map[string]config.WorkflowInput{
			"repository": {Type: "string", Required: true},
		}, Steps: []config.StepConfig{{ID: "run", Agent: "architect"}}},
		{ID: "parent", Steps: []config.StepConfig{{ID: "call", Type: config.StepTypeWorkflow, Workflow: "child"}}},
	}
	assertError(t, cfg, `required input "repository"`)
}

func TestWorkflowContractRejectsOutputTypeMismatch(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{{
		ID: "child",
		Outputs: map[string]config.WorkflowOutput{
			"workspace": {Type: "boolean", Value: "${{ steps.run.workspace }}"},
		},
		Steps: []config.StepConfig{{
			ID: "run", Agent: "architect",
			OutputSchema: &config.OutputSchema{Type: "object", Properties: map[string]config.SchemaField{
				"workspace": {Type: "string"},
			}},
		}},
	}}
	assertError(t, cfg, `does not match step "run" field "workspace" type "string"`)
}
