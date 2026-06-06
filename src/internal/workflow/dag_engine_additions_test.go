package workflow

import (
	"context"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// TestDAG_ConditionSkipsStep verifies that a step whose condition evaluates to
// false is skipped and its dependents cascade.
func TestDAG_ConditionSkipsStep(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"classify": {Success: true, StructuredOutput: map[string]any{"track": "complex"},
			Summary: "complex issue"},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	// classify writes "track" to memory; implement has condition that is false for "complex".
	wf := config.WorkflowConfig{ID: "cond-wf", Steps: []config.StepConfig{
		{
			ID: "classify", Agent: "architect",
			OutputSchema: &config.OutputSchema{Type: "object",
				Properties: map[string]config.SchemaField{"track": {Type: "string"}}},
			Memory: &config.MemoryConfig{Write: []string{"track"}},
		},
		{
			ID: "implement", Agent: "backend-dev", DependsOn: []string{"classify"},
			Condition: `memory.track == "implement"`,
		},
		// downstream of implement should also be skipped
		{ID: "deploy", Agent: "architect", DependsOn: []string{"implement"}},
	}}

	instID, success, err := eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("expected success (skipped steps don't fail the workflow)")
	}
	_ = instID

	ids := executedIDs(exec.seen)
	if !contains(ids, "classify") {
		t.Errorf("expected classify to run, got %v", ids)
	}
	if contains(ids, "implement") {
		t.Errorf("implement should be skipped (condition false), got %v", ids)
	}
	if contains(ids, "deploy") {
		t.Errorf("deploy should cascade-skip after implement skipped, got %v", ids)
	}
}

// TestDAG_CondSkip_SeqSuccessorStillRuns verifies the v2 seq edge case: when a
// step is condition-skipped but its successor is linked via SeqDependsOn (the
// implicit dep added by the lowering pass), the successor still runs.
func TestDAG_CondSkip_SeqSuccessorStillRuns(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	// Simulate a v2-lowered workflow: classify → implement (cond) → qa
	// implement is condition-false (never runs); qa uses SeqDependsOn so it
	// is not blocked by the cond-skipped implement.
	wf := config.WorkflowConfig{ID: "v2-seq", Steps: []config.StepConfig{
		{ID: "classify", Agent: "architect"},
		{
			ID: "implement", Agent: "backend-dev",
			SeqDependsOn: []string{"classify"},
			Condition:    `memory.track == "implement"`, // always false (nothing writes track)
		},
		{
			ID: "qa", Agent: "architect",
			SeqDependsOn: []string{"implement"},
		},
	}}

	_, success, err := eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("expected success (cond-skipped seq step does not fail the workflow)")
	}
	ids := executedIDs(exec.seen)
	if !contains(ids, "classify") {
		t.Errorf("classify should run, got %v", ids)
	}
	if contains(ids, "implement") {
		t.Errorf("implement should be skipped (condition false), got %v", ids)
	}
	if !contains(ids, "qa") {
		t.Errorf("qa should run after cond-skipped implement (seq dep), got %v", ids)
	}
}

// TestDAG_ConditionTrueRunsStep verifies that a step whose condition evaluates
// true runs normally.
func TestDAG_ConditionTrueRunsStep(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"classify": {Success: true, StructuredOutput: map[string]any{"track": "implement"}},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "cond-wf", Steps: []config.StepConfig{
		{
			ID: "classify", Agent: "architect",
			OutputSchema: &config.OutputSchema{Type: "object",
				Properties: map[string]config.SchemaField{"track": {Type: "string"}}},
			Memory: &config.MemoryConfig{Write: []string{"track"}},
		},
		{
			ID: "implement", Agent: "backend-dev", DependsOn: []string{"classify"},
			Condition: `memory.track == "implement"`,
		},
	}}

	_, success, _ := eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "c1"})
	if !success {
		t.Fatal("expected success")
	}
	if !contains(executedIDs(exec.seen), "implement") {
		t.Errorf("implement should run when condition is true, got %v", executedIDs(exec.seen))
	}
}

// TestDAG_FailWhenRejectsStep verifies that fail_when = true turns a
// successful agent run into a rejection (failure eligible for loop-back).
func TestDAG_FailWhenRejectsStep(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := newSeqExecutor()
	// review: first call → agent success but verdict=rejected; second → approved.
	exec.scripts["review"] = []StepResult{
		{Success: true, StructuredOutput: map[string]any{"verdict": "rejected"}, Output: "NACK"},
		{Success: true, StructuredOutput: map[string]any{"verdict": "approved"}, Output: "LGTM"},
	}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "fw-wf", Steps: []config.StepConfig{
		{ID: "implement", Agent: "backend-dev"},
		{
			ID: "review", Agent: "architect", DependsOn: []string{"implement"},
			OutputSchema: &config.OutputSchema{Type: "object",
				Properties: map[string]config.SchemaField{"verdict": {Type: "string"}}},
			Memory:   &config.MemoryConfig{Write: []string{"verdict"}},
			FailWhen: `memory.verdict == "rejected"`,
			OnFail:   &config.StepOutcome{Goto: "implement", MaxRetries: 2},
		},
	}}

	_, success, _ := eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "c1"})
	if !success {
		t.Fatal("expected success after retry")
	}
	if exec.ran("review") != 2 {
		t.Errorf("expected review to run twice (reject + approve), got %d", exec.ran("review"))
	}
	if exec.ran("implement") != 2 {
		t.Errorf("expected implement to run twice (loop-back), got %d", exec.ran("implement"))
	}
}

// TestDAG_FailWhenFalseDoesNotReject verifies that when fail_when evaluates
// false, a successful agent run passes normally.
func TestDAG_FailWhenFalseDoesNotReject(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"review": {Success: true, StructuredOutput: map[string]any{"verdict": "approved"}},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "fw-wf", Steps: []config.StepConfig{
		{
			ID: "review", Agent: "architect",
			OutputSchema: &config.OutputSchema{Type: "object",
				Properties: map[string]config.SchemaField{"verdict": {Type: "string"}}},
			FailWhen: `memory.verdict == "rejected"`,
		},
	}}

	_, success, _ := eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "c1"})
	if !success {
		t.Fatal("expected success when fail_when is false")
	}
	if countSeen(exec.seen, "review") != 1 {
		t.Errorf("expected review to run once, got %d", countSeen(exec.seen, "review"))
	}
}

// TestDAG_FailWhenExhaustsRetries verifies that fail_when + retry exhaustion
// marks the instance as failed after max_retries.
func TestDAG_FailWhenExhaustsRetries(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"review": {Success: true, StructuredOutput: map[string]any{"verdict": "rejected"}},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "fw-wf", Steps: []config.StepConfig{
		{ID: "implement", Agent: "backend-dev"},
		{
			ID: "review", Agent: "architect", DependsOn: []string{"implement"},
			OutputSchema: &config.OutputSchema{Type: "object",
				Properties: map[string]config.SchemaField{"verdict": {Type: "string"}}},
			FailWhen: `memory.verdict == "rejected"`,
			OnFail:   &config.StepOutcome{Goto: "implement", MaxRetries: 1},
		},
	}}

	instID, success, _ := eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "c1"})
	if success {
		t.Fatal("expected failure after exhausted retries")
	}
	if store.instances[instID].State != "failed" {
		t.Errorf("instance state = %q, want failed", store.instances[instID].State)
	}
	// review: attempt 1 (reject, retry) + attempt 2 (reject, exhausted) = 2
	if countSeen(exec.seen, "review") != 2 {
		t.Errorf("expected review to run twice, got %d", countSeen(exec.seen, "review"))
	}
}

// TestDAG_OnMissingOutputFail verifies that a step with output_schema and
// on_missing_output=fail fails when no structured output is produced.
func TestDAG_OnMissingOutputFail(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	// Agent succeeds (exit 0) but produces no structured output.
	exec := &fakeExecutor{results: map[string]StepResult{
		"classify": {Success: true, Output: "done but no JSON"},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "omf-wf", Steps: []config.StepConfig{
		{
			ID: "classify", Agent: "architect",
			OutputSchema:    &config.OutputSchema{Type: "object"},
			OnMissingOutput: config.OnMissingOutputFail,
		},
	}}

	instID, success, _ := eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "c1"})
	if success {
		t.Fatal("expected failure when structured output is missing with on_missing_output=fail")
	}
	if store.instances[instID].State != "failed" {
		t.Errorf("instance state = %q, want failed", store.instances[instID].State)
	}
}

// TestDAG_OnMissingOutputWarnDoesNotFail verifies that warn (default) does not
// fail the step when structured output is absent.
func TestDAG_OnMissingOutputWarnDoesNotFail(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"classify": {Success: true, Output: "done but no JSON"},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "omw-wf", Steps: []config.StepConfig{
		{
			ID: "classify", Agent: "architect",
			OutputSchema:    &config.OutputSchema{Type: "object"},
			OnMissingOutput: config.OnMissingOutputWarn,
		},
	}}

	_, success, _ := eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "c1"})
	if !success {
		t.Fatal("expected success when on_missing_output=warn and output is absent")
	}
}

// TestStripExprDelimiters verifies the ${{ }} wrapper stripping.
func TestStripExprDelimiters(t *testing.T) {
	cases := []struct{ in, want string }{
		{`${{ memory.track == "implement" }}`, `memory.track == "implement"`},
		{`memory.track == "implement"`, `memory.track == "implement"`},
		{`${{verdict == "rejected"}}`, `verdict == "rejected"`},
		{`  ${{ cell.priority == "high" }}  `, `cell.priority == "high"`},
	}
	for _, c := range cases {
		got := stripExprDelimiters(c.in)
		if got != c.want {
			t.Errorf("stripExprDelimiters(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
