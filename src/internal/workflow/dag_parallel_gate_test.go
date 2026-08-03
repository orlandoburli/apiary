package workflow

import (
	"context"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// parallelGateWF builds the shape from project-erp #4140: implement → gate
// (parallel review+validate with a rejection gate) → merge.
func parallelGateWF(maxRetries int) config.WorkflowConfig {
	return config.WorkflowConfig{ID: "par-gate", Steps: []config.StepConfig{
		{ID: "implement", Agent: "backend-dev"},
		{
			ID: "gate", Type: config.StepTypeParallel, Join: "all",
			DependsOn: []string{"implement"},
			SubSteps: []config.StepConfig{
				{ID: "review", Agent: "architect",
					OutputSchema: &config.OutputSchema{Type: "object",
						Properties: map[string]config.SchemaField{"review_verdict": {Type: "string"}}},
					Memory: &config.MemoryConfig{Write: []string{"review_verdict"}}},
				{ID: "validate", Agent: "architect",
					OutputSchema: &config.OutputSchema{Type: "object",
						Properties: map[string]config.SchemaField{"qa_verdict": {Type: "string"}}},
					Memory: &config.MemoryConfig{Write: []string{"qa_verdict"}}},
			},
			FailWhen: `memory.review_verdict == "rejected" or memory.qa_verdict == "rejected"`,
			OnFail:   &config.StepOutcome{Goto: "implement", MaxRetries: maxRetries},
		},
		{ID: "merge", Agent: "backend-dev", DependsOn: []string{"gate"}},
	}}
}

// TestDAG_ParallelFailWhen_ChildRejectionTriggersLoopback verifies that a
// rejected verdict emitted by a parallel child is visible to the parent's
// fail_when at join time and triggers on_fail.goto restart of the outer step.
// Regression: the children's memory contributions are only merged after the
// fail_when check, so without the transient overlay the gate read stale/empty
// memory and passed a rejected result straight through to merge (#4140).
func TestDAG_ParallelFailWhen_ChildRejectionTriggersLoopback(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := newSeqExecutor()
	exec.scripts["review"] = []StepResult{
		{Success: true, StructuredOutput: map[string]any{"review_verdict": "approved"}},
		{Success: true, StructuredOutput: map[string]any{"review_verdict": "approved"}},
	}
	// validate: first round rejected, second round approved.
	exec.scripts["validate"] = []StepResult{
		{Success: true, StructuredOutput: map[string]any{"qa_verdict": "rejected"}},
		{Success: true, StructuredOutput: map[string]any{"qa_verdict": "approved"}},
	}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	_, success, err := eng.RunInstance(context.Background(), parallelGateWF(3), model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("expected success after loop-back retry")
	}
	if exec.ran("implement") != 2 {
		t.Errorf("expected implement to run twice (loop-back), got %d", exec.ran("implement"))
	}
	if exec.ran("validate") != 2 {
		t.Errorf("expected validate to run twice (reject + approve), got %d", exec.ran("validate"))
	}
	if exec.ran("merge") != 1 {
		t.Errorf("expected merge to run once, after the gate finally passed, got %d", exec.ran("merge"))
	}
}

// TestDAG_ParallelFailWhen_ExhaustedRetriesFailsInstance verifies the gate is
// terminal once on_fail.max_retries is exhausted — merge must never run.
func TestDAG_ParallelFailWhen_ExhaustedRetriesFailsInstance(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"review":   {Success: true, StructuredOutput: map[string]any{"review_verdict": "approved"}},
		"validate": {Success: true, StructuredOutput: map[string]any{"qa_verdict": "rejected"}},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	instID, success, err := eng.RunInstance(context.Background(), parallelGateWF(1), model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if success {
		t.Fatal("expected failure after exhausted gate retries")
	}
	if store.instances[instID].State != "failed" {
		t.Errorf("instance state = %q, want failed", store.instances[instID].State)
	}
	if countSeen(exec.seen, "merge") != 0 {
		t.Errorf("merge must not run after a rejected gate, ran %d times", countSeen(exec.seen, "merge"))
	}
}
