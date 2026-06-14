package cli

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

// TestLintExprWiring verifies the end-to-end #180 repro: with the cli wiring
// in place (init() injects the real workflow parser), a config whose `if:`
// uses C-style && fails Validate with a pointer to the supported operator —
// it must not pass validation and silently skip at runtime.
func TestLintExprWiring(t *testing.T) {
	if config.LintExpr == nil {
		t.Fatal("config.LintExpr not wired by cli init()")
	}

	c := &config.Config{
		Version: "1",
		Agents:  []config.AgentConfig{{ID: "engineer", Model: "m"}},
		Workflows: []config.WorkflowConfig{{
			ID: "w",
			Steps: []config.StepConfig{
				{ID: "judge", Agent: "engineer"},
				{ID: "standard-track", Agent: "engineer",
					If: `${{ memory.engineer_track != "100x" && memory.engineer_track != "decomposed" }}`},
			},
		}},
	}

	errs := c.Validate()
	var hit bool
	for _, err := range errs {
		if strings.Contains(err.Error(), "use 'and'") {
			hit = true
		}
	}
	if !hit {
		t.Errorf("expected a validation error suggesting 'and' for '&&', got: %v", errs)
	}
}
