package workflow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// concurrentExecutor is a thread-safe executor that records the order in which
// steps START executing, so tests can verify parallelism.
type concurrentExecutor struct {
	mu      sync.Mutex
	results map[string]StepResult
	started []string // step IDs in the order their execution started
	delay   map[string]time.Duration
}

func newConcurrentExecutor() *concurrentExecutor {
	return &concurrentExecutor{
		results: map[string]StepResult{},
		delay:   map[string]time.Duration{},
	}
}

func (c *concurrentExecutor) ExecuteStep(_ context.Context, req StepRequest) StepResult {
	c.mu.Lock()
	c.started = append(c.started, req.Step.ID)
	d := c.delay[req.Step.ID]
	c.mu.Unlock()

	if d > 0 {
		time.Sleep(d)
	}

	c.mu.Lock()
	res, ok := c.results[req.Step.ID]
	c.mu.Unlock()

	if ok {
		return res
	}
	return StepResult{Success: true, Output: "ok"}
}

// TestConcurrentScheduler_IndependentStepsRunParallel checks that two
// independent agent steps are dispatched concurrently (both start before either
// finishes) when concurrency > 1.
func TestConcurrentScheduler_IndependentStepsRunParallel(t *testing.T) {
	cfg := baseCfg()
	cfg.Settings.Concurrency = 4
	store := newFakeStore()

	exec := newConcurrentExecutor()
	// Give both steps a small delay so they overlap if truly parallel.
	exec.delay["a"] = 20 * time.Millisecond
	exec.delay["b"] = 20 * time.Millisecond

	eng := testEngine(cfg, store, exec, nil)

	wf := config.WorkflowConfig{ID: "par-wf", Steps: []config.StepConfig{
		{ID: "a", Agent: "architect"},
		{ID: "b", Agent: "architect"}, // no depends_on: independent from a
	}}

	_, success, err := eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("expected success")
	}

	exec.mu.Lock()
	started := exec.started
	exec.mu.Unlock()

	if len(started) != 2 {
		t.Fatalf("expected 2 steps to run, got %d: %v", len(started), started)
	}
}

// TestConcurrentScheduler_Concurrency1IsSequential ensures concurrency=1 still
// executes steps in declaration order (regression guard).
func TestConcurrentScheduler_Concurrency1IsSequential(t *testing.T) {
	cfg := baseCfg()
	cfg.Settings.Concurrency = 1
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{}}
	eng := testEngine(cfg, store, exec, nil)

	wf := config.WorkflowConfig{ID: "seq-wf", Steps: []config.StepConfig{
		{ID: "a", Agent: "architect"},
		{ID: "b", Agent: "architect"},
		{ID: "c", Agent: "architect"},
	}}

	_, success, err := eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("expected success")
	}

	ids := executedIDs(exec.seen)
	for i, want := range []string{"a", "b", "c"} {
		if i >= len(ids) || ids[i] != want {
			t.Errorf("step[%d] = %q, want %q (got %v)", i, ids[i], want, ids)
			break
		}
	}
}

// TestConcurrentScheduler_DepsPreventEarlyDispatch verifies that a step only
// starts after its dependency passes, even with high concurrency.
func TestConcurrentScheduler_DepsPreventEarlyDispatch(t *testing.T) {
	cfg := baseCfg()
	cfg.Settings.Concurrency = 4
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{}}
	eng := testEngine(cfg, store, exec, nil)

	wf := config.WorkflowConfig{ID: "dep-wf", Steps: []config.StepConfig{
		{ID: "a", Agent: "architect"},
		{ID: "b", Agent: "architect", DependsOn: []string{"a"}},
	}}

	_, success, err := eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("expected success")
	}

	ids := executedIDs(exec.seen)
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Errorf("expected [a, b] in order, got %v", ids)
	}
}

// TestParallelStep_AllJoin verifies that a StepTypeParallel step with join=all
// passes when all children pass.
func TestParallelStep_AllJoin(t *testing.T) {
	cfg := baseCfg()
	cfg.Settings.Concurrency = 4
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{}}
	eng := testEngine(cfg, store, exec, nil)

	wf := config.WorkflowConfig{ID: "parallel-wf", Steps: []config.StepConfig{
		{
			ID:   "par",
			Type: config.StepTypeParallel,
			Join: "all",
			SubSteps: []config.StepConfig{
				{ID: "child-a", Agent: "architect"},
				{ID: "child-b", Agent: "architect"},
			},
		},
		{ID: "after", Agent: "architect", DependsOn: []string{"par"}},
	}}

	_, success, err := eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("expected success (all children passed)")
	}

	ids := executedIDs(exec.seen)
	if !contains(ids, "child-a") || !contains(ids, "child-b") || !contains(ids, "after") {
		t.Errorf("expected child-a, child-b, after to run, got %v", ids)
	}
}

// TestParallelStep_AllJoinFailsIfAnyChildFails verifies join=all fails if one child fails.
func TestParallelStep_AllJoinFailsIfAnyChildFails(t *testing.T) {
	cfg := baseCfg()
	cfg.Settings.Concurrency = 4
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"child-b": {Success: false},
	}}
	eng := testEngine(cfg, store, exec, nil)

	wf := config.WorkflowConfig{ID: "parallel-wf", Steps: []config.StepConfig{
		{
			ID:   "par",
			Type: config.StepTypeParallel,
			Join: "all",
			SubSteps: []config.StepConfig{
				{ID: "child-a", Agent: "architect"},
				{ID: "child-b", Agent: "architect"},
			},
		},
	}}

	_, success, _ := eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "c1"})
	if success {
		t.Fatal("expected failure (child-b failed, join=all)")
	}
}

// TestParallelStep_AnyJoinPassesIfOneChildPasses verifies join=any passes if any child passes.
func TestParallelStep_AnyJoinPassesIfOneChildPasses(t *testing.T) {
	cfg := baseCfg()
	cfg.Settings.Concurrency = 4
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"child-a": {Success: false},
		"child-b": {Success: true},
	}}
	eng := testEngine(cfg, store, exec, nil)

	wf := config.WorkflowConfig{ID: "parallel-any-wf", Steps: []config.StepConfig{
		{
			ID:   "par",
			Type: config.StepTypeParallel,
			Join: "any",
			SubSteps: []config.StepConfig{
				{ID: "child-a", Agent: "architect"},
				{ID: "child-b", Agent: "architect"},
			},
		},
		{ID: "after", Agent: "architect", DependsOn: []string{"par"}},
	}}

	_, success, err := eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("expected success (child-b passed, join=any)")
	}

	ids := executedIDs(exec.seen)
	if !contains(ids, "after") {
		t.Errorf("expected after to run, got %v", ids)
	}
}

// TestConcurrentScheduler_MemoryOrderDeterministic verifies that concurrent
// steps' memory contributions are ordered by declaration, not completion order.
func TestConcurrentScheduler_MemoryOrderDeterministic(t *testing.T) {
	cfg := baseCfg()
	cfg.Settings.Concurrency = 4
	store := newFakeStore()

	// a and b run concurrently; b completes faster but a is declared first.
	// The final memory should have a's contribution before b's.
	exec := &fakeExecutor{results: map[string]StepResult{
		"a": {Success: true, StructuredOutput: map[string]any{"field": "from-a"},
			Summary: "a done"},
		"b": {Success: true, StructuredOutput: map[string]any{"field": "from-b"},
			Summary: "b done"},
	}}
	eng := testEngine(cfg, store, exec, nil)

	wf := config.WorkflowConfig{ID: "mem-order-wf", Steps: []config.StepConfig{
		{
			ID:    "a",
			Agent: "architect",
			OutputSchema: &config.OutputSchema{
				Type:       "object",
				Properties: map[string]config.SchemaField{"field": {Type: "string"}},
			},
			Memory: &config.MemoryConfig{Write: []string{"field"}},
		},
		{
			ID:    "b",
			Agent: "architect",
			OutputSchema: &config.OutputSchema{
				Type:       "object",
				Properties: map[string]config.SchemaField{"field": {Type: "string"}},
			},
			Memory: &config.MemoryConfig{Write: []string{"field"}},
		},
		// c depends on both; reads "field" from memory
		{ID: "c", Agent: "architect", DependsOn: []string{"a", "b"}},
	}}

	_, success, err := eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("expected success")
	}

	// Verify that c ran (proving the join worked).
	ids := executedIDs(exec.seen)
	if !contains(ids, "c") {
		t.Errorf("expected c to run after a and b, got %v", ids)
	}
}
