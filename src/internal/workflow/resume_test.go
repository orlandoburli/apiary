package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// linearWF is plan → implement → review, all agent steps.
func linearWF() config.WorkflowConfig {
	return config.WorkflowConfig{ID: "feature", Steps: []config.StepConfig{
		{ID: "plan", Agent: "architect",
			OutputSchema: &config.OutputSchema{Type: "object",
				Properties: map[string]config.SchemaField{"approach": {Type: "string"}}},
			Memory: &config.MemoryConfig{Write: []string{"approach"}}},
		{ID: "implement", Agent: "backend-dev", DependsOn: []string{"plan"}},
		{ID: "review", Agent: "architect", DependsOn: []string{"implement"}},
	}}
}

func TestResume_SkipsCachedAndContinues(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	instID := "wf_resume_1"
	store.CreateWorkflowInstance(context.Background(), &db.WorkflowInstance{ //nolint:errcheck
		ID: instID, WorkflowID: "feature", CellID: "c1", State: db.InstanceStateFailed})

	// plan passed earlier, implement failed; review never ran.
	prior := []db.StepRun{
		{ID: "sr-plan", WorkflowInstanceID: instID, StepID: "plan", State: db.StepStatePassed,
			StructuredOutput: `{"approach":"layered"}`, Summary: "chose layered"},
		{ID: "sr-impl", WorkflowInstanceID: instID, StepID: "implement", State: db.StepStateFailed},
	}

	newID, success, err := eng.ResumeInstance(context.Background(), store.instances[instID], linearWF(), model.InternalTask{ID: "c1"}, prior, "", "")
	if err != nil {
		t.Fatalf("ResumeInstance: %v", err)
	}
	if !success {
		t.Fatal("expected resume to succeed")
	}

	ids := executedIDs(exec.seen)
	if contains(ids, "plan") {
		t.Errorf("plan should be replayed from cache, not re-executed: %v", ids)
	}
	if !contains(ids, "implement") || !contains(ids, "review") {
		t.Errorf("expected implement and review to run, got %v", ids)
	}
	if store.instances[newID].State != db.InstanceStateDone {
		t.Errorf("expected descendant state done, got %s", store.instances[newID].State)
	}
	if store.instances[instID].State != db.InstanceStateFailed {
		t.Errorf("source instance was mutated: %s", store.instances[instID].State)
	}
}

func TestResume_CachedMemoryAvailableDownstream(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	instID := "wf_resume_2"
	prior := []db.StepRun{
		{ID: "sr-plan", WorkflowInstanceID: instID, StepID: "plan", State: db.StepStatePassed,
			StructuredOutput: `{"approach":"event-sourced"}`, Summary: "use events"},
		{ID: "sr-impl", WorkflowInstanceID: instID, StepID: "implement", State: db.StepStateFailed},
	}

	source := &db.WorkflowInstance{ID: instID, WorkflowID: "feature", CellID: "c1", State: db.InstanceStateFailed}
	if _, _, err := eng.ResumeInstance(context.Background(), source, linearWF(), model.InternalTask{ID: "c1"}, prior, "", ""); err != nil {
		t.Fatalf("ResumeInstance: %v", err)
	}

	// The implement step (first to re-run) must see the plan's cached memory.
	var implReq *StepRequest
	for i := range exec.seen {
		if exec.seen[i].Step.ID == "implement" {
			implReq = &exec.seen[i]
			break
		}
	}
	if implReq == nil {
		t.Fatal("implement step did not run")
	}
	if !strings.Contains(implReq.MemoryDoc, "event-sourced") {
		t.Errorf("cached plan memory missing from downstream memory doc:\n%s", implReq.MemoryDoc)
	}
}

func TestResume_MarksPriorStepCached(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	instID := "wf_resume_3"
	prior := []db.StepRun{
		{ID: "sr-plan", WorkflowInstanceID: instID, StepID: "plan", State: db.StepStatePassed,
			StructuredOutput: `{"approach":"layered"}`},
		{ID: "sr-impl", WorkflowInstanceID: instID, StepID: "implement", State: db.StepStateFailed},
	}
	for i := range prior {
		_ = store.CreateStepRun(context.Background(), &prior[i])
	}

	source := &db.WorkflowInstance{ID: instID, WorkflowID: "feature", CellID: "c1", State: db.InstanceStateFailed}
	newID, _, err := eng.ResumeInstance(context.Background(), source, linearWF(), model.InternalTask{ID: "c1"}, prior, "", "")
	if err != nil {
		t.Fatalf("ResumeInstance: %v", err)
	}
	if sr := store.stepRuns["sr-plan"]; sr == nil || sr.SkippedCached {
		t.Errorf("source step should remain unchanged, got %+v", sr)
	}
	var cached *db.StepRun
	for _, sr := range store.stepRuns {
		if sr.WorkflowInstanceID == newID && sr.StepID == "plan" {
			cached = sr
		}
	}
	if cached == nil || !cached.SkippedCached {
		t.Errorf("expected a cached descendant plan step, got %+v", cached)
	}
}

func TestResume_FromStepRerunsPassedStep(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{}
	eng := testEngine(cfg, store, exec, &fakeSide{})
	wf := linearWF()
	source := &db.WorkflowInstance{ID: "wf-source", WorkflowID: wf.ID, CellID: "c1", State: db.InstanceStateFailed}
	prior := []db.StepRun{
		{ID: "sr-plan", WorkflowInstanceID: source.ID, StepID: "plan", State: db.StepStatePassed},
		{ID: "sr-implement", WorkflowInstanceID: source.ID, StepID: "implement", State: db.StepStatePassed},
		{ID: "sr-review", WorkflowInstanceID: source.ID, StepID: "review", State: db.StepStateFailed},
	}
	newID, success, err := eng.ResumeInstance(context.Background(), source, wf, model.InternalTask{ID: "c1"}, prior, "implement", "")
	if err != nil || !success {
		t.Fatalf("resume: id=%s success=%v err=%v", newID, success, err)
	}
	ids := executedIDs(exec.seen)
	if contains(ids, "plan") {
		t.Errorf("plan should remain cached: %v", ids)
	}
	if !contains(ids, "implement") || !contains(ids, "review") {
		t.Errorf("implement and review should rerun: %v", ids)
	}
	if store.instances[newID].ResumedFrom != source.ID {
		t.Errorf("resumed_from = %q", store.instances[newID].ResumedFrom)
	}
}

func TestResume_ReevaluatesSplit(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		// plan re-runs? no — plan is cached. senior re-runs and now passes.
		"senior": {Success: true, Output: "ok"},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "split", Steps: []config.StepConfig{
		{ID: "plan", Agent: "architect",
			OutputSchema: &config.OutputSchema{Type: "object",
				Properties: map[string]config.SchemaField{"level": {Type: "string"}}},
			Memory: &config.MemoryConfig{Write: []string{"level"}}},
		{ID: "route", Type: config.StepTypeSplit, DependsOn: []string{"plan"}, Branches: []config.SplitBranch{
			{If: `memory.level == "high"`, Goto: "senior"},
			{Else: true, Goto: "junior"},
		}},
		{ID: "senior", Agent: "architect"},
		{ID: "junior", Agent: "backend-dev"},
	}}

	instID := "wf_resume_4"
	// plan passed (level=high), route passed, senior failed.
	prior := []db.StepRun{
		{ID: "sr-plan", WorkflowInstanceID: instID, StepID: "plan", State: db.StepStatePassed,
			StructuredOutput: `{"level":"high"}`},
		{ID: "sr-route", WorkflowInstanceID: instID, StepID: "route", State: db.StepStatePassed},
		{ID: "sr-senior", WorkflowInstanceID: instID, StepID: "senior", State: db.StepStateFailed},
	}

	source := &db.WorkflowInstance{ID: instID, WorkflowID: wf.ID, CellID: "c1", State: db.InstanceStateFailed}
	_, success, err := eng.ResumeInstance(context.Background(), source, wf, model.InternalTask{ID: "c1"}, prior, "", "")
	if err != nil {
		t.Fatalf("ResumeInstance: %v", err)
	}
	if !success {
		t.Fatal("expected resume to succeed")
	}

	ids := executedIDs(exec.seen)
	if !contains(ids, "senior") {
		t.Errorf("expected senior to re-run after split re-evaluation, got %v", ids)
	}
	if contains(ids, "junior") {
		t.Errorf("junior should not run — split routes to senior: %v", ids)
	}
	if contains(ids, "plan") {
		t.Errorf("plan should be cached, not re-run: %v", ids)
	}
}

// alreadyDoneWF mirrors the shape of project-erp's implementation workflow
// (issue #2967 loop): implement writes `action` to memory; check-ci is gated on
// action != already_done and close-noop on action == already_done.
func alreadyDoneWF() config.WorkflowConfig {
	actionSchema := &config.OutputSchema{Type: "object",
		Properties: map[string]config.SchemaField{
			"action": {Type: "string", Enum: []string{"pr_opened", "already_done"}}},
		Required: []string{"action"}}
	return config.WorkflowConfig{ID: "implementation", Steps: []config.StepConfig{
		{ID: "implement", Agent: "engineer",
			OutputSchema: actionSchema,
			Memory:       &config.MemoryConfig{Write: []string{"action"}}},
		{ID: "checkci", Agent: "engineer", DependsOn: []string{"implement"},
			Condition: `memory.action != "already_done"`},
		{ID: "closenoop", Agent: "engineer", DependsOn: []string{"implement"},
			Condition: `memory.action == "already_done"`},
	}}
}

// A step re-run by a restart_from/goto loop persists a second passed run. On
// resume/rehydrate the LATEST passed run's structured output must win —
// restoring the first run's memory made `if` conditions read the stale value,
// so an implement re-run that emitted already_done was invisible and the
// workflow looped instead of taking the noop exit (project-erp #2967).
func TestRestoreCachedSteps_LastPassedRunWins(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	instID := "wf_resume_lastwins"
	prior := []db.StepRun{
		{ID: "sr-impl-1", WorkflowInstanceID: instID, StepID: "implement",
			State: db.StepStatePassed, StructuredOutput: `{"action":"pr_opened"}`},
		{ID: "sr-ci-1", WorkflowInstanceID: instID, StepID: "checkci",
			State: db.StepStateFailed},
		{ID: "sr-impl-2", WorkflowInstanceID: instID, StepID: "implement",
			State: db.StepStatePassed, StructuredOutput: `{"action":"already_done"}`},
	}

	source := &db.WorkflowInstance{ID: instID, WorkflowID: "implementation", CellID: "c1", State: db.InstanceStateFailed}
	_, success, err := eng.ResumeInstance(context.Background(), source, alreadyDoneWF(), model.InternalTask{ID: "c1"}, prior, "", "")
	if err != nil {
		t.Fatalf("ResumeInstance: %v", err)
	}
	if !success {
		t.Fatal("expected resume to succeed")
	}

	ids := executedIDs(exec.seen)
	if contains(ids, "implement") {
		t.Errorf("implement is cached and must not re-run: %v", ids)
	}
	if contains(ids, "checkci") {
		t.Errorf("checkci must be condition-skipped — latest implement run said already_done: %v", ids)
	}
	if !contains(ids, "closenoop") {
		t.Errorf("closenoop must run so the noop exit closes the task: %v", ids)
	}
}
