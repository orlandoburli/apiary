package config_test

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

// findStep returns the lowered step with the given id.
func findStep(t *testing.T, wf config.WorkflowConfig, id string) config.StepConfig {
	t.Helper()
	for _, s := range wf.Steps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("step %q not found in lowered workflow (steps=%d)", id, len(wf.Steps))
	return config.StepConfig{}
}

// The items path must use the singular "output" segment — that is what
// workflow.resolveItemsFromContrib and foreachItemsResolveToArray both require.
// The lowering used to emit the plural "outputs", so no v2 for_each could run.
func TestLowerV2_ForEachEmitsSingularOutputItemsPath(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"short form", "${{ plan.issues }}"},
		{"explicit singular", "${{ steps.plan.output.issues }}"},
		{"explicit plural (v2 expression spelling)", "${{ steps.plan.outputs.issues }}"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wf := config.WorkflowConfig{
				ID: "w",
				Steps: []config.StepConfig{
					{
						ID: "plan", Agent: "architect",
						OutputSchema: &config.OutputSchema{Type: "object",
							Properties: map[string]config.SchemaField{
								"issues": {Type: "array", Items: &config.SchemaField{Type: "object"}},
							}},
					},
					{
						ID: "fan", ForEachExpr: tc.expr, As: "issue",
						SubSteps: []config.StepConfig{{ID: "fix", Agent: "backend-dev"}},
					},
				},
			}

			lowered, err := config.LowerV2Workflow(wf)
			if err != nil {
				t.Fatalf("lower: %v", err)
			}

			fan := findStep(t, lowered, "fan")
			if want := "steps.plan.output.issues"; fan.Items != want {
				t.Errorf("items path = %q, want %q", fan.Items, want)
			}

			// The referenced field must be auto-added to the producing step's
			// memory.write, or the array never reaches the contribution snapshot
			// the runtime reads items from.
			plan := findStep(t, lowered, "plan")
			if plan.Memory == nil {
				t.Fatalf("plan step has no memory config; expected %q auto-added to memory.write", "issues")
			}
			found := false
			for _, w := range plan.Memory.Write {
				if w == "issues" {
					found = true
				}
			}
			if !found {
				t.Errorf("memory.write = %v, want it to contain %q", plan.Memory.Write, "issues")
			}
		})
	}
}

// A non-steps path (e.g. memory.*) must pass through untouched.
func TestLowerV2_ForEachMemoryPathUntouched(t *testing.T) {
	wf := config.WorkflowConfig{
		ID: "w",
		Steps: []config.StepConfig{
			{ID: "plan", Agent: "architect"},
			{
				ID: "fan", ForEachExpr: "${{ memory.issues }}", As: "issue",
				SubSteps: []config.StepConfig{{ID: "fix", Agent: "backend-dev"}},
			},
		},
	}
	lowered, err := config.LowerV2Workflow(wf)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	if got := findStep(t, lowered, "fan").Items; got != "memory.issues" {
		t.Errorf("items path = %q, want it passed through unchanged", got)
	}
}
