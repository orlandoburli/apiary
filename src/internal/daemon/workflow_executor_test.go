package daemon

import (
	"context"
	"errors"
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
		Cell:       model.SourceItem{ID: "c1", Title: "Fix bug", Number: "#42"},
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

// TestWfStepExecutor_PublishPropagation verifies the executor forwards the
// runner's APIARY_PUBLISH payload to the engine by default (publish: auto) and
// clears it when the step sets publish: off, before the engine ever sees it
// (6.1.2, 6.4.2).
func TestWfStepExecutor_PublishPropagation(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	runner := &fakeRunner{result: model.RunResult{
		Success:        true,
		Output:         "done",
		PublishPayload: "## Result\nshipped",
	}}
	d := &Dispatcher{
		cfg:         &config.Config{},
		db:          dbc,
		runners:     map[string]runnerpkg.Runner{"agent-architect": runner},
		agentRunner: map[string]string{"architect": "claude"},
	}
	x := &wfStepExecutor{d: d}

	// Default (publish unset == auto): payload propagates.
	auto := x.ExecuteStep(ctx, workflow.StepRequest{
		InstanceID: "wf_1",
		Cell:       model.SourceItem{ID: "c1"},
		Step:       config.StepConfig{ID: "plan", Agent: "architect"},
	})
	if auto.PublishPayload != "## Result\nshipped" {
		t.Errorf("auto: payload = %q, want it propagated", auto.PublishPayload)
	}

	// publish: off clears the payload before the engine sees it.
	off := x.ExecuteStep(ctx, workflow.StepRequest{
		InstanceID: "wf_2",
		Cell:       model.SourceItem{ID: "c1"},
		Step:       config.StepConfig{ID: "plan", Agent: "architect", Publish: config.PublishOff},
	})
	if off.PublishPayload != "" {
		t.Errorf("publish off: payload = %q, want cleared", off.PublishPayload)
	}
}

// TestWfStepExecutor_SpawnPropagationAndError verifies the executor forwards a
// parsed APIARY_SPAWN request to the engine, and turns a malformed block
// (RunResult.SpawnError) into a failed step (7.2.1, 7.3.3).
func TestWfStepExecutor_SpawnPropagationAndError(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	newExec := func(res model.RunResult) *wfStepExecutor {
		d := &Dispatcher{
			cfg:         &config.Config{},
			db:          dbc,
			runners:     map[string]runnerpkg.Runner{"agent-architect": &fakeRunner{result: res}},
			agentRunner: map[string]string{"architect": "claude"},
		}
		return &wfStepExecutor{d: d}
	}
	step := config.StepConfig{ID: "plan", Agent: "architect"}

	// Valid spawn request propagates to the engine.
	okRes := newExec(model.RunResult{Success: true, SpawnRequest: &model.SpawnRequest{WorkflowID: "collect"}}).
		ExecuteStep(ctx, workflow.StepRequest{InstanceID: "wf_1", Cell: model.SourceItem{ID: "c1"}, Step: step})
	if !okRes.Success {
		t.Fatalf("expected success, got %+v", okRes)
	}
	if okRes.SpawnRequest == nil || okRes.SpawnRequest.WorkflowID != "collect" {
		t.Errorf("spawn request not propagated: %+v", okRes.SpawnRequest)
	}

	// Malformed spawn block → failed step with the error surfaced.
	badRes := newExec(model.RunResult{Success: true, SpawnError: errors.New("invalid JSON")}).
		ExecuteStep(ctx, workflow.StepRequest{InstanceID: "wf_2", Cell: model.SourceItem{ID: "c1"}, Step: step})
	if badRes.Success {
		t.Error("expected failed step for malformed spawn block")
	}
	if badRes.Err == nil {
		t.Error("expected spawn error surfaced on the step result")
	}
	if badRes.SpawnRequest != nil {
		t.Errorf("malformed spawn should yield no request, got %+v", badRes.SpawnRequest)
	}
}
