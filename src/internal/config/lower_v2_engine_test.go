package config_test

// This file lives in the external test package so it can import the runtime
// workflow expr engine (which itself imports config) without an import cycle.
// It is the end-to-end proof for the v2 if:-shorthand lowering fix: the lowered
// condition must be something the engine's expr parser/evaluator actually
// accepts, and the field it reads must be one the emitting step persists.

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/workflow"
)

// TestLowerV2_IfShorthandIsRuntimeValid lowers the documented v2 example and then
// feeds the result through the real expr engine. It guards both bugs against
// regression at the layer that actually matters: the runtime.
func TestLowerV2_IfShorthandIsRuntimeValid(t *testing.T) {
	wf := config.WorkflowConfig{
		ID: "triage",
		Steps: []config.StepConfig{
			{ID: "classify", Agent: "investigator",
				Output: &config.OutputSchema{Type: "object",
					Properties: map[string]config.SchemaField{"track": {Type: "string"}}}},
			{
				ID: "complex-track",
				If: `${{ classify.track == 'complex' }}`,
				SubSteps: []config.StepConfig{
					{ID: "design", Agent: "staff"},
				},
			},
		},
	}

	lowered, err := config.LowerV2Workflow(wf)
	if err != nil {
		t.Fatalf("LowerV2Workflow: %v", err)
	}

	// Locate the lowered child step and its emitting predecessor.
	var classify, design config.StepConfig
	for _, s := range lowered.Steps {
		switch s.ID {
		case "classify":
			classify = s
		case "design":
			design = s
		}
	}
	if design.ID == "" || classify.ID == "" {
		t.Fatalf("expected classify+design in lowered steps, got %v", lowered.Steps)
	}

	// The emitting step must persist the field the condition reads — otherwise the
	// memory accessor resolves to empty at runtime and the guard never matches.
	if got := classify.MemoryWriteFields(); !contains(got, "track") {
		t.Fatalf("classify.memory.write = %v, want it to contain \"track\"", got)
	}

	// The lowered condition must parse and evaluate under the runtime expr engine.
	expr, err := workflow.ParseExpr(design.Condition)
	if err != nil {
		t.Fatalf("engine rejected lowered condition %q: %v", design.Condition, err)
	}
	gotTrue, err := expr.Eval(workflow.EvalContext{Memory: map[string]string{"track": "complex"}})
	if err != nil {
		t.Fatalf("eval(track=complex) errored: %v", err)
	}
	if !gotTrue {
		t.Errorf("condition %q with track=complex = false, want true", design.Condition)
	}
	gotFalse, err := expr.Eval(workflow.EvalContext{Memory: map[string]string{"track": "implement"}})
	if err != nil {
		t.Fatalf("eval(track=implement) errored: %v", err)
	}
	if gotFalse {
		t.Errorf("condition %q with track=implement = true, want false", design.Condition)
	}

	// Negative control: the un-rewritten shorthand parses syntactically but the
	// engine rejects `classify.*` at eval time (unknown accessor root). That eval
	// error is exactly what the engine swallows into a silent skip — i.e. the bug.
	bad, err := workflow.ParseExpr(`classify.track == 'complex'`)
	if err != nil {
		t.Fatalf("setup: bare step-ref should still parse, got %v", err)
	}
	if _, err := bad.Eval(workflow.EvalContext{Memory: map[string]string{"track": "complex"}}); err == nil {
		t.Error("expected eval error for bare step-ref `classify.track`, got nil (the lowering must rewrite it)")
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
