package workflow

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// synthWF converts a RouteConfig to an equivalent single-step WorkflowConfig.
// Used only in tests; production code no longer has SynthesizeWorkflow.
func synthWF(route config.RouteConfig) config.WorkflowConfig {
	oc := route.OnComplete
	return config.WorkflowConfig{
		ID:         route.ID,
		Trigger:    &config.TriggerConfig{Priority: route.Priority, Match: route.Match},
		Steps:      []config.StepConfig{{ID: "run", Agent: route.Agent}},
		OnComplete: &oc,
	}
}

// fakeStore records instances and step runs in memory. Thread-safe so it can be
// used from concurrent engine tests.
type fakeStore struct {
	mu        sync.Mutex
	instances map[string]*db.WorkflowInstance
	stepRuns  map[string]*db.StepRun
	stepOrder []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{instances: map[string]*db.WorkflowInstance{}, stepRuns: map[string]*db.StepRun{}}
}

func (f *fakeStore) CreateWorkflowInstance(_ context.Context, inst *db.WorkflowInstance) error {
	cp := *inst
	f.mu.Lock()
	f.instances[inst.ID] = &cp
	f.mu.Unlock()
	return nil
}
func (f *fakeStore) UpdateWorkflowInstanceState(_ context.Context, id, state string) error {
	f.mu.Lock()
	if inst, ok := f.instances[id]; ok {
		inst.State = state
	}
	f.mu.Unlock()
	return nil
}
func (f *fakeStore) CreateStepRun(_ context.Context, sr *db.StepRun) error {
	cp := *sr
	f.mu.Lock()
	f.stepRuns[sr.ID] = &cp
	f.stepOrder = append(f.stepOrder, sr.ID)
	f.mu.Unlock()
	return nil
}
func (f *fakeStore) UpdateStepRun(_ context.Context, sr *db.StepRun) error {
	cp := *sr
	f.mu.Lock()
	f.stepRuns[sr.ID] = &cp
	f.mu.Unlock()
	return nil
}

// fakeExecutor returns scripted results keyed by step ID and records requests.
// Thread-safe so it can be used from concurrent engine tests.
type fakeExecutor struct {
	mu      sync.Mutex
	results map[string]StepResult
	seen    []StepRequest
}

func (f *fakeExecutor) ExecuteStep(_ context.Context, req StepRequest) StepResult {
	f.mu.Lock()
	f.seen = append(f.seen, req)
	r, ok := f.results[req.Step.ID]
	f.mu.Unlock()
	if ok {
		return r
	}
	return StepResult{Success: true, Output: "ok"}
}

// fakeSide records side-effect calls.
type fakeSide struct {
	stateLocked bool
	comments    []string
	hooks       []config.OnComplete
}

func (f *fakeSide) StateLock(_ context.Context, _ model.SourceItem) error {
	f.stateLocked = true
	return nil
}
func (f *fakeSide) PostComment(_ context.Context, _ model.SourceItem, c string) error {
	f.comments = append(f.comments, c)
	return nil
}
func (f *fakeSide) ApplyHook(_ context.Context, _ model.SourceItem, h config.OnComplete) error {
	f.hooks = append(f.hooks, h)
	return nil
}

func testEngine(cfg *config.Config, store Store, exec StepExecutor, side SideEffects) *Engine {
	var seq atomic.Int64
	return NewEngine(cfg, store, exec,
		WithSideEffects(side),
		WithClock(func() time.Time { return time.Unix(1000, 0) }),
		WithIDGen(func(prefix string) string {
			return prefix + "-" + itoa(int(seq.Add(1)))
		}),
	)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func baseCfg() *config.Config {
	return &config.Config{
		Agents: []config.AgentConfig{
			{ID: "architect", Model: "claude-opus-4-8"},
			{ID: "backend-dev", Model: "claude-sonnet-4-6"},
		},
		Settings: config.Settings{StateLock: true, ResultComment: true},
	}
}

func TestEngine_SingleStepSuccess(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"run": {Success: true, Output: "done"},
	}}
	side := &fakeSide{}
	eng := testEngine(cfg, store, exec, side)

	wf := synthWF(config.RouteConfig{
		ID: "backend-bugs", Priority: 10, Agent: "backend-dev",
		OnComplete: config.OnComplete{SetState: "in_review"},
	})

	instID, _, err := eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "C1", Title: "Fix bug"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}

	inst := store.instances[instID]
	if inst == nil || inst.State != db.InstanceStateDone {
		t.Fatalf("instance not marked done: %+v", inst)
	}
	if len(store.stepOrder) != 1 {
		t.Fatalf("expected 1 step run, got %d", len(store.stepOrder))
	}
	sr := store.stepRuns[store.stepOrder[0]]
	if sr.State != db.StepStatePassed || sr.Output != "done" {
		t.Errorf("step run wrong: %+v", sr)
	}
	if !side.stateLocked {
		t.Error("expected state_lock to fire")
	}
	if len(side.hooks) != 1 || side.hooks[0].SetState != "in_review" {
		t.Errorf("expected on_complete hook, got: %+v", side.hooks)
	}
	// Model resolves to the agent's model.
	if exec.seen[0].Model != "claude-sonnet-4-6" {
		t.Errorf("expected agent model, got %q", exec.seen[0].Model)
	}
}

func TestEngine_StepFailureMarksInstanceFailed(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"run": {Success: false, Output: "boom"},
	}}
	side := &fakeSide{}
	eng := testEngine(cfg, store, exec, side)

	wf := synthWF(config.RouteConfig{ID: "r", Agent: "backend-dev",
		OnComplete: config.OnComplete{SetState: "in_review"}})
	wf.OnFail = &config.OnComplete{SetState: "blocked"}

	instID, _, _ := eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "C1"})

	if store.instances[instID].State != db.InstanceStateFailed {
		t.Errorf("expected failed instance, got %s", store.instances[instID].State)
	}
	sr := store.stepRuns[store.stepOrder[0]]
	if sr.State != db.StepStateFailed {
		t.Errorf("expected failed step, got %s", sr.State)
	}
	// on_fail hook applied, not on_complete.
	if len(side.hooks) != 1 || side.hooks[0].SetState != "blocked" {
		t.Errorf("expected on_fail hook blocked, got: %+v", side.hooks)
	}
}

func TestEngine_SequentialMemoryThreading(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"plan": {Success: true, Output: "planned",
			StructuredOutput: map[string]any{"complexity": "high"},
			Summary:          "decided on JWT"},
		"implement": {Success: true, Output: "implemented"},
	}}
	side := &fakeSide{}
	eng := testEngine(cfg, store, exec, side)

	wf := config.WorkflowConfig{
		ID: "feature",
		Steps: []config.StepConfig{
			{
				ID: "plan", Agent: "architect",
				OutputSchema: &config.OutputSchema{Type: "object",
					Properties: map[string]config.SchemaField{"complexity": {Type: "string"}}},
				Memory: &config.MemoryConfig{Write: []string{"complexity"}},
			},
			{ID: "implement", Agent: "backend-dev", DependsOn: []string{"plan"}},
		},
	}

	_, _, err := eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "C1", Title: "Add auth"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}

	// The implement step should have received memory containing the plan's
	// written field and summary.
	if len(exec.seen) != 2 {
		t.Fatalf("expected 2 step executions, got %d", len(exec.seen))
	}
	implMem := exec.seen[1].MemoryDoc
	if !strings.Contains(implMem, "complexity: high") {
		t.Errorf("implement step memory missing plan field:\n%s", implMem)
	}
	if !strings.Contains(implMem, "decided on JWT") {
		t.Errorf("implement step memory missing plan summary:\n%s", implMem)
	}
	// The plan step (first) gets memory with no prior step data.
	if strings.Contains(exec.seen[0].MemoryDoc, "complexity: high") {
		t.Error("plan step should not see its own output in memory")
	}
}

func TestEngine_MemoryReadFalseSkipsInjection(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	rd := false
	wf := config.WorkflowConfig{ID: "w", Steps: []config.StepConfig{
		{ID: "s", Agent: "architect", Memory: &config.MemoryConfig{Read: &rd}},
	}}
	_, _, _ = eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "C1", Title: "t"})

	if exec.seen[0].MemoryDoc != "" {
		t.Errorf("expected empty memory doc when memory.read is false, got:\n%s", exec.seen[0].MemoryDoc)
	}
}

func TestEngine_PerStepModelOverride(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "w", Steps: []config.StepConfig{
		{ID: "s", Agent: "backend-dev", Model: "claude-haiku-4-5"},
	}}
	_, _, _ = eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "C1"})

	if exec.seen[0].Model != "claude-haiku-4-5" {
		t.Errorf("expected step model override, got %q", exec.seen[0].Model)
	}
}

func TestEngine_ResultCommentOnComplete(t *testing.T) {
	cfg := baseCfg() // ResultComment: true → on_complete default
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{"run": {Success: true, Output: "x"}}}
	side := &fakeSide{}
	eng := testEngine(cfg, store, exec, side)

	wf := synthWF(config.RouteConfig{ID: "r", Agent: "backend-dev"})
	_, _, _ = eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "C1", Title: "t"})

	if len(side.comments) != 1 {
		t.Fatalf("expected 1 on_complete comment, got %d", len(side.comments))
	}
	if !strings.Contains(side.comments[0], "Workflow: r — ✓ Done") {
		t.Errorf("unexpected comment: %q", side.comments[0])
	}
}

func TestEngine_ResultCommentPerStep(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"plan":      {Success: true, Output: "planned"},
		"implement": {Success: true, Output: "implemented"},
	}}
	side := &fakeSide{}
	eng := testEngine(cfg, store, exec, side)

	wf := config.WorkflowConfig{
		ID: "w", ResultComment: config.ResultCommentPerStep,
		Steps: []config.StepConfig{
			{ID: "plan", Agent: "architect"},
			{ID: "implement", Agent: "backend-dev", DependsOn: []string{"plan"}},
		},
	}
	_, _, _ = eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "C1"})

	if len(side.comments) != 2 {
		t.Fatalf("expected 2 per-step comments, got %d: %v", len(side.comments), side.comments)
	}
	if !strings.Contains(side.comments[0], "Step: plan") || !strings.Contains(side.comments[1], "Step: implement") {
		t.Errorf("unexpected per-step comments: %v", side.comments)
	}
}

func TestEngine_ResultCommentOff(t *testing.T) {
	cfg := baseCfg()
	cfg.Settings.ResultComment = false
	store := newFakeStore()
	exec := &fakeExecutor{}
	side := &fakeSide{}
	eng := testEngine(cfg, store, exec, side)

	wf := synthWF(config.RouteConfig{ID: "r", Agent: "backend-dev"})
	_, _, _ = eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "C1"})

	if len(side.comments) != 0 {
		t.Errorf("expected no comments when result_comment off, got: %v", side.comments)
	}
}

func TestEngine_StructuredOutputPersisted(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"run": {Success: true, Output: "x", StructuredOutput: map[string]any{"k": "v"}, Summary: "sum"},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := synthWF(config.RouteConfig{ID: "r", Agent: "backend-dev"})
	_, _, _ = eng.RunInstance(context.Background(), wf, model.SourceItem{ID: "C1"})

	sr := store.stepRuns[store.stepOrder[0]]
	if !strings.Contains(sr.StructuredOutput, `"k":"v"`) {
		t.Errorf("structured output not persisted: %q", sr.StructuredOutput)
	}
	if sr.Summary != "sum" {
		t.Errorf("summary not persisted: %q", sr.Summary)
	}
}

func TestSynthWF(t *testing.T) {
	route := config.RouteConfig{
		ID: "backend-bugs", Priority: 10, Agent: "backend-dev",
		Match:      config.RouteMatch{Source: "main-plane", Labels: []string{"bug"}},
		OnComplete: config.OnComplete{SetState: "in_review"},
	}
	wf := synthWF(route)
	if wf.ID != "backend-bugs" || len(wf.Steps) != 1 {
		t.Fatalf("unexpected synthesized workflow: %+v", wf)
	}
	if wf.Steps[0].Agent != "backend-dev" {
		t.Errorf("synthesized step agent wrong: %q", wf.Steps[0].Agent)
	}
	if wf.Trigger == nil || wf.Trigger.Priority != 10 || wf.Trigger.Match.Source != "main-plane" {
		t.Errorf("synthesized trigger wrong: %+v", wf.Trigger)
	}
	if wf.OnComplete == nil || wf.OnComplete.SetState != "in_review" {
		t.Errorf("synthesized on_complete wrong: %+v", wf.OnComplete)
	}
}
