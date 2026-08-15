package workflow

import (
	"context"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// These tests pin #421: on_missing_output was only enforced on the scheduler
// loop, so children of a parallel (or foreach) group skipped it entirely — a
// child that never emitted APIARY_OUTPUT was recorded as passed with a NULL
// structured output and nothing was logged, and the group's reject_when then
// compared an unset memory key against its failure value and read false.

// groupChild builds one child of the validate group from the issue.
func groupChild(id, agent, key, onMissing string) config.StepConfig {
	return config.StepConfig{
		ID: id, Agent: agent,
		OnMissingOutput: onMissing,
		OutputSchema: &config.OutputSchema{
			Type:       "object",
			Properties: map[string]config.SchemaField{key: {Type: "string"}},
			Required:   []string{key},
		},
		Memory: &config.MemoryConfig{Write: []string{key}},
	}
}

// missingOutputGroupWF is the shape from the issue: a parallel group whose children
// each own a verdict key, joined with "all" and gated by reject_when.
func missingOutputGroupWF(onMissing string) config.WorkflowConfig {
	return config.WorkflowConfig{ID: "validate-wf", Steps: []config.StepConfig{
		{ID: "plan", Agent: "architect"},
		{
			ID: "validate", Type: config.StepTypeParallel, DependsOn: []string{"plan"},
			SubSteps: []config.StepConfig{
				groupChild("review", "reviewer", "verdict", onMissing),
				groupChild("qa_validate", "qa", "qa_verdict", onMissing),
			},
			Join:     config.JoinAll,
			FailWhen: `memory.verdict == "rejected" or memory.qa_verdict == "fail"`,
		},
	}}
}

// TestParallelChild_MissingOutputFailFailsTheJoin is the core regression: the
// qa child ends early with no APIARY_OUTPUT, so on_missing_output: fail must
// fail the child, and the "all" join must fail the group with it.
func TestParallelChild_MissingOutputFailFailsTheJoin(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"review":      {Success: true, StructuredOutput: map[string]any{"verdict": "approved"}},
		"qa_validate": {Success: true, Output: "Still waiting on CI"},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	instID, success, err := eng.RunInstance(context.Background(),
		missingOutputGroupWF(config.OnMissingOutputFail), model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if success {
		t.Fatal("expected failure: a group child with on_missing_output: fail emitted no structured output")
	}
	if store.instances[instID].State != db.InstanceStateFailed {
		t.Errorf("instance state = %q, want failed", store.instances[instID].State)
	}
	if states := store.stepRunStates("qa_validate"); len(states) != 1 || states[0] != db.StepStateFailed {
		t.Errorf("step_runs state for qa_validate = %v, want [failed]", states)
	}
}

// TestParallelChild_MissingOutputWarnIsRecorded pins the "at minimum it must be
// diagnosable" half: under the warn default the child still passes, but the
// missing output is logged and recorded as an event against the child's id.
func TestParallelChild_MissingOutputWarnIsRecorded(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"review":      {Success: true, StructuredOutput: map[string]any{"verdict": "approved"}},
		"qa_validate": {Success: true, StructuredOutput: map[string]any{"qa_verdict": "pass"}},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	// Only the review child goes missing this time.
	exec.results["review"] = StepResult{Success: true}

	if _, _, err := eng.RunInstance(context.Background(),
		missingOutputGroupWF(config.OnMissingOutputWarn), model.InternalTask{ID: "c1"}); err != nil {
		t.Fatalf("RunInstance: %v", err)
	}

	evs := store.eventsOfType("step.missing_output")
	if len(evs) != 1 {
		t.Fatalf("step.missing_output events = %d, want 1", len(evs))
	}
	if evs[0].StepID != "review" {
		t.Errorf("event step_id = %q, want review", evs[0].StepID)
	}
	if got := evs[0].Metadata["policy"]; got != config.OnMissingOutputWarn {
		t.Errorf("event policy = %v, want warn", got)
	}
}

// TestParallelChild_MissingOutputIgnoreOptsOut keeps the documented escape
// hatch working for children too.
func TestParallelChild_MissingOutputIgnoreOptsOut(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := missingOutputGroupWF(config.OnMissingOutputIgnore)
	wf.Steps[1].FailWhen = "" // the gate's own fail-closed check is #390's contract

	_, success, err := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("on_missing_output: ignore must opt a group child out of the check")
	}
	if evs := store.eventsOfType("step.missing_output"); len(evs) != 0 {
		t.Errorf("ignore must record no event, got %d", len(evs))
	}
}

// TestParallelChildren_BothEmitOutputPasses is a control: with both verdicts
// emitted the group joins and the gate reads false.
func TestParallelChildren_BothEmitOutputPasses(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"review":      {Success: true, StructuredOutput: map[string]any{"verdict": "approved"}},
		"qa_validate": {Success: true, StructuredOutput: map[string]any{"qa_verdict": "pass"}},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	_, success, err := eng.RunInstance(context.Background(),
		missingOutputGroupWF(config.OnMissingOutputFail), model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("both children emitted their verdicts: the group must pass")
	}
	if evs := store.eventsOfType("step.missing_output"); len(evs) != 0 {
		t.Errorf("no child is missing output, got %d events", len(evs))
	}
}

// TestForeachItem_MissingOutputFailFailsTheItem covers the other group kind:
// foreach items run off the scheduler loop for the same reason.
func TestForeachItem_MissingOutputFailFailsTheItem(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := newSeqExecutor()
	exec.scripts["plan"] = []StepResult{planWithIssues(2)}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	inner := groupChild("", "backend-dev", "fixed", config.OnMissingOutputFail)
	inner.Prompt = "Fix {{ issue.file }}"
	wf := foreachWorkflow(config.StepConfig{As: "issue", Step: &inner})

	_, success, err := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if success {
		t.Fatal("expected failure: every foreach item emitted no structured output")
	}
	if evs := store.eventsOfType("step.missing_output"); len(evs) != 2 {
		t.Errorf("step.missing_output events = %d, want 2 (one per item)", len(evs))
	}
}
