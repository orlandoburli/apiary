package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// v2ForeachWorkflow builds a workflow in **v2 authoring form** — `for_each:` as
// an expression with a nested body — and lowers it the way Config.Validate does
// before the engine ever sees it.
func v2ForeachWorkflow(t *testing.T, forEachExpr string) config.WorkflowConfig {
	t.Helper()
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
				ID:          "fix-each",
				ForEachExpr: forEachExpr,
				As:          "issue",
				SubSteps: []config.StepConfig{
					{ID: "fix", Agent: "backend-dev", Prompt: "Fix {{ issue.file }}"},
				},
			},
		},
	}
	lowered, err := config.LowerV2Workflow(wf)
	if err != nil {
		t.Fatalf("lower v2 workflow: %v", err)
	}
	return lowered
}

// Regression: the v2 lowering emitted the items path with a plural "outputs"
// segment, but both consumers require the singular "output" — so every
// v2-authored for_each failed at runtime with `invalid items path`. This is the
// end-to-end form: author in v2, lower, run, and require the body to execute
// once per item.
func TestForeach_V2LoweredRunsOnePerItem(t *testing.T) {
	for _, expr := range []string{
		"${{ plan.issues }}",               // short form
		"${{ steps.plan.output.issues }}",  // explicit, singular
		"${{ steps.plan.outputs.issues }}", // explicit, plural (v2 docs spelling)
	} {
		t.Run(expr, func(t *testing.T) {
			cfg := baseCfg()
			store := newFakeStore()
			exec := newSeqExecutor()
			exec.scripts["plan"] = []StepResult{planWithIssues(3)}
			eng := testEngine(cfg, store, exec, &fakeSide{})

			wf := v2ForeachWorkflow(t, expr)

			_, success, _ := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
			if !success {
				t.Fatalf("instance failed; the for_each step could not resolve its items (seen=%v)", exec.seenIDs())
			}

			subRuns := 0
			for _, r := range exec.seenIDs() {
				if strings.HasPrefix(r, "fix-each[") {
					subRuns++
				}
			}
			if subRuns != 3 {
				t.Fatalf("expected 3 sub-runs (one per item), got %d (seen=%v)", subRuns, exec.seenIDs())
			}
		})
	}
}
