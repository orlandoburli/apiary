package workflow

import (
	"context"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// These tests pin the fail-loud contract from #180: a condition expression
// that cannot be parsed or evaluated fails the step (and the instance), it is
// never silently coerced to false → skip.

func TestDAG_ConditionEvalErrorFailsStep(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "cond-err-wf", Steps: []config.StepConfig{
		{ID: "classify", Agent: "architect"},
		{
			ID: "implement", Agent: "backend-dev", DependsOn: []string{"classify"},
			// C-style && does not parse — the exact slip from #180.
			Condition: `memory.track != "100x" && memory.track != "decomposed"`,
		},
	}}

	_, success, err := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if success {
		t.Fatal("expected instance failure (condition eval error must fail the step, not skip it)")
	}
	if contains(executedIDs(exec.seen), "implement") {
		t.Errorf("implement must not run when its condition cannot be evaluated, got %v", executedIDs(exec.seen))
	}
}

// TestDAG_ConditionEvalErrorTriggersOnFail verifies the eval error is routed
// through the normal failure path, so step-level on_fail.goto applies.
func TestDAG_ConditionEvalErrorTriggersOnFail(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "cond-err-onfail-wf", Steps: []config.StepConfig{
		{ID: "classify", Agent: "architect"},
		{
			ID: "implement", Agent: "backend-dev", DependsOn: []string{"classify"},
			Condition: `memory.track != "100x" && memory.track != "decomposed"`,
			OnFail:    &config.StepOutcome{Goto: "classify", MaxRetries: 1},
		},
	}}

	_, success, err := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if success {
		t.Fatal("expected instance failure after on_fail retries are exhausted")
	}
	// classify runs once, then re-runs once via on_fail.goto before the retry
	// budget is exhausted by the deterministic eval error.
	runs := 0
	for _, id := range executedIDs(exec.seen) {
		if id == "classify" {
			runs++
		}
	}
	if runs != 2 {
		t.Errorf("expected classify to run twice (initial + on_fail loop), ran %d times", runs)
	}
}

func TestDAG_FailWhenEvalErrorFailsStep(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"review": {Success: true},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "failwhen-err-wf", Steps: []config.StepConfig{
		{
			ID: "review", Agent: "architect",
			FailWhen: `memory.verdict == "rejected" || memory.verdict == "needs_work"`,
		},
	}}

	_, success, err := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if success {
		t.Fatal("expected instance failure (fail_when eval error must fail the step, not pass it)")
	}
}

func TestDAG_SplitBranchEvalErrorFailsSplit(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "split-err-wf", Steps: []config.StepConfig{
		{ID: "plan", Agent: "architect"},
		{ID: "route", Type: config.StepTypeSplit, DependsOn: []string{"plan"}, Branches: []config.SplitBranch{
			{If: `memory.level == "high" && memory.size == "xl"`, Goto: "senior"},
			{Else: true, Goto: "junior"},
		}},
		{ID: "senior", Agent: "architect"},
		{ID: "junior", Agent: "backend-dev"},
	}}

	_, success, err := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if success {
		t.Fatal("expected instance failure (split branch eval error must fail the split, not route as no-match)")
	}
	ids := executedIDs(exec.seen)
	if contains(ids, "senior") || contains(ids, "junior") {
		t.Errorf("no branch target may run when a branch condition cannot be evaluated, got %v", ids)
	}
}
