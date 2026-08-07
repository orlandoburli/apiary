package workflow

import (
	"context"
	"sync"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// seqExecutor returns a different scripted result per call to the same step,
// for testing retry loops. Falls back to the last scripted result, then to ok.
//
// mu guards calls/seen: parallel and for_each steps run their children on
// separate goroutines, so ExecuteStep is called concurrently and the bookkeeping
// races without it. Without the lock `go test -race` fails on this package —
// which is the one package whose concurrency most needs the race detector.
// scripts is written only during test setup, before the run starts, so reads of
// it are safe under the same lock.
type seqExecutor struct {
	mu      sync.Mutex
	scripts map[string][]StepResult
	calls   map[string]int
	seen    []string
}

func newSeqExecutor() *seqExecutor {
	return &seqExecutor{scripts: map[string][]StepResult{}, calls: map[string]int{}}
}

func (s *seqExecutor) ExecuteStep(_ context.Context, req StepRequest) StepResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, req.Step.ID)
	i := s.calls[req.Step.ID]
	s.calls[req.Step.ID]++
	list := s.scripts[req.Step.ID]
	switch {
	case i < len(list):
		return list[i]
	case len(list) > 0:
		return list[len(list)-1]
	default:
		return StepResult{Success: true, Output: "ok"}
	}
}

func (s *seqExecutor) ran(stepID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[stepID]
}

// seenIDs returns a copy of the executed step ids. Assertions must go through
// this rather than reading the field, so a test that inspects progress while
// goroutines are still running does not race.
func (s *seqExecutor) seenIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

func countSeen(seen []StepRequest, id string) int {
	n := 0
	for _, r := range seen {
		if r.Step.ID == id {
			n++
		}
	}
	return n
}

func executedIDs(seen []StepRequest) []string {
	out := make([]string, 0, len(seen))
	for _, r := range seen {
		out = append(out, r.Step.ID)
	}
	return out
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestDAG_DiamondParallelJoin(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	// A → {B, C} → D
	wf := config.WorkflowConfig{ID: "diamond", Steps: []config.StepConfig{
		{ID: "A", Agent: "architect"},
		{ID: "B", Agent: "backend-dev", DependsOn: []string{"A"}},
		{ID: "C", Agent: "backend-dev", DependsOn: []string{"A"}},
		{ID: "D", Agent: "architect", DependsOn: []string{"B", "C"}},
	}}

	_, success, _ := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if !success {
		t.Fatal("expected success")
	}
	ids := executedIDs(exec.seen)
	if len(ids) != 4 {
		t.Fatalf("expected 4 steps to run, got %v", ids)
	}
	// A runs first, D runs last (after B and C).
	if ids[0] != "A" || ids[len(ids)-1] != "D" {
		t.Errorf("unexpected execution order: %v", ids)
	}
}

func TestDAG_SplitFirstMatch(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"plan": {Success: true, StructuredOutput: map[string]any{"level": "high"}},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "split", Steps: []config.StepConfig{
		{
			ID: "plan", Agent: "architect",
			OutputSchema: &config.OutputSchema{Type: "object",
				Properties: map[string]config.SchemaField{"level": {Type: "string"}}},
			Memory: &config.MemoryConfig{Write: []string{"level"}},
		},
		{ID: "route", Type: config.StepTypeSplit, DependsOn: []string{"plan"}, Branches: []config.SplitBranch{
			{If: `memory.level == "high"`, Goto: "senior"},
			{Else: true, Goto: "junior"},
		}},
		{ID: "senior", Agent: "architect"},
		{ID: "junior", Agent: "backend-dev"},
	}}

	_, success, _ := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if !success {
		t.Fatal("expected success")
	}
	ids := executedIDs(exec.seen)
	if !contains(ids, "senior") {
		t.Errorf("expected senior branch to run, got %v", ids)
	}
	if contains(ids, "junior") {
		t.Errorf("junior branch should have been skipped, got %v", ids)
	}
}

func TestDAG_SplitFallback(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"plan": {Success: true, StructuredOutput: map[string]any{"level": "low"}},
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

	_, _, _ = eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	ids := executedIDs(exec.seen)
	if !contains(ids, "junior") || contains(ids, "senior") {
		t.Errorf("expected fallback junior only, got %v", ids)
	}
}

func TestDAG_SplitMultiFanOut(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "split", Steps: []config.StepConfig{
		{ID: "route", Type: config.StepTypeSplit, Multi: true, Branches: []config.SplitBranch{
			{If: `cell.labels contains "backend"`, Goto: "be"},
			{If: `cell.labels contains "frontend"`, Goto: "fe"},
		}},
		{ID: "be", Agent: "backend-dev"},
		{ID: "fe", Agent: "architect"},
	}}

	_, _, _ = eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1", Metadata: model.TaskMetadata{Labels: []string{"backend", "frontend"}}})
	ids := executedIDs(exec.seen)
	if !contains(ids, "be") || !contains(ids, "fe") {
		t.Errorf("expected both branches via multi, got %v", ids)
	}
}

func TestDAG_SplitSkipCascadesDownstream(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	// route picks "a"; "b" and its dependent "b2" must be skipped.
	wf := config.WorkflowConfig{ID: "split", Steps: []config.StepConfig{
		{ID: "route", Type: config.StepTypeSplit, Branches: []config.SplitBranch{
			{If: `cell.priority == "high"`, Goto: "a"},
			{Else: true, Goto: "b"},
		}},
		{ID: "a", Agent: "architect"},
		{ID: "b", Agent: "backend-dev"},
		{ID: "b2", Agent: "architect", DependsOn: []string{"b"}},
	}}

	_, success, _ := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1", Metadata: model.TaskMetadata{Priority: "high"}})
	if !success {
		t.Fatal("expected success")
	}
	ids := executedIDs(exec.seen)
	if !contains(ids, "a") {
		t.Errorf("expected 'a' to run, got %v", ids)
	}
	if contains(ids, "b") || contains(ids, "b2") {
		t.Errorf("expected b and b2 skipped, got %v", ids)
	}
}

func TestDAG_OnFailLoopSucceedsOnRetry(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := newSeqExecutor()
	exec.scripts["review"] = []StepResult{
		{Success: false, Output: "changes requested"}, // first attempt fails
		{Success: true, Output: "LGTM"},               // retry passes
	}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "loop", Steps: []config.StepConfig{
		{ID: "implement", Agent: "backend-dev"},
		{ID: "review", Agent: "architect", DependsOn: []string{"implement"},
			OnFail: &config.StepOutcome{Goto: "implement", MaxRetries: 2}},
	}}

	_, success, _ := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if !success {
		t.Fatal("expected success after retry")
	}
	if exec.ran("implement") != 2 {
		t.Errorf("expected implement to run twice (loop), got %d", exec.ran("implement"))
	}
	if exec.ran("review") != 2 {
		t.Errorf("expected review to run twice, got %d", exec.ran("review"))
	}
}

func TestDAG_OnFailLoopExhaustsRetries(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := newSeqExecutor()
	exec.scripts["review"] = []StepResult{{Success: false}} // always fails
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "loop", Steps: []config.StepConfig{
		{ID: "implement", Agent: "backend-dev"},
		{ID: "review", Agent: "architect", DependsOn: []string{"implement"},
			OnFail: &config.StepOutcome{Goto: "implement", MaxRetries: 1}},
	}}

	instID, success, _ := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if success {
		t.Fatal("expected failure after exhausting retries")
	}
	if store.instances[instID].State != db.InstanceStateFailed {
		t.Errorf("expected failed instance, got %s", store.instances[instID].State)
	}
	// review: attempt 1 (fail, retry) + attempt 2 (fail, exhausted) = 2 runs.
	if exec.ran("review") != 2 {
		t.Errorf("expected review to run twice, got %d", exec.ran("review"))
	}
	if exec.ran("implement") != 2 {
		t.Errorf("expected implement to run twice (one loop), got %d", exec.ran("implement"))
	}
}

func TestDAG_FailureWithoutLoopStopsGraph(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"build": {Success: false, Output: "compile error"},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "chain", Steps: []config.StepConfig{
		{ID: "build", Agent: "backend-dev"},
		{ID: "deploy", Agent: "architect", DependsOn: []string{"build"}},
	}}

	_, success, _ := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if success {
		t.Fatal("expected failure")
	}
	ids := executedIDs(exec.seen)
	if contains(ids, "deploy") {
		t.Errorf("deploy should not run after build failed, got %v", ids)
	}
}

func TestDAG_LinearChainOrder(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "w", Steps: []config.StepConfig{
		{ID: "a", Agent: "architect"},
		{ID: "b", Agent: "backend-dev", DependsOn: []string{"a"}},
		{ID: "c", Agent: "architect", DependsOn: []string{"b"}},
	}}
	_, success, _ := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if !success {
		t.Fatal("expected success")
	}
	ids := executedIDs(exec.seen)
	want := []string{"a", "b", "c"}
	if len(ids) != 3 || ids[0] != want[0] || ids[1] != want[1] || ids[2] != want[2] {
		t.Errorf("expected order %v, got %v", want, ids)
	}
}
