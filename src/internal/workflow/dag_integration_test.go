package workflow

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// realDB opens a temporary SQLite database for integration tests.
func realDB(t *testing.T) *db.Client {
	t.Helper()
	client, err := db.New(context.Background(), filepath.Join(t.TempDir(), "wf.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

// realEngine creates an Engine backed by a real SQLite client.
func realEngine(t *testing.T, client *db.Client, exec StepExecutor) *Engine {
	t.Helper()
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{ID: "agent-a", Model: "claude-sonnet-4-6"},
			{ID: "agent-b", Model: "claude-sonnet-4-6"},
			{ID: "agent-c", Model: "claude-sonnet-4-6"},
		},
	}
	return NewEngine(cfg, client, exec)
}

// stepIDs returns the step IDs from a list of step runs in order.
func stepIDs(runs []db.StepRun) []string {
	ids := make([]string, len(runs))
	for i, r := range runs {
		ids[i] = r.StepID
	}
	return ids
}

// TestDAGIntegration_SequentialMemory verifies that a 3-step sequential workflow
// completes with all step runs persisted and memory threaded to downstream steps.
func TestDAGIntegration_SequentialMemory(t *testing.T) {
	ctx := context.Background()
	client := realDB(t)

	exec := &fakeExecutor{results: map[string]StepResult{
		"classify": {
			Success:         true,
			Output:          "classified as high-priority",
			StructuredOutput: map[string]any{"track": "implement"},
			Summary:         "track decided",
		},
		"implement": {Success: true, Output: "PR opened"},
		"review":    {Success: true, Output: "LGTM"},
	}}
	eng := realEngine(t, client, exec)

	wf := config.WorkflowConfig{
		ID: "seq-wf",
		Steps: []config.StepConfig{
			{
				ID:    "classify",
				Agent: "agent-a",
				OutputSchema: &config.OutputSchema{
					Type: "object",
					Properties: map[string]config.SchemaField{
						"track": {Type: "string"},
					},
				},
				Memory: &config.MemoryConfig{Write: []string{"track"}},
			},
			{ID: "implement", Agent: "agent-b", DependsOn: []string{"classify"}},
			{ID: "review", Agent: "agent-c", DependsOn: []string{"implement"}},
		},
	}

	instID, success, err := eng.RunInstance(ctx, wf, model.SourceItem{ID: "T-1", Title: "Add feature"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("expected success")
	}

	// Instance persisted as done.
	inst, err := client.GetWorkflowInstance(ctx, instID)
	if err != nil || inst == nil {
		t.Fatalf("get instance: %v", err)
	}
	if inst.State != db.InstanceStateDone {
		t.Errorf("instance state = %q, want done", inst.State)
	}

	// All 3 step runs persisted and passed.
	runs, err := client.ListStepRuns(ctx, instID)
	if err != nil {
		t.Fatalf("list step runs: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 step runs, got %d: %v", len(runs), stepIDs(runs))
	}
	for _, r := range runs {
		if r.State != db.StepStatePassed {
			t.Errorf("step %q state = %q, want passed", r.StepID, r.State)
		}
	}

	// Memory from classify is threaded into the implement step's system prepend.
	var implementReq *StepRequest
	for i := range exec.seen {
		if exec.seen[i].Step.ID == "implement" {
			implementReq = &exec.seen[i]
			break
		}
	}
	if implementReq == nil {
		t.Fatal("implement step was not executed")
	}
	if !strings.Contains(implementReq.MemoryDoc, "track") {
		t.Errorf("implement step MemoryDoc missing memory from classify; got: %q", implementReq.MemoryDoc)
	}
}

// TestDAGIntegration_DiamondFanIn verifies that a diamond-shaped DAG (A→{B,C}→D)
// completes with all four steps persisted, A first and D last.
func TestDAGIntegration_DiamondFanIn(t *testing.T) {
	ctx := context.Background()
	client := realDB(t)
	exec := &fakeExecutor{}
	eng := realEngine(t, client, exec)

	wf := config.WorkflowConfig{
		ID: "diamond-wf",
		Steps: []config.StepConfig{
			{ID: "A", Agent: "agent-a"},
			{ID: "B", Agent: "agent-b", DependsOn: []string{"A"}},
			{ID: "C", Agent: "agent-c", DependsOn: []string{"A"}},
			{ID: "D", Agent: "agent-a", DependsOn: []string{"B", "C"}},
		},
	}

	instID, success, err := eng.RunInstance(ctx, wf, model.SourceItem{ID: "T-2"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("expected success")
	}

	// All 4 step runs persisted and passed.
	runs, err := client.ListStepRuns(ctx, instID)
	if err != nil {
		t.Fatalf("list step runs: %v", err)
	}
	if len(runs) != 4 {
		t.Fatalf("expected 4 step runs, got %d: %v", len(runs), stepIDs(runs))
	}
	for _, r := range runs {
		if r.State != db.StepStatePassed {
			t.Errorf("step %q state = %q, want passed", r.StepID, r.State)
		}
	}

	// A runs first, D runs last.
	ids := executedIDs(exec.seen)
	if ids[0] != "A" {
		t.Errorf("expected A first, got %v", ids)
	}
	if ids[len(ids)-1] != "D" {
		t.Errorf("expected D last, got %v", ids)
	}
	// B and C both ran (fan-in).
	if !contains(ids, "B") || !contains(ids, "C") {
		t.Errorf("expected both B and C to run, got %v", ids)
	}

	// D's start timestamp is after B and C finished.
	found := map[string]db.StepRun{}
	for _, r := range runs {
		found[r.StepID] = r
	}
	dRun, dOK := found["D"]
	bRun, bOK := found["B"]
	cRun, cOK := found["C"]
	if !dOK || !bOK || !cOK {
		t.Fatal("could not find all step runs for B, C, D")
	}
	if dRun.StartedAt != nil && bRun.FinishedAt != nil && dRun.StartedAt.Before(*bRun.FinishedAt) {
		t.Errorf("D started before B finished")
	}
	if dRun.StartedAt != nil && cRun.FinishedAt != nil && dRun.StartedAt.Before(*cRun.FinishedAt) {
		t.Errorf("D started before C finished")
	}
}

// TestDAGIntegration_SplitByMemoryField verifies that a split step routes to the
// correct branch based on a memory field written by the prior agent step.
func TestDAGIntegration_SplitByMemoryField(t *testing.T) {
	ctx := context.Background()
	client := realDB(t)

	exec := &fakeExecutor{results: map[string]StepResult{
		"plan": {
			Success:         true,
			StructuredOutput: map[string]any{"complexity": "high"},
		},
	}}
	eng := realEngine(t, client, exec)

	wf := config.WorkflowConfig{
		ID: "split-wf",
		Steps: []config.StepConfig{
			{
				ID:    "plan",
				Agent: "agent-a",
				OutputSchema: &config.OutputSchema{
					Type:       "object",
					Properties: map[string]config.SchemaField{"complexity": {Type: "string"}},
				},
				Memory: &config.MemoryConfig{Write: []string{"complexity"}},
			},
			{ID: "route", Type: config.StepTypeSplit, DependsOn: []string{"plan"}, Branches: []config.SplitBranch{
				{If: `memory.complexity == "high"`, Goto: "senior"},
				{Else: true, Goto: "junior"},
			}},
			{ID: "senior", Agent: "agent-b"},
			{ID: "junior", Agent: "agent-c"},
		},
	}

	instID, success, err := eng.RunInstance(ctx, wf, model.SourceItem{ID: "T-3"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("expected success")
	}

	runs, err := client.ListStepRuns(ctx, instID)
	if err != nil {
		t.Fatalf("list step runs: %v", err)
	}

	// plan + senior = 2 step runs (route is a split step with no StepRun); junior must not appear.
	stateByID := map[string]string{}
	for _, r := range runs {
		stateByID[r.StepID] = r.State
	}
	if stateByID["plan"] != db.StepStatePassed {
		t.Errorf("plan state = %q, want passed", stateByID["plan"])
	}
	if stateByID["senior"] != db.StepStatePassed {
		t.Errorf("senior state = %q, want passed", stateByID["senior"])
	}
	if _, ok := stateByID["junior"]; ok {
		t.Errorf("junior should not have a step run (skipped), but got state %q", stateByID["junior"])
	}
}

// TestDAGIntegration_OnFailLoopBackAndRetry verifies that on_fail.goto loops back
// to the target step, increments retries, and succeeds when the step eventually passes.
func TestDAGIntegration_OnFailLoopBackAndRetry(t *testing.T) {
	ctx := context.Background()
	client := realDB(t)

	exec := newSeqExecutor()
	exec.scripts["review"] = []StepResult{
		{Success: false, Output: "needs changes"},
		{Success: true, Output: "LGTM"},
	}
	eng := realEngine(t, client, exec)

	wf := config.WorkflowConfig{
		ID: "retry-wf",
		Steps: []config.StepConfig{
			{ID: "implement", Agent: "agent-a"},
			{ID: "review", Agent: "agent-b", DependsOn: []string{"implement"},
				OnFail: &config.StepOutcome{Goto: "implement", MaxRetries: 2}},
		},
	}

	instID, success, err := eng.RunInstance(ctx, wf, model.SourceItem{ID: "T-4"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("expected success after retry")
	}

	// Instance done.
	inst, err := client.GetWorkflowInstance(ctx, instID)
	if err != nil || inst.State != db.InstanceStateDone {
		t.Errorf("instance state = %q, want done", inst.State)
	}

	// implement ran twice (initial + loop-back), review ran twice (fail + pass).
	if exec.ran("implement") != 2 {
		t.Errorf("implement ran %d times, want 2", exec.ran("implement"))
	}
	if exec.ran("review") != 2 {
		t.Errorf("review ran %d times, want 2", exec.ran("review"))
	}

	// All step runs in DB should reflect only the final terminal states.
	runs, err := client.ListStepRuns(ctx, instID)
	if err != nil {
		t.Fatalf("list step runs: %v", err)
	}
	// Expect at minimum the final implement (passed) and final review (passed).
	passed := 0
	for _, r := range runs {
		if r.State == db.StepStatePassed {
			passed++
		}
	}
	if passed < 2 {
		t.Errorf("expected at least 2 passed step runs, got %d total: %v", passed, stepIDs(runs))
	}
}

// TestDAGIntegration_OnFailMaxRetriesExhausted verifies that exhausting
// max_retries marks the instance as failed in SQLite.
func TestDAGIntegration_OnFailMaxRetriesExhausted(t *testing.T) {
	ctx := context.Background()
	client := realDB(t)

	exec := newSeqExecutor()
	exec.scripts["review"] = []StepResult{
		{Success: false}, // always fails
	}
	eng := realEngine(t, client, exec)

	wf := config.WorkflowConfig{
		ID: "exhaust-wf",
		Steps: []config.StepConfig{
			{ID: "implement", Agent: "agent-a"},
			{ID: "review", Agent: "agent-b", DependsOn: []string{"implement"},
				OnFail: &config.StepOutcome{Goto: "implement", MaxRetries: 1}},
		},
	}

	instID, success, err := eng.RunInstance(ctx, wf, model.SourceItem{ID: "T-5"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if success {
		t.Fatal("expected failure after exhausting retries")
	}

	inst, err := client.GetWorkflowInstance(ctx, instID)
	if err != nil || inst == nil {
		t.Fatalf("get instance: %v", err)
	}
	if inst.State != db.InstanceStateFailed {
		t.Errorf("instance state = %q, want failed", inst.State)
	}

	// review ran max_retries+1 = 2 times total.
	if exec.ran("review") != 2 {
		t.Errorf("review ran %d times, want 2 (1 initial + 1 retry)", exec.ran("review"))
	}
}
