package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	runnerpkg "github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/workflow"
)

// fakeRunner returns a scripted result and records that it ran.
type fakeRunner struct {
	result model.RunResult
	ran    bool
}

func (f *fakeRunner) ID() string                       { return "fake" }
func (f *fakeRunner) Configure(_ map[string]any) error { return nil }
func (f *fakeRunner) Run(_ context.Context, _ model.RunRequest) (model.RunResult, error) {
	f.ran = true
	return f.result, nil
}

func TestWfStepExecutor_RecordsExecution(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	runner := &fakeRunner{result: model.RunResult{
		Success:  true,
		Output:   "done",
		Duration: 1200 * time.Millisecond,
		Usage:    &model.Usage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150, CostUSD: 0.02, NumTurns: 3},
	}}

	d := &Dispatcher{
		cfg:         &config.Config{},
		db:          dbc,
		runners:     map[string]runnerpkg.Runner{"agent-architect": runner},
		agentRunner: map[string]string{"architect": "claude"},
	}
	x := &wfStepExecutor{d: d}

	res := x.ExecuteStep(ctx, workflow.StepRequest{
		InstanceID: "wf_1",
		Cell:       model.Cell{ID: "c1", Title: "Fix bug", Number: "#42"},
		Step:       config.StepConfig{ID: "plan", Agent: "architect"},
		Model:      "claude-opus-4-8",
	})

	if !runner.ran {
		t.Fatal("runner was not invoked")
	}
	if !res.Success {
		t.Fatalf("expected success, got %+v", res)
	}

	// A terminal task_executions row must exist with the recorded usage.
	exec, err := dbc.GetLastExecution(ctx, "c1")
	if err != nil {
		t.Fatalf("GetLastExecution: %v", err)
	}
	if exec == nil {
		t.Fatal("no execution row written for the step")
	}
	if exec.Status != "success" {
		t.Errorf("status = %q, want success", exec.Status)
	}
	if exec.AgentID != "architect" {
		t.Errorf("agent = %q, want architect", exec.AgentID)
	}
	if exec.TotalTokens != 150 || exec.CostUSD != 0.02 {
		t.Errorf("usage not recorded: tokens=%d cost=%.4f", exec.TotalTokens, exec.CostUSD)
	}
	if exec.DurationMs != 1200 {
		t.Errorf("duration = %dms, want 1200", exec.DurationMs)
	}
}
