package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// outcomesWorkflow is implement → gate → deploy, where the gate offers three
// answers: continue, loop back to implement, or fail.
func outcomesWorkflow() config.WorkflowConfig {
	return config.WorkflowConfig{ID: "feature", Steps: []config.StepConfig{
		{ID: "implement", Agent: "backend-dev"},
		{ID: "gate", Type: config.StepTypeApproval, DependsOn: []string{"implement"},
			Message: "How should this go?",
			ApprovalOutcomes: []config.ApprovalOutcome{
				{Value: "approve"},
				{Value: "ship-early"},
				{Value: "rework", Goto: "implement"},
				{Value: "abort", Fail: true},
			}},
		{ID: "deploy", Agent: "backend-dev", DependsOn: []string{"gate"}},
	}}
}

// A `fail` outcome ends the gate the way a rejection always has, so on_fail and
// on_reject keep working without the outcome system knowing about them.
func TestApproval_FailOutcomeAborts(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	eng := testEngine(baseCfg(), store, exec, &fakeSide{})

	instID, _, _ := eng.RunInstance(context.Background(), outcomesWorkflow(), model.InternalTask{ID: "c1"})
	success, err := eng.ResolveApprovalResponse(context.Background(), instID, db.ApprovalResponse{
		Decision: "approve", Outcome: "abort", Actor: "alice", Channel: "dashboard",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if success {
		t.Error("a fail outcome must not report success")
	}
	if store.instances[instID].State != db.InstanceStateFailed {
		t.Errorf("instance state = %q, want failed", store.instances[instID].State)
	}
	if contains(executedIDs(exec.seen), "deploy") {
		t.Error("deploy must not run after a fail outcome")
	}
}

// A `goto` outcome resets the target and everything downstream: a backward
// target re-enters the gate on a new lap, which is how "needs rework" loops.
func TestApproval_GotoOutcomeReentersTheGate(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	eng := testEngine(baseCfg(), store, exec, &fakeSide{})

	instID, _, _ := eng.RunInstance(context.Background(), outcomesWorkflow(), model.InternalTask{ID: "c1"})
	if _, err := eng.ResolveApprovalResponse(context.Background(), instID, db.ApprovalResponse{
		Decision: "approve", Outcome: "rework", Actor: "alice", Channel: "dashboard",
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	var implementRuns int
	for _, run := range executedIDs(exec.seen) {
		if run == "implement" {
			implementRuns++
		}
	}
	if implementRuns != 2 {
		t.Errorf("implement ran %d times, want 2 (once before the gate, once after rework)", implementRuns)
	}
	if len(eng.ParkedApprovals()) != 1 {
		t.Error("the workflow should be parked at the gate again after rework")
	}
	if contains(executedIDs(exec.seen), "deploy") {
		t.Error("deploy must not run before the reworked work is approved")
	}
}

// The chosen outcome is memory the rest of the workflow can branch on, which is
// what makes an outcome that declares no routing meaningful at all.
func TestApproval_OutcomeReachesNextStepMemory(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{}
	eng := testEngine(baseCfg(), store, exec, &fakeSide{})

	instID, _, _ := eng.RunInstance(context.Background(), outcomesWorkflow(), model.InternalTask{ID: "c1"})
	if _, err := eng.ResolveApprovalResponse(context.Background(), instID, db.ApprovalResponse{
		Decision: "approve", Outcome: "ship-early", Actor: "alice", Channel: "dashboard",
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	var prompt string
	for _, seen := range exec.seen {
		if seen.Step.ID == "deploy" {
			prompt = seen.MemoryDoc
		}
	}
	if !strings.Contains(prompt, "ship-early") {
		t.Fatalf("the chosen outcome is missing from the next step's memory:\n%s", prompt)
	}
}
