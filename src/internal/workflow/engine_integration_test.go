package workflow

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// TestEngine_PersistsToRealSQLite runs the engine against a real db.Client (not a
// fake store) and verifies the workflow instance and step run land in SQLite with
// the expected state — the Phase 2 integration checkpoint.
func TestEngine_PersistsToRealSQLite(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "wf.db")
	client, err := db.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer client.Close()

	cfg := &config.Config{
		Agents:   []config.AgentConfig{{ID: "backend-dev", Model: "claude-sonnet-4-6"}},
		Settings: config.Settings{StateLock: true, ResultComment: false},
	}
	exec := &fakeExecutor{results: map[string]StepResult{
		"run": {Success: true, Output: "completed the task",
			StructuredOutput: map[string]any{"status": "ok"}, Summary: "did it"},
	}}

	// Real db.Client satisfies the Store interface.
	var store Store = client
	eng := NewEngine(cfg, store, exec)

	wf := synthWF(config.RouteConfig{
		ID: "backend-bugs", Agent: "backend-dev",
		OnComplete: config.OnComplete{SetState: "in_review"},
	})

	instID, success, err := eng.RunInstance(ctx, wf, model.InternalTask{ID: "PLANE-1", Title: "Fix it", Metadata: model.TaskMetadata{Source: "main-plane"}})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("expected success")
	}

	// Instance persisted and marked done.
	inst, err := client.GetWorkflowInstance(ctx, instID)
	if err != nil || inst == nil {
		t.Fatalf("get instance: %v (inst=%v)", err, inst)
	}
	if inst.State != db.InstanceStateDone {
		t.Errorf("instance state = %q, want done", inst.State)
	}
	if inst.WorkflowID != "backend-bugs" || inst.CellID != "PLANE-1" || inst.SourceID != "main-plane" {
		t.Errorf("instance fields wrong: %+v", inst)
	}
	if inst.TaskID != "PLANE-1" {
		t.Errorf("instance task_id = %q, want PLANE-1", inst.TaskID)
	}

	// Step run persisted with structured output + summary.
	runs, err := client.ListStepRuns(ctx, instID)
	if err != nil {
		t.Fatalf("list step runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 step run, got %d", len(runs))
	}
	sr := runs[0]
	if sr.State != db.StepStatePassed {
		t.Errorf("step state = %q, want passed", sr.State)
	}
	if sr.Output != "completed the task" {
		t.Errorf("step output = %q", sr.Output)
	}
	if sr.StructuredOutput != `{"status":"ok"}` {
		t.Errorf("structured output = %q", sr.StructuredOutput)
	}
	if sr.Summary != "did it" {
		t.Errorf("summary = %q", sr.Summary)
	}
	if sr.StartedAt == nil || sr.FinishedAt == nil {
		t.Error("expected started_at and finished_at to be set")
	}
}

// TestEngine_FailedStepPersistsFailedState verifies a failed run is recorded
// with a failed instance and step in real SQLite.
func TestEngine_FailedStepPersistsFailedState(t *testing.T) {
	ctx := context.Background()
	client, err := db.New(ctx, filepath.Join(t.TempDir(), "wf.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer client.Close()

	cfg := &config.Config{Agents: []config.AgentConfig{{ID: "a", Model: "m"}}}
	exec := &fakeExecutor{results: map[string]StepResult{"run": {Success: false, Output: "boom"}}}
	eng := NewEngine(cfg, client, exec)

	wf := synthWF(config.RouteConfig{ID: "r", Agent: "a"})
	instID, success, _ := eng.RunInstance(ctx, wf, model.InternalTask{ID: "C1"})

	if success {
		t.Error("expected failure")
	}
	inst, _ := client.GetWorkflowInstance(ctx, instID)
	if inst.State != db.InstanceStateFailed {
		t.Errorf("instance state = %q, want failed", inst.State)
	}
	runs, _ := client.ListStepRuns(ctx, instID)
	if len(runs) != 1 || runs[0].State != db.StepStateFailed {
		t.Errorf("expected 1 failed step run, got %+v", runs)
	}
}
