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

// envRecordingRunner records the env of every RunRequest it executes, so the
// test can assert the event payload reached the agent step.
type envRecordingRunner struct {
	mu   sync.Mutex
	envs []map[string]string
}

func (r *envRecordingRunner) ID() string                     { return "recording" }
func (r *envRecordingRunner) Configure(map[string]any) error { return nil }
func (r *envRecordingRunner) Run(_ context.Context, rr model.RunRequest) (model.RunResult, error) {
	r.mu.Lock()
	r.envs = append(r.envs, rr.Env)
	r.mu.Unlock()
	return model.RunResult{Success: true}, nil
}

func newEventDispatcher(t *testing.T) (*Dispatcher, *db.Client, *envRecordingRunner) {
	t.Helper()
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{ID: "github", Type: "github"}},
		Agents:  []config.AgentConfig{{ID: "eng", Model: "test/model"}},
		Workflows: []config.WorkflowConfig{{
			ID:      "fix-feedback",
			Trigger: &config.TriggerConfig{On: config.TriggerOnPRComment, CommentMatches: "(?i)@apiary", MaxDispatches: 2},
			Steps:   []config.StepConfig{{ID: "run", Agent: "eng"}},
		}},
	}
	r, err := router.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	runner := &envRecordingRunner{}
	d := &Dispatcher{
		cfg:         cfg,
		db:          dbc,
		router:      r,
		sources:     map[string]source.Adapter{},
		runners:     map[string]runnerpkg.Runner{"agent-eng": runner},
		agentRunner: map[string]string{"eng": "claude"},
		agentSem:    map[string]chan struct{}{},
		stats:       map[string]*sourceStat{},
	}
	// Teardown order matters: a dispatch runs on its own goroutine and keeps
	// writing (execution events, outstanding counters) after routePREvent
	// returns. Closing the store first leaves those writes failing with
	// "sql: database is closed" and lets the goroutine race the next test, so
	// wait for the dispatcher to go idle before closing.
	t.Cleanup(func() {
		if !d.WaitBackground(10 * time.Second) {
			t.Error("dispatcher goroutines still running 10s after the test finished")
		}
		_ = dbc.Close()
	})
	d.binder = source.NewSourceBinder(dbc)
	return d, dbc, runner
}

func testEvent(id string) model.SourceEvent {
	return model.SourceEvent{
		ID: id, SourceID: "github", Kind: model.EventPRComment,
		PRNumber: 7, PRURL: "https://github.com/o/r/pull/7",
		Author: "alice", AuthorAssociation: "COLLABORATOR",
		Body: "@apiary fix the lint errors", SubmittedAt: time.Now(),
	}
}

// waitIdle blocks until every dispatch the test kicked off has finished, so
// assertions observe the completed run rather than a half-written one. Waiting
// on the dispatcher's own goroutines is deterministic: polling the DB for the
// instance row only proves the run *started*, and under load the agent step can
// still be pending when the row appears.
func waitIdle(t *testing.T, d *Dispatcher) {
	t.Helper()
	if !d.WaitBackground(10 * time.Second) {
		t.Fatal("dispatch did not finish within 10s")
	}
}

// waitForInstances polls until the workflow_instances count reaches want (the
// dispatch runs on its own goroutine) or the deadline passes.
func waitForInstances(t *testing.T, dbc *db.Client, want int) []db.WorkflowInstance {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		insts, err := dbc.ListWorkflowInstances(context.Background(), 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(insts) >= want || time.Now().After(deadline) {
			if len(insts) != want {
				t.Fatalf("expected %d workflow instance(s), got %d", want, len(insts))
			}
			return insts
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRoutePREvent_ExactlyOnceAndEnvPayload drives one PR comment event through
// routing twice (simulating the watermark overlap / a restart re-fetch) and
// asserts: exactly one workflow instance dispatches, the agent step receives the
// APIARY_EVENT_* / APIARY_PR_* payload, and a standalone per-PR task is bound.
func TestRoutePREvent_ExactlyOnceAndEnvPayload(t *testing.T) {
	ctx := context.Background()
	d, dbc, runner := newEventDispatcher(t)

	ev := testEvent("comment-1")
	d.routePREvent(ctx, ev)
	d.routePREvent(ctx, ev) // duplicate delivery must be a no-op

	waitIdle(t, d)
	insts := waitForInstances(t, dbc, 1)
	if insts[0].WorkflowID != "fix-feedback" {
		t.Errorf("instance workflow = %q", insts[0].WorkflowID)
	}

	// The standalone per-PR task is bound via the synthetic pr-<n> item.
	b, err := dbc.SourceBindings().GetBindingBySourceItem(ctx, "github", "pr-7")
	if err != nil || b == nil {
		t.Fatalf("expected a binding for pr-7, got %v (%v)", b, err)
	}
	if insts[0].TaskID != b.TaskID {
		t.Errorf("instance task %q != bound task %q", insts[0].TaskID, b.TaskID)
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.envs) != 1 {
		t.Fatalf("expected 1 agent run, got %d", len(runner.envs))
	}
	env := runner.envs[0]
	for k, want := range map[string]string{
		"APIARY_EVENT_KIND":   "pr_comment",
		"APIARY_EVENT_AUTHOR": "alice",
		"APIARY_EVENT_BODY":   "@apiary fix the lint errors",
		"APIARY_PR_NUMBER":    "7",
		"APIARY_PR_URL":       "https://github.com/o/r/pull/7",
	} {
		if env[k] != want {
			t.Errorf("env[%s] = %q, want %q", k, env[k], want)
		}
	}
}

// TestRoutePREvent_MaxDispatchesBudget sends distinct events on the same PR past
// the trigger's max_dispatches and asserts the budget caps dispatch.
func TestRoutePREvent_MaxDispatchesBudget(t *testing.T) {
	ctx := context.Background()
	d, dbc, _ := newEventDispatcher(t)

	d.routePREvent(ctx, testEvent("comment-1"))
	waitIdle(t, d)
	waitForInstances(t, dbc, 1)
	d.routePREvent(ctx, testEvent("comment-2"))
	waitIdle(t, d)
	waitForInstances(t, dbc, 2)

	// Budget is 2: the third event on the same PR must not dispatch.
	d.routePREvent(ctx, testEvent("comment-3"))
	waitIdle(t, d)
	waitForInstances(t, dbc, 2)
}

// TestRoutePREvent_BindsRelatedTask pre-binds an issue task and asserts an event
// whose RelatedItemID points at it dispatches on that task (lineage preserved),
// with no standalone pr-<n> task created.
func TestRoutePREvent_BindsRelatedTask(t *testing.T) {
	ctx := context.Background()
	d, dbc, _ := newEventDispatcher(t)

	issueTask, persisted := d.bindItem(ctx, model.SourceItem{
		ID: "42", SourceID: "github", Number: "#42", Title: "Fix login", State: "open",
	})
	if !persisted {
		t.Fatal("issue task must persist")
	}

	ev := testEvent("comment-9")
	ev.RelatedItemID = "42"
	d.routePREvent(ctx, ev)

	waitIdle(t, d)
	insts := waitForInstances(t, dbc, 1)
	if insts[0].TaskID != issueTask.ID {
		t.Errorf("instance task = %q, want the originating issue's task %q", insts[0].TaskID, issueTask.ID)
	}
	if b, _ := dbc.SourceBindings().GetBindingBySourceItem(ctx, "github", "pr-7"); b != nil {
		t.Error("no standalone pr-7 task must be bound when the related task exists")
	}
}
