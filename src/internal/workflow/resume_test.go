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

	success, err := eng.ResumeInstance(context.Background(), instID, linearWF(), model.InternalTask{ID: "c1"}, prior)
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
	if store.instances[instID].State != db.InstanceStateDone {
		t.Errorf("expected final state done, got %s", store.instances[instID].State)
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

	if _, err := eng.ResumeInstance(context.Background(), instID, linearWF(), model.InternalTask{ID: "c1"}, prior); err != nil {
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

	if _, err := eng.ResumeInstance(context.Background(), instID, linearWF(), model.InternalTask{ID: "c1"}, prior); err != nil {
		t.Fatalf("ResumeInstance: %v", err)
	}
	if sr := store.stepRuns["sr-plan"]; sr == nil || !sr.SkippedCached {
		t.Errorf("expected sr-plan to be marked skipped_cached, got %+v", sr)
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

	success, err := eng.ResumeInstance(context.Background(), instID, wf, model.InternalTask{ID: "c1"}, prior)
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
