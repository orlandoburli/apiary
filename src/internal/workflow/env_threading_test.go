package workflow

import (
	"context"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// TestEngine_WorkflowEnvThreadedToStep verifies that a workflow's Env is passed
// to the step executor via StepRequest.WorkflowEnv, and that the step's own Env
// rides along on the StepConfig. The daemon executor (stepEnv) is responsible for
// merging them; here we assert the engine threads both through unchanged.
func TestEngine_WorkflowEnvThreadedToStep(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{"run": {Success: true}}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{
		ID:    "release",
		Steps: []config.StepConfig{{ID: "run", Agent: "backend-dev", Env: map[string]string{"STEP_KEY": "sv"}}},
		Env:   map[string]string{"WF_KEY": "wv"},
	}

	if _, _, err := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "C1"}); err != nil {
		t.Fatalf("RunInstance: %v", err)
	}

	if len(exec.seen) != 1 {
		t.Fatalf("expected 1 step request, got %d", len(exec.seen))
	}
	req := exec.seen[0]
	if req.WorkflowEnv["WF_KEY"] != "wv" {
		t.Errorf("WorkflowEnv[WF_KEY] = %q, want wv", req.WorkflowEnv["WF_KEY"])
	}
	if req.Step.Env["STEP_KEY"] != "sv" {
		t.Errorf("Step.Env[STEP_KEY] = %q, want sv", req.Step.Env["STEP_KEY"])
	}
}
