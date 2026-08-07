package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/router"
	runnerpkg "github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/source"
)

// stepRecordingRunner records the step id of every run it is asked to perform,
// so a test can assert exactly which steps the engine (re-)executed.
type stepRecordingRunner struct {
	mu    sync.Mutex
	steps []string
}

func (r *stepRecordingRunner) ID() string                     { return "recording" }
func (r *stepRecordingRunner) Configure(map[string]any) error { return nil }
func (r *stepRecordingRunner) Run(_ context.Context, req model.RunRequest) (model.RunResult, error) {
	r.mu.Lock()
	r.steps = append(r.steps, req.StepID)
	r.mu.Unlock()
	return model.RunResult{Success: true}, nil
}

func (r *stepRecordingRunner) ran() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.steps...)
}

// autoResumeWorkflow is the three-step pipeline of issue #376: plan and
// implement already passed, the last step was in flight when the daemon died.
func autoResumeWorkflow(policy string) config.WorkflowConfig {
	return config.WorkflowConfig{
		ID:      "impl",
		Resume:  policy,
		Trigger: &config.TriggerConfig{Priority: 10, Match: config.RouteMatch{Source: "src"}},
		Steps: []config.StepConfig{
			{ID: "plan", Agent: "a", Idempotent: true},
			{ID: "implement", Agent: "a", Idempotent: true},
			{ID: "qa", Agent: "a", Idempotent: true},
		},
	}
}

// autoResumeFixture builds a dispatcher over a real DB holding one task whose
// `impl` instance was left interrupted by a daemon restart, with plan/implement
// passed and qa interrupted.
func autoResumeFixture(t *testing.T, policy string) (*Dispatcher, *db.Client, *stepRecordingRunner, string) {
	t.Helper()
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "resume-auto.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	task := &model.InternalTask{Title: "Ship it", State: model.TaskStateRunning}
	if err := dbc.InternalTasks().CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	inst := &db.WorkflowInstance{
		ID: "i-orphan", WorkflowID: "impl", CellID: "ISSUE-9", SourceID: "src",
		TaskID: task.ID, State: db.InstanceStateInterrupted,
	}
	if err := dbc.CreateWorkflowInstance(ctx, inst); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	for _, sr := range []db.StepRun{
		{ID: "sr1", WorkflowInstanceID: inst.ID, StepID: "plan", AgentID: "a", State: db.StepStatePassed},
		{ID: "sr2", WorkflowInstanceID: inst.ID, StepID: "implement", AgentID: "a", State: db.StepStatePassed},
		{ID: "sr3", WorkflowInstanceID: inst.ID, StepID: "qa", AgentID: "a", State: db.StepStateInterrupted},
	} {
		sr := sr
		if err := dbc.CreateStepRun(ctx, &sr); err != nil {
			t.Fatalf("create step run: %v", err)
		}
	}

	cfg := &config.Config{
		Version:   "1",
		Sources:   []config.SourceConfig{{ID: "src", Type: "fake"}},
		Agents:    []config.AgentConfig{{ID: "a", Model: "test/model"}},
		Settings:  config.Settings{},
		Workflows: []config.WorkflowConfig{autoResumeWorkflow(policy)},
	}
	rt, err := router.New(cfg)
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	runner := &stepRecordingRunner{}
	d := &Dispatcher{
		cfg:         cfg,
		db:          dbc,
		router:      rt,
		runners:     map[string]runnerpkg.Runner{"agent-a": runner},
		agentRunner: map[string]string{"a": "claude"},
		agentSem:    map[string]chan struct{}{},
		stats:       map[string]*sourceStat{"src": {}},
	}
	return d, dbc, runner, task.ID
}

// waitForDescendant polls for an instance whose resumed_from is sourceID.
func waitForDescendant(t *testing.T, dbc *db.Client, taskID, sourceID string) *db.WorkflowInstance {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		instances, err := dbc.ListWorkflowInstancesByTask(ctx, taskID)
		if err != nil {
			t.Fatalf("list instances: %v", err)
		}
		for i := range instances {
			if instances[i].ResumedFrom == sourceID {
				return &instances[i]
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

// TestResumeAutoInterrupted_ContinuesInsteadOfRestarting is the regression test
// for issue #376: after a restart, an interrupted instance of a `resume: auto`
// workflow must be continued from its cached steps — only the step that was in
// flight re-runs — instead of the rescan starting a fresh run at step 1.
func TestResumeAutoInterrupted_ContinuesInsteadOfRestarting(t *testing.T) {
	d, dbc, runner, taskID := autoResumeFixture(t, config.ResumeAuto)

	d.resumeAutoInterrupted(context.Background())

	desc := waitForDescendant(t, dbc, taskID, "i-orphan")
	if desc == nil {
		t.Fatal("no resume descendant created for the interrupted resume:auto instance")
	}

	// Give the descendant a moment to settle, then assert only 'qa' re-ran.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got, err := dbc.GetWorkflowInstance(context.Background(), desc.ID); err == nil && got != nil && got.State == db.InstanceStateDone {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	ran := runner.ran()
	if len(ran) != 1 || ran[0] != "qa" {
		t.Errorf("resumed run executed %v, want only [qa] (plan/implement must come from cache)", ran)
	}
}

// TestResumeAutoInterrupted_SkipsNonAutoPolicies keeps today's behavior for
// workflows that did not opt in: they are left interrupted (manual `apiary
// resume` / re-dispatch), never replayed automatically.
func TestResumeAutoInterrupted_SkipsNonAutoPolicies(t *testing.T) {
	for _, policy := range []string{"", config.ResumeAllowed, config.ResumeForbidden} {
		d, dbc, runner, taskID := autoResumeFixture(t, policy)
		d.resumeAutoInterrupted(context.Background())
		time.Sleep(200 * time.Millisecond)
		if desc := waitForDescendantOnce(t, dbc, taskID, "i-orphan"); desc != nil {
			t.Errorf("resume policy %q: unexpected auto-resume descendant %s", policy, desc.ID)
		}
		if ran := runner.ran(); len(ran) != 0 {
			t.Errorf("resume policy %q: unexpected step runs %v", policy, ran)
		}
	}
}

func waitForDescendantOnce(t *testing.T, dbc *db.Client, taskID, sourceID string) *db.WorkflowInstance {
	t.Helper()
	instances, err := dbc.ListWorkflowInstancesByTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	for i := range instances {
		if instances[i].ResumedFrom == sourceID {
			return &instances[i]
		}
	}
	return nil
}

// TestResumeAutoInterrupted_SkipsSupersededInstance ensures repeated restarts do
// not fork several descendants from the same interrupted ancestor.
func TestResumeAutoInterrupted_SkipsSupersededInstance(t *testing.T) {
	ctx := context.Background()
	d, dbc, runner, taskID := autoResumeFixture(t, config.ResumeAuto)
	if err := dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{
		ID: "i-descendant", WorkflowID: "impl", CellID: "ISSUE-9", SourceID: "src",
		TaskID: taskID, State: db.InstanceStateDone, ResumedFrom: "i-orphan",
	}); err != nil {
		t.Fatalf("create descendant: %v", err)
	}

	d.resumeAutoInterrupted(ctx)
	time.Sleep(200 * time.Millisecond)

	instances, err := dbc.ListWorkflowInstancesByTask(ctx, taskID)
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	n := 0
	for i := range instances {
		if instances[i].ResumedFrom == "i-orphan" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("descendants of i-orphan = %d, want 1 (already superseded)", n)
	}
	if ran := runner.ran(); len(ran) != 0 {
		t.Errorf("unexpected step runs %v", ran)
	}
}

// TestDropAutoResumingMatches covers the dispatch guard: while a (task,
// workflow) pair is being auto-resumed, a poll must not dispatch a fresh
// instance for it — but other workflows and other tasks still flow.
func TestDropAutoResumingMatches(t *testing.T) {
	d := &Dispatcher{}
	d.autoResuming.Store(autoResumeKey("T1", "impl"), struct{}{})

	matches := []router.Match{
		{Route: config.RouteConfig{ID: "impl"}},
		{Route: config.RouteConfig{ID: "triage"}},
	}
	got := d.dropAutoResumingMatches("T1", matches)
	if len(got) != 1 || got[0].Route.ID != "triage" {
		t.Errorf("kept %+v, want only triage (impl is auto-resuming)", got)
	}
	if got := d.dropAutoResumingMatches("T2", matches); len(got) != 2 {
		t.Errorf("another task must be unaffected, kept %d of 2", len(got))
	}
}

// blockingRunner records step ids and holds each run open until release is
// closed, so a test can observe a resume that is still in flight.
type blockingRunner struct {
	release chan struct{}
	mu      sync.Mutex
	steps   []string
}

func (r *blockingRunner) ID() string                     { return "blocking" }
func (r *blockingRunner) Configure(map[string]any) error { return nil }
func (r *blockingRunner) Run(ctx context.Context, req model.RunRequest) (model.RunResult, error) {
	r.mu.Lock()
	r.steps = append(r.steps, req.StepID)
	r.mu.Unlock()
	select {
	case <-r.release:
	case <-ctx.Done():
	}
	return model.RunResult{Success: true}, nil
}

func (r *blockingRunner) ran() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.steps...)
}

// TestPollDoesNotRestartWhileAutoResuming is the end-to-end regression for issue
// #376 at the poll level: the daemon restarts, the orphaned instance of a
// `resume: auto` workflow is auto-resumed, and the very next poll of the same
// still-matching source item must NOT dispatch a fresh instance from step 1.
func TestPollDoesNotRestartWhileAutoResuming(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "resume-poll.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	item := model.SourceItem{ID: "ISSUE-9", SourceID: "src", Number: "#9", Title: "Ship it", State: "todo"}
	adapter := &recordingAdapter{items: []model.SourceItem{item}}

	cfg := &config.Config{
		Version:   "1",
		Sources:   []config.SourceConfig{{ID: "src", Type: "fake"}},
		Agents:    []config.AgentConfig{{ID: "a", Model: "test/model"}},
		Workflows: []config.WorkflowConfig{autoResumeWorkflow(config.ResumeAuto)},
	}
	rt, err := router.New(cfg)
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	runner := &blockingRunner{release: make(chan struct{})}
	d := &Dispatcher{
		cfg:         cfg,
		db:          dbc,
		router:      rt,
		sources:     map[string]source.Adapter{"src": adapter},
		runners:     map[string]runnerpkg.Runner{"agent-a": runner},
		agentRunner: map[string]string{"a": "claude"},
		agentSem:    map[string]chan struct{}{},
		stats:       map[string]*sourceStat{"src": {}},
	}
	d.binder = source.NewSourceBinder(dbc)

	// Bind the item exactly as a poll would, so the interrupted instance hangs off
	// the same InternalTask the next poll will route on.
	task, err := d.binder.Bind(ctx, item)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	inst := &db.WorkflowInstance{
		ID: "i-orphan", WorkflowID: "impl", CellID: item.ID, SourceID: "src",
		TaskID: task.ID, State: db.InstanceStateInterrupted,
	}
	if err := dbc.CreateWorkflowInstance(ctx, inst); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	for _, sr := range []db.StepRun{
		{ID: "sr1", WorkflowInstanceID: inst.ID, StepID: "plan", AgentID: "a", State: db.StepStatePassed},
		{ID: "sr2", WorkflowInstanceID: inst.ID, StepID: "implement", AgentID: "a", State: db.StepStatePassed},
		{ID: "sr3", WorkflowInstanceID: inst.ID, StepID: "qa", AgentID: "a", State: db.StepStateInterrupted},
	} {
		sr := sr
		if err := dbc.CreateStepRun(ctx, &sr); err != nil {
			t.Fatalf("create step run: %v", err)
		}
	}

	// Startup: continue the orphan, then poll while the resumed run is in flight.
	d.resumeAutoInterrupted(ctx)
	d.poll(ctx, cfg.Sources[0], adapter, time.Time{})
	time.Sleep(300 * time.Millisecond)

	ran := runner.ran()
	for _, step := range ran {
		if step != "qa" {
			t.Errorf("poll re-ran %q: completed work was discarded and the pipeline restarted (ran=%v)", step, ran)
		}
	}

	instances, err := dbc.ListWorkflowInstancesByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	fresh := 0
	for i := range instances {
		if instances[i].ID != "i-orphan" && instances[i].ResumedFrom == "" {
			fresh++
		}
	}
	if fresh != 0 {
		t.Errorf("%d fresh step-1 instance(s) dispatched alongside the resume; want 0", fresh)
	}

	// Let the in-flight runs finish before the DB is closed by t.Cleanup.
	close(runner.release)
	time.Sleep(300 * time.Millisecond)
}
