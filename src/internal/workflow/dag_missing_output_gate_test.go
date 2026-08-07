package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// These tests pin the fail-closed contract from #390: a reject_when/fail_when
// gate that reads memory keys its own step declared in memory.write but never
// emitted cannot be evaluated, and an unevaluable gate must never read as a
// pass. The reported incident: the reviewer agent said "rejected" in prose,
// omitted its APIARY_OUTPUT line, `memory.verdict` stayed unset, the gate
// compared "" against "rejected" → false, and the step was recorded as passed.

// reviewStep builds the step shape from the issue: an output schema with a
// verdict, memory.write of that verdict, and a gate reading it.
func reviewStep(onMissing string) config.StepConfig {
	return config.StepConfig{
		ID: "review", Agent: "architect", DependsOn: []string{"implement"},
		OutputSchema: &config.OutputSchema{
			Type: "object",
			Properties: map[string]config.SchemaField{
				"verdict":  {Type: "string", Enum: []string{"approved", "rejected"}},
				"feedback": {Type: "string"},
			},
			Required: []string{"verdict"},
		},
		Memory:          &config.MemoryConfig{Write: []string{"verdict", "feedback"}},
		FailWhen:        `memory.verdict == "rejected"`,
		OnMissingOutput: onMissing,
	}
}

func reviewWF(onMissing string, extra ...config.StepConfig) config.WorkflowConfig {
	steps := []config.StepConfig{
		{ID: "implement", Agent: "backend-dev"},
		reviewStep(onMissing),
	}
	return config.WorkflowConfig{ID: "review-gate-wf", Steps: append(steps, extra...)}
}

// TestDAG_GateUnevaluableFailsClosed is the core regression: the agent exits 0
// with no structured output, so the gate's operand is unset. The step must fail
// rather than pass, and downstream steps must not run.
func TestDAG_GateUnevaluableFailsClosed(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"review": {Success: true, Output: "Verdict: rejected. Race on the shared map."},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := reviewWF("", config.StepConfig{ID: "qa", Agent: "backend-dev", DependsOn: []string{"review"}})

	instID, success, err := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if success {
		t.Fatal("expected instance failure: a gate whose operand is unset must not read as a pass")
	}
	if store.instances[instID].State != db.InstanceStateFailed {
		t.Errorf("instance state = %q, want failed", store.instances[instID].State)
	}
	if contains(executedIDs(exec.seen), "qa") {
		t.Errorf("downstream step must not run after an unevaluable gate, got %v", executedIDs(exec.seen))
	}
}

// TestDAG_GateUnevaluableMarksStepRunFailed pins the visibility half: the
// persisted step_runs row must read failed, not the `passed` the runner
// produced (the row in the issue said `passed` with a NULL structured_output).
func TestDAG_GateUnevaluableMarksStepRunFailed(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{"review": {Success: true}}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	if _, success, err := eng.RunInstance(context.Background(), reviewWF(""), model.InternalTask{ID: "c1"}); err != nil {
		t.Fatalf("RunInstance: %v", err)
	} else if success {
		t.Fatal("expected instance failure")
	}

	states := store.stepRunStates("review")
	if len(states) != 1 || states[0] != db.StepStateFailed {
		t.Errorf("step_runs state for review = %v, want [failed]", states)
	}
}

// TestDAG_GateUnevaluableRecordsEvent pins the operator-visible signal: an
// execution event (dashboard / event stream), not just an ERROR log line.
func TestDAG_GateUnevaluableRecordsEvent(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{"review": {Success: true}}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	if _, _, err := eng.RunInstance(context.Background(), reviewWF(""), model.InternalTask{ID: "c1"}); err != nil {
		t.Fatalf("RunInstance: %v", err)
	}

	evs := store.eventsOfType("step.gate_unevaluable")
	if len(evs) != 1 {
		t.Fatalf("step.gate_unevaluable events = %d, want 1", len(evs))
	}
	if evs[0].StepID != "review" {
		t.Errorf("event step_id = %q, want review", evs[0].StepID)
	}
	keys, _ := evs[0].Metadata["unset_keys"].([]string)
	if len(keys) != 1 || keys[0] != "verdict" {
		t.Errorf("event unset_keys = %v, want [verdict]", evs[0].Metadata["unset_keys"])
	}
}

// TestDAG_MissingOutputRecordsEvent covers the warn default with no gate at
// all: the step still passes (unchanged behaviour), but the missing output is
// now recorded on the instance instead of living only in the daemon log.
func TestDAG_MissingOutputRecordsEvent(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{"classify": {Success: true}}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "warn-wf", Steps: []config.StepConfig{{
		ID: "classify", Agent: "architect",
		OutputSchema: &config.OutputSchema{Type: "object",
			Properties: map[string]config.SchemaField{"track": {Type: "string"}}},
		Memory: &config.MemoryConfig{Write: []string{"track"}},
	}}}

	_, success, err := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("a missing output with no gate reading it still passes under the warn default")
	}
	evs := store.eventsOfType("step.missing_output")
	if len(evs) != 1 {
		t.Fatalf("step.missing_output events = %d, want 1", len(evs))
	}
	if got := evs[0].Metadata["policy"]; got != config.OnMissingOutputWarn {
		t.Errorf("event policy = %v, want warn", got)
	}
}

// TestDAG_GateUnevaluablePartialOutput covers the partial case: the agent
// emitted structured output, but not the key the gate reads.
func TestDAG_GateUnevaluablePartialOutput(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"review": {Success: true, StructuredOutput: map[string]any{"feedback": "looks risky"}},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	_, success, err := eng.RunInstance(context.Background(), reviewWF(""), model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if success {
		t.Fatal("expected failure: the gate's key is missing even though other keys were emitted")
	}
}

// TestDAG_GateUnevaluableRoutesThroughOnFail verifies the fail-closed failure
// takes the normal failure route, so the authored on_reject.restart_from
// (lowered to on_fail.goto) loops back instead of dead-ending.
func TestDAG_GateUnevaluableRoutesThroughOnFail(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{"review": {Success: true}}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	step := reviewStep("")
	step.OnFail = &config.StepOutcome{Goto: "implement", MaxRetries: 2}
	wf := config.WorkflowConfig{ID: "review-loop-wf", Steps: []config.StepConfig{
		{ID: "implement", Agent: "backend-dev"}, step,
	}}

	_, success, err := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if success {
		t.Fatal("expected instance failure after the retry budget is exhausted")
	}
	if got := countSeen(exec.seen, "implement"); got != 3 {
		t.Errorf("implement ran %d times, want 3 (initial + 2 loop-backs)", got)
	}
}

// TestDAG_GateUnevaluableIgnoreOptsOut verifies the explicit opt-out:
// on_missing_output: ignore means "I know this output may be absent" and keeps
// the old permissive behaviour.
func TestDAG_GateUnevaluableIgnoreOptsOut(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{"review": {Success: true}}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	_, success, err := eng.RunInstance(context.Background(),
		reviewWF(config.OnMissingOutputIgnore), model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("on_missing_output: ignore must opt out of the fail-closed gate check")
	}
}

// ── controls: these pass with and without the fix ───────────────────────────

// TestDAG_GateEvaluableApprovedPasses is a control: the agent emitted the
// verdict, the gate is evaluable and false, the step passes.
func TestDAG_GateEvaluableApprovedPasses(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"review": {Success: true, StructuredOutput: map[string]any{"verdict": "approved", "feedback": "ok"}},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	_, success, err := eng.RunInstance(context.Background(), reviewWF(""), model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("an evaluable gate that does not match must still pass")
	}
}

// TestDAG_GateEvaluableRejectedFails is a control: the ordinary rejection path.
func TestDAG_GateEvaluableRejectedFails(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"review": {Success: true, StructuredOutput: map[string]any{"verdict": "rejected"}},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	_, success, err := eng.RunInstance(context.Background(), reviewWF(""), model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if success {
		t.Fatal("a matching gate must fail the step")
	}
}

// TestDAG_GateOnForeignMemoryKeyUnaffected is a control that documents the
// deliberate scope: the check only covers keys the gating step itself declared
// in memory.write. A gate reading a key owned by another step is unchanged.
func TestDAG_GateOnForeignMemoryKeyUnaffected(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{"review": {Success: true}}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	step := reviewStep("")
	step.FailWhen = `memory.qa_verdict == "rejected"` // written by no step in this workflow
	step.Memory = &config.MemoryConfig{Write: []string{"verdict"}}
	step.OutputSchema.Required = nil
	wf := config.WorkflowConfig{ID: "foreign-key-wf", Steps: []config.StepConfig{
		{ID: "implement", Agent: "backend-dev"}, step,
	}}

	_, success, err := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("gates over keys this step does not own are out of scope for the fail-closed check")
	}
}

// TestExpr_MemoryRefs pins the accessor extraction the gate check relies on.
func TestExpr_MemoryRefs(t *testing.T) {
	e, err := ParseExpr(`memory.verdict == "rejected" or (not memory.done == "yes" and cell.priority == "P1")`)
	if err != nil {
		t.Fatalf("ParseExpr: %v", err)
	}
	if got := strings.Join(e.MemoryRefs(), ","); got != "done,verdict" {
		t.Errorf("MemoryRefs = %q, want \"done,verdict\"", got)
	}
}
