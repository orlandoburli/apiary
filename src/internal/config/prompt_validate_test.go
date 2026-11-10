package config_test

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

// TestValidate_UnknownPromptRootFails covers the #526 validation requirement:
// a typo such as `${{ inputs.x }}` (plural) in a non-sub-workflow prompt must
// fail validation instead of reaching the agent unrendered.
func TestValidate_UnknownPromptRootFails(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{{
		ID: "w",
		Steps: []config.StepConfig{{
			ID:     "s",
			Agent:  "architect",
			Prompt: "Review ${{ inputs.dry_run }} now",
		}},
	}}

	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation error for unknown prompt root, got none")
	}
	if !strings.Contains(errs[0].Error(), `unknown expression root "inputs"`) {
		t.Errorf("error does not name the unknown root: %v", errs)
	}
}

// TestValidate_UnknownSummaryPromptRootFails checks summary_prompt is linted too.
func TestValidate_UnknownSummaryPromptRootFails(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{{
		ID: "w",
		Steps: []config.StepConfig{{
			ID:            "s",
			Agent:         "architect",
			SummaryPrompt: "Summarize ${{ bogus.field }}",
		}},
	}}

	errs := cfg.Validate()
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), `unknown expression root "bogus"`) {
		t.Fatalf("expected unknown-root error for summary_prompt, got: %v", errs)
	}
}

// TestValidate_KnownPromptRootsPass verifies every root the renderer understands
// is accepted.
func TestValidate_KnownPromptRootsPass(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{{
		ID: "w",
		Steps: []config.StepConfig{{
			ID:    "s",
			Agent: "architect",
			Prompt: "${{ input.dry_run }} ${{ event.pr_url }} ${{ memory.verdict }} " +
				"${{ cell.title }} ${{ steps.plan.output }} ${{ task.title }}",
		}},
	}}

	assertNoError(t, cfg)
}

// TestValidate_InputsRootAllowedInSubworkflow verifies the same plural root is
// accepted where sub-workflow inputs actually apply.
func TestValidate_InputsRootAllowedInSubworkflow(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{
			ID: "parent",
			Steps: []config.StepConfig{{
				ID:       "call",
				Type:     config.StepTypeWorkflow,
				Workflow: "child",
			}},
		},
		{
			ID:     "child",
			Inputs: map[string]config.WorkflowInput{"repository": {Type: "string"}},
			Steps: []config.StepConfig{{
				ID:     "run",
				Agent:  "architect",
				Prompt: "Clone ${{ inputs.repository }}",
			}},
		},
	}

	assertNoError(t, cfg)
}

// TestValidate_ForeachItemAliasAllowed avoids rejecting the documented foreach
// item alias form inside an inner step prompt.
func TestValidate_ForeachItemAliasAllowed(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{{
		ID: "w",
		Steps: []config.StepConfig{
			{
				ID:    "plan",
				Agent: "architect",
				OutputSchema: &config.OutputSchema{
					Type: "object",
					Properties: map[string]config.SchemaField{
						"tasks": {Type: "array", Items: &config.SchemaField{Type: "string"}},
					},
				},
				Memory: &config.MemoryConfig{Write: []string{"tasks"}},
			},
			{
				ID:        "each",
				Type:      config.StepTypeForeach,
				DependsOn: []string{"plan"},
				Items:     "steps.plan.output.tasks",
				As:        "issue",
				Step: &config.StepConfig{
					ID:     "one",
					Agent:  "architect",
					Prompt: "Do ${{ issue.title }}",
				},
			},
		},
	}}

	assertNoError(t, cfg)
}
