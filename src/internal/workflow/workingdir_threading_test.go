package workflow

import (
	"context"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// TestEngine_WorkflowWorkingDirThreadedToStep verifies the engine hands the
// workflow's working_dir to the executor via StepRequest.WorkflowWorkingDir, and
// that the step's own working_dir rides along on the StepConfig. Resolving the
// two against the agent/runner/config layers is the daemon executor's job
// (config.ResolveWorkingDir); here we assert both reach it unchanged (#436).
func TestEngine_WorkflowWorkingDirThreadedToStep(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{"run": {Success: true}}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{
		ID:         "release",
		WorkingDir: "/wf/dir",
		Steps:      []config.StepConfig{{ID: "run", Agent: "backend-dev", WorkingDir: "/step/dir"}},
	}

	if _, _, err := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "C1"}); err != nil {
		t.Fatalf("RunInstance: %v", err)
	}

	if len(exec.seen) != 1 {
		t.Fatalf("expected 1 step request, got %d", len(exec.seen))
	}
	req := exec.seen[0]
	if req.WorkflowWorkingDir != "/wf/dir" {
		t.Errorf("WorkflowWorkingDir = %q, want /wf/dir", req.WorkflowWorkingDir)
	}
	if req.Step.WorkingDir != "/step/dir" {
		t.Errorf("Step.WorkingDir = %q, want /step/dir", req.Step.WorkingDir)
	}
}
