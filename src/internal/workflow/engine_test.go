package workflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// errTest is a sentinel error for tests that script a failure.
var errTest = errors.New("test error")

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
	bindings  map[string][]model.SourceBinding // task id → bindings (for bindingLister)
	ciPolls   []db.CIPollCheck                 // recorded wait_for CI polls (for ciPollRecorder)
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		instances: map[string]*db.WorkflowInstance{},
		stepRuns:  map[string]*db.StepRun{},
		bindings:  map[string][]model.SourceBinding{},
	}
}

// RecordCIPollCheck satisfies ciPollRecorder so the engine persists each CI poll
// the same way the production *db.Client does.
func (f *fakeStore) RecordCIPollCheck(_ context.Context, p *db.CIPollCheck) error {
	cp := *p
	f.mu.Lock()
	f.ciPolls = append(f.ciPolls, cp)
	f.mu.Unlock()
	return nil
}

// pollStatuses returns the recorded CI poll statuses in order, for assertions.
func (f *fakeStore) pollStatuses() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.ciPolls))
	for i, p := range f.ciPolls {
		out[i] = p.Status
	}
	return out
}

// ListBindingsByTask satisfies bindingLister so the engine resolves a task's
// bindings the same way the production *db.Client does.
func (f *fakeStore) ListBindingsByTask(_ context.Context, taskID string) ([]model.SourceBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bindings[taskID], nil
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

func (f *fakeSide) StateLock(_ context.Context, _ model.InternalTask, _ []model.SourceBinding) error {
	f.stateLocked = true
	return nil
}
func (f *fakeSide) PostComment(_ context.Context, _ model.InternalTask, _ []model.SourceBinding, c string) error {
	f.comments = append(f.comments, c)
	return nil
}
func (f *fakeSide) ApplyHook(_ context.Context, _ model.InternalTask, _ []model.SourceBinding, h config.OnComplete) error {
	f.hooks = append(f.hooks, h)
	return nil
}

// fakeSpawner records spawn requests and returns scripted outcomes.
type fakeSpawner struct {
	mu       sync.Mutex
	requests []model.SpawnRequest
	child    model.InternalTask
	spawnErr error
	awaitOK  bool
	awaitErr error
}

func (f *fakeSpawner) Spawn(_ context.Context, req model.SpawnRequest) (model.InternalTask, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	if f.spawnErr != nil {
		return model.InternalTask{}, f.spawnErr
	}
	return f.child, nil
}

func (f *fakeSpawner) Await(_ context.Context, _ string) (bool, error) {
	return f.awaitOK, f.awaitErr
}

func (f *fakeSpawner) lastRequest() (model.SpawnRequest, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return model.SpawnRequest{}, false
	}
	return f.requests[len(f.requests)-1], true
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

// testEngineWithSpawner builds a test engine wired with a spawner.
func testEngineWithSpawner(cfg *config.Config, store Store, exec StepExecutor, side SideEffects, sp WorkflowSpawner) *Engine {
	var seq atomic.Int64
	return NewEngine(cfg, store, exec,
		WithSideEffects(side),
		WithSpawner(sp),
		WithClock(func() time.Time { return time.Unix(1000, 0) }),
		WithIDGen(func(prefix string) string {
			return prefix + "-" + itoa(int(seq.Add(1)))
		}),
	)
}

// spawnWF builds a single-step workflow whose agent step uses the given spawn mode.
func spawnWF(spawnMode string) config.WorkflowConfig {
	return config.WorkflowConfig{
		ID:    "r",
		Steps: []config.StepConfig{{ID: "run", Agent: "backend-dev", Spawn: spawnMode}},
	}
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

	instID, _, err := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "C1", Title: "Fix bug"})
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

	instID, _, _ := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "C1"})

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

// TestEngine_PublishWritesBackToBindings asserts that a step whose agent emits
// an APIARY_PUBLISH payload writes it back via PostComment and records the step
// run's publish_payload/publish_state (6.2.1, 6.2.2, 6.4.1).
func TestEngine_PublishWritesBackToBindings(t *testing.T) {
	cfg := baseCfg()
	cfg.Settings.ResultComment = false // isolate the publish comment from result_comment
	store := newFakeStore()
	store.bindings["C1"] = []model.SourceBinding{{TaskID: "C1", SourceID: "s1", SourceItemID: "ISSUE-1"}}
	exec := &fakeExecutor{results: map[string]StepResult{
		"run": {Success: true, Output: "done", PublishPayload: "## Result\nshipped"},
	}}
	side := &fakeSide{}
	eng := testEngine(cfg, store, exec, side)

	wf := synthWF(config.RouteConfig{ID: "r", Agent: "backend-dev"})
	_, _, err := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "C1", Title: "Fix"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}

	if len(side.comments) != 1 || side.comments[0] != "## Result\nshipped" {
		t.Fatalf("expected one publish comment %q, got %#v", "## Result\nshipped", side.comments)
	}
	sr := store.stepRuns[store.stepOrder[0]]
	if sr.PublishState != db.PublishStateSent {
		t.Errorf("expected publish_state %q, got %q", db.PublishStateSent, sr.PublishState)
	}
	if sr.PublishPayload != "## Result\nshipped" {
		t.Errorf("expected publish_payload persisted, got %q", sr.PublishPayload)
	}
}

// TestEngine_PublishSkippedWhenNoBindings asserts that a publish payload on a
// task with no source bindings (e.g. a spawned task) is silently skipped: no
// PostComment, no error, and publish_state recorded as skipped (6.2.1, 6.4.3).
func TestEngine_PublishSkippedWhenNoBindings(t *testing.T) {
	cfg := baseCfg()
	cfg.Settings.ResultComment = false
	store := newFakeStore() // no bindings registered for C1
	exec := &fakeExecutor{results: map[string]StepResult{
		"run": {Success: true, Output: "done", PublishPayload: "should not post"},
	}}
	side := &fakeSide{}
	eng := testEngine(cfg, store, exec, side)

	wf := synthWF(config.RouteConfig{ID: "r", Agent: "backend-dev"})
	_, _, err := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "C1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}

	if len(side.comments) != 0 {
		t.Errorf("expected no publish comment when task has no bindings, got %#v", side.comments)
	}
	sr := store.stepRuns[store.stepOrder[0]]
	if sr.PublishState != db.PublishStateSkipped {
		t.Errorf("expected publish_state %q, got %q", db.PublishStateSkipped, sr.PublishState)
	}
	if sr.PublishPayload != "should not post" {
		t.Errorf("expected publish_payload persisted even when skipped, got %q", sr.PublishPayload)
	}
}

// TestEngine_SpawnAutoFireAndForget covers 7.2.2/7.2.3 + 7.3.1 at the engine
// level: a step emitting APIARY_SPAWN creates a child (parent set to the running
// task) and records spawned_task_id; in auto mode the step's own success stands.
func TestEngine_SpawnAutoFireAndForget(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"run": {Success: true, Output: "ok", SpawnRequest: &model.SpawnRequest{
			WorkflowID: "collect", Title: "Collect", Input: map[string]any{"k": "v"},
		}},
	}}
	sp := &fakeSpawner{child: model.InternalTask{ID: "task-child"}}
	eng := testEngineWithSpawner(cfg, store, exec, &fakeSide{}, sp)

	instID, _, err := eng.RunInstance(context.Background(), spawnWF(config.SpawnAuto),
		model.InternalTask{ID: "C1", Title: "Parent"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if store.instances[instID].State != db.InstanceStateDone {
		t.Errorf("expected instance done, got %s", store.instances[instID].State)
	}

	req, ok := sp.lastRequest()
	if !ok {
		t.Fatal("spawner was not called")
	}
	if req.ParentTaskID != "C1" {
		t.Errorf("spawn ParentTaskID = %q, want C1 (the running task)", req.ParentTaskID)
	}
	if req.WorkflowID != "collect" || req.Input["k"] != "v" {
		t.Errorf("spawn request not forwarded: %+v", req)
	}
	sr := store.stepRuns[store.stepOrder[0]]
	if sr.SpawnedTaskID != "task-child" {
		t.Errorf("spawned_task_id = %q, want task-child", sr.SpawnedTaskID)
	}
}

// TestEngine_SpawnAwait covers 7.2.5 + 7.3.2: spawn: await fails the step when
// the child fails and passes it when the child succeeds.
func TestEngine_SpawnAwait(t *testing.T) {
	for _, tc := range []struct {
		name    string
		awaitOK bool
		want    string
	}{
		{"child done", true, db.InstanceStateDone},
		{"child failed", false, db.InstanceStateFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			exec := &fakeExecutor{results: map[string]StepResult{
				"run": {Success: true, Output: "ok", SpawnRequest: &model.SpawnRequest{WorkflowID: "collect"}},
			}}
			sp := &fakeSpawner{child: model.InternalTask{ID: "task-child"}, awaitOK: tc.awaitOK}
			eng := testEngineWithSpawner(baseCfg(), store, exec, &fakeSide{}, sp)

			instID, _, err := eng.RunInstance(context.Background(), spawnWF(config.SpawnAwait),
				model.InternalTask{ID: "C1"})
			if err != nil {
				t.Fatalf("RunInstance: %v", err)
			}
			if store.instances[instID].State != tc.want {
				t.Errorf("instance state = %s, want %s", store.instances[instID].State, tc.want)
			}
		})
	}
}

// TestEngine_SpawnErrorFailsStep covers 7.3.4 at the engine level: a spawner
// error (e.g. unknown workflow) fails the step rather than passing silently.
func TestEngine_SpawnErrorFailsStep(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"run": {Success: true, SpawnRequest: &model.SpawnRequest{WorkflowID: "nope"}},
	}}
	sp := &fakeSpawner{spawnErr: errTest}
	eng := testEngineWithSpawner(baseCfg(), store, exec, &fakeSide{}, sp)

	instID, _, _ := eng.RunInstance(context.Background(), spawnWF(config.SpawnAuto), model.InternalTask{ID: "C1"})
	if store.instances[instID].State != db.InstanceStateFailed {
		t.Errorf("expected failed instance on spawn error, got %s", store.instances[instID].State)
	}
}

// TestEngine_SpawnWithoutSpawnerFailsStep: a spawn marker with no spawner wired
// is a step error, never a silent no-op (7.3.4 spirit).
func TestEngine_SpawnWithoutSpawnerFailsStep(t *testing.T) {
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"run": {Success: true, SpawnRequest: &model.SpawnRequest{WorkflowID: "collect"}},
	}}
	eng := testEngine(baseCfg(), store, exec, &fakeSide{}) // no spawner

	instID, _, _ := eng.RunInstance(context.Background(), spawnWF(config.SpawnAuto), model.InternalTask{ID: "C1"})
	if store.instances[instID].State != db.InstanceStateFailed {
		t.Errorf("expected failed instance when no spawner configured, got %s", store.instances[instID].State)
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

	_, _, err := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "C1", Title: "Add auth"})
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
	_, _, _ = eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "C1", Title: "t"})

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
	_, _, _ = eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "C1"})

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
	_, _, _ = eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "C1", Title: "t"})

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
	_, _, _ = eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "C1"})

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
	_, _, _ = eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "C1"})

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
	_, _, _ = eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "C1"})

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
