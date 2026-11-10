package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/router"
	runnerpkg "github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/source"
	ticketpkg "github.com/orlandoburli/apiary/internal/ticket"
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
		PRNumber: 7, PRURL: "https://github.com/o/r/pull/7", PRAuthor: "carol",
		Author: "alice", AuthorAssociation: "COLLABORATOR",
		Body: "@apiary fix the lint errors", SubmittedAt: time.Now(),
	}
}

// bindTicket pre-binds a ticket task the way the normal poll→bind path would,
// so a PR event reference can resolve to it.
func bindTicket(t *testing.T, d *Dispatcher, sourceID, itemID, number, title string) model.InternalTask {
	t.Helper()
	task, persisted := d.bindItem(context.Background(), model.SourceItem{
		ID: itemID, SourceID: sourceID, Number: number, Title: title, State: "open",
	})
	if !persisted {
		t.Fatalf("ticket %s/%s must persist", sourceID, itemID)
	}
	return task
}

// assertNoPRCell asserts the synthetic per-PR task the old behaviour created was
// not bound.
func assertNoPRCell(t *testing.T, dbc *db.Client, n int) {
	t.Helper()
	if b, err := dbc.SourceBindings().GetBindingBySourceItem(context.Background(), "github", fmt.Sprintf("pr-%d", n)); err != nil || b != nil {
		t.Fatalf("a synthetic pr-%d task/binding must not be created, got %v (%v)", n, b, err)
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
// asserts: exactly one workflow instance dispatches on the ticket's task, the
// agent step receives the APIARY_EVENT_* / APIARY_PR_* payload, and no synthetic
// per-PR task is created.
func TestRoutePREvent_ExactlyOnceAndEnvPayload(t *testing.T) {
	ctx := context.Background()
	d, dbc, runner := newEventDispatcher(t)

	ticketTask := bindTicket(t, d, "jira", "10001", "PSP-348", "PSP-348 — [ask-du] ship the thing")

	ev := testEvent("comment-1")
	ev.PRTitle = "PSP-348 — [ask-du] ship the thing"
	ev.PRBranch = "feat/PSP-348-thing"
	ev.TicketRefs = []string{"PSP-348"}
	d.routePREvent(ctx, ev)
	d.routePREvent(ctx, ev) // duplicate delivery must be a no-op

	waitIdle(t, d)
	insts := waitForInstances(t, dbc, 1)
	if insts[0].WorkflowID != "fix-feedback" {
		t.Errorf("instance workflow = %q", insts[0].WorkflowID)
	}

	// The instance binds to the ticket task, not to a PR event receipt.
	if insts[0].TaskID != ticketTask.ID {
		t.Errorf("instance task %q != ticket task %q", insts[0].TaskID, ticketTask.ID)
	}
	gotTask, err := dbc.InternalTasks().GetTask(ctx, insts[0].TaskID)
	if err != nil || gotTask == nil {
		t.Fatalf("read instance task: %v (%v)", gotTask, err)
	}
	if gotTask.Title != ticketTask.Title {
		t.Errorf("instance task title = %q, want the ticket's %q", gotTask.Title, ticketTask.Title)
	}
	assertNoPRCell(t, dbc, 7)

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.envs) != 1 {
		t.Fatalf("expected 1 agent run, got %d", len(runner.envs))
	}
	env := runner.envs[0]
	for k, want := range map[string]string{
		"APIARY_EVENT_KIND":      "pr_comment",
		"APIARY_EVENT_AUTHOR":    "alice",
		"APIARY_EVENT_PR_AUTHOR": "carol",
		"APIARY_EVENT_BODY":      "@apiary fix the lint errors",
		"APIARY_EVENT_TICKET":    "PSP-348",
		"APIARY_EVENT_PR_TITLE":  "PSP-348 — [ask-du] ship the thing",
		"APIARY_EVENT_PR_BRANCH": "feat/PSP-348-thing",
		"APIARY_PR_NUMBER":       "7",
		"APIARY_PR_URL":          "https://github.com/o/r/pull/7",
	} {
		if env[k] != want {
			t.Errorf("env[%s] = %q, want %q", k, env[k], want)
		}
	}
}

// TestRoutePREvent_ResolvesTicketFromTitle checks the title is the first place a
// ticket key is looked for.
func TestRoutePREvent_ResolvesTicketFromTitle(t *testing.T) {
	d, dbc, _ := newEventDispatcher(t)
	ticketTask := bindTicket(t, d, "jira", "10001", "PSP-348", "PSP-348 — [ask-du] ship the thing")

	ev := testEvent("comment-1")
	ev.PRTitle = "[ask-du] PSP-348 ship the thing"
	ev.TicketRefs = ticketpkg.Refs(ev.PRTitle, "", "")

	d.routePREvent(context.Background(), ev)
	waitIdle(t, d)
	insts := waitForInstances(t, dbc, 1)
	if insts[0].TaskID != ticketTask.ID {
		t.Errorf("instance task = %q, want ticket task %q", insts[0].TaskID, ticketTask.ID)
	}
	assertNoPRCell(t, dbc, 7)
}

// TestRoutePREvent_ResolvesTicketFromBranch checks the head branch is the second
// place a ticket key is looked for.
func TestRoutePREvent_ResolvesTicketFromBranch(t *testing.T) {
	d, dbc, _ := newEventDispatcher(t)
	ticketTask := bindTicket(t, d, "jira", "10001", "PSP-348", "PSP-348 — [ask-du] ship the thing")

	ev := testEvent("comment-1")
	ev.PRBranch = "feat/PSP-348-thing"
	ev.TicketRefs = ticketpkg.Refs("", ev.PRBranch, "")

	d.routePREvent(context.Background(), ev)
	waitIdle(t, d)
	insts := waitForInstances(t, dbc, 1)
	if insts[0].TaskID != ticketTask.ID {
		t.Errorf("instance task = %q, want ticket task %q", insts[0].TaskID, ticketTask.ID)
	}
	assertNoPRCell(t, dbc, 7)
}

// TestRoutePREvent_ResolvesTicketFromBody checks the PR body is the last place a
// ticket key is looked for.
func TestRoutePREvent_ResolvesTicketFromBody(t *testing.T) {
	d, dbc, _ := newEventDispatcher(t)
	ticketTask := bindTicket(t, d, "jira", "10001", "PSP-348", "PSP-348 — [ask-du] ship the thing")

	ev := testEvent("comment-1")
	ev.PRBody = "Tracks PSP-348."
	ev.TicketRefs = ticketpkg.Refs("", "", ev.PRBody)

	d.routePREvent(context.Background(), ev)
	waitIdle(t, d)
	insts := waitForInstances(t, dbc, 1)
	if insts[0].TaskID != ticketTask.ID {
		t.Errorf("instance task = %q, want ticket task %q", insts[0].TaskID, ticketTask.ID)
	}
	assertNoPRCell(t, dbc, 7)
}

// TestRoutePREvent_DropsEventWithoutTicket pins the new default: no ticket key
// means no task. The event must not create a pr-<n> row or dispatch.
func TestRoutePREvent_DropsEventWithoutTicket(t *testing.T) {
	d, dbc, _ := newEventDispatcher(t)

	ev := testEvent("comment-1")
	ev.PRTitle = "fix the lint errors" // no key anywhere
	ev.PRBody = "Closes #42"           // a GitHub ref, not a ticket key
	ev.TicketRefs = ticketpkg.Refs(ev.PRTitle, ev.PRBranch, ev.PRBody)

	if task, ref, reason := d.resolveEventTask(context.Background(), ev); task != nil || ref != "" || !strings.Contains(reason, "no ticket reference") {
		t.Fatalf("resolveEventTask = (%v, %q, %q), want no task, no ref and a no-ticket-reference reason", task, ref, reason)
	}

	d.routePREvent(context.Background(), ev)
	waitIdle(t, d)
	waitForInstances(t, dbc, 0)
	assertNoPRCell(t, dbc, 7)
}

// TestRoutePREvent_AmbiguousTicketReferenceDropsEvent pins the ambiguity rule: a
// reference bound to two distinct items is dropped (never guessed at), with a
// reason naming the candidates.
func TestRoutePREvent_AmbiguousTicketReferenceDropsEvent(t *testing.T) {
	d, dbc, _ := newEventDispatcher(t)
	bindTicket(t, d, "jira", "10001", "PSP-348", "PSP-348 one")
	bindTicket(t, d, "other", "20002", "PSP-348", "PSP-348 two")

	ev := testEvent("comment-1")
	ev.TicketRefs = []string{"PSP-348"}

	task, ref, reason := d.resolveEventTask(context.Background(), ev)
	if task != nil || ref != "" || !strings.Contains(reason, "ambiguous") {
		t.Fatalf("resolveEventTask = (%v, %q, %q), want no task, no ref and an ambiguous reason", task, ref, reason)
	}

	d.routePREvent(context.Background(), ev)
	waitIdle(t, d)
	waitForInstances(t, dbc, 0)
	assertNoPRCell(t, dbc, 7)
}

// TestRoutePREvent_SkipsUnboundFalsePositiveCandidate checks that a leading
// token that looks like a key but is not bound ("UTF-8") does not hide the real
// ticket key later in the title.
func TestRoutePREvent_SkipsUnboundFalsePositiveCandidate(t *testing.T) {
	d, dbc, _ := newEventDispatcher(t)
	ticketTask := bindTicket(t, d, "jira", "10001", "PSP-348", "PSP-348 — [ask-du] ship the thing")

	ev := testEvent("comment-1")
	ev.PRTitle = "Fix UTF-8 decoding for PSP-348"
	ev.TicketRefs = ticketpkg.Refs(ev.PRTitle, "", "")

	d.routePREvent(context.Background(), ev)
	waitIdle(t, d)
	insts := waitForInstances(t, dbc, 1)
	if insts[0].TaskID != ticketTask.ID {
		t.Errorf("instance task = %q, want ticket task %q (the unbound UTF-8 candidate must be skipped)", insts[0].TaskID, ticketTask.ID)
	}
}

// TestRoutePREvent_MaxDispatchesBudget sends distinct events on the same PR past
// the trigger's max_dispatches and asserts the budget caps dispatch.
func TestRoutePREvent_MaxDispatchesBudget(t *testing.T) {
	ctx := context.Background()
	d, dbc, _ := newEventDispatcher(t)
	bindTicket(t, d, "jira", "10001", "PSP-348", "PSP-348 — ship the thing")

	for i := 1; i <= 3; i++ {
		ev := testEvent(fmt.Sprintf("comment-%d", i))
		ev.TicketRefs = []string{"PSP-348"}
		d.routePREvent(ctx, ev)
		waitIdle(t, d)
		if i <= 2 {
			waitForInstances(t, dbc, i)
		}
	}

	// Budget is 2: the third event on the same PR must not dispatch.
	waitForInstances(t, dbc, 2)
}

// TestRoutePREvent_BindsRelatedTask pre-binds an issue task and asserts an event
// whose RelatedItemID points at it dispatches on that task (lineage preserved),
// with no standalone pr-<n> task created.
func TestRoutePREvent_BindsRelatedTask(t *testing.T) {
	ctx := context.Background()
	d, dbc, runner := newEventDispatcher(t)

	issueTask, persisted := d.bindItem(ctx, model.SourceItem{
		ID: "42", SourceID: "github", Number: "#42", Title: "Fix login", State: "open",
	})
	if !persisted {
		t.Fatal("issue task must persist")
	}

	ev := testEvent("comment-9")
	ev.RelatedItemID = "42"

	// A "Closes #42" resolution exposes the issue's human reference, never the
	// PR number.
	_, ref, reason := d.resolveEventTask(ctx, ev)
	if ref != "#42" || reason != "" {
		t.Fatalf("resolveEventTask ref = %q (reason %q), want #42", ref, reason)
	}

	d.routePREvent(ctx, ev)

	waitIdle(t, d)
	insts := waitForInstances(t, dbc, 1)
	if insts[0].TaskID != issueTask.ID {
		t.Errorf("instance task = %q, want the originating issue's task %q", insts[0].TaskID, issueTask.ID)
	}
	if b, _ := dbc.SourceBindings().GetBindingBySourceItem(ctx, "github", "pr-7"); b != nil {
		t.Error("no standalone pr-7 task must be bound when the related task exists")
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.envs) != 1 {
		t.Fatalf("expected 1 agent run, got %d", len(runner.envs))
	}
	if got := runner.envs[0]["APIARY_EVENT_TICKET"]; got != "#42" {
		t.Errorf("APIARY_EVENT_TICKET = %q, want #42 (the related issue, not PR #7)", got)
	}
}

func TestPREventScope_ExposesPRAuthor(t *testing.T) {
	if got := prEventScope(testEvent("comment-1"), "")["pr_author"]; got != "carol" {
		t.Errorf("event scope pr_author = %q, want carol", got)
	}
}

// TestRoutePREvent_ExposesBoundTicketNotLookalike pins #492's crux: the ticket
// the agent sees is the one the instance was bound to, not the first candidate
// in the PR text. The unbound "UTF-8" lookalike precedes the real key, and the
// env must still carry PSP-348.
func TestRoutePREvent_ExposesBoundTicketNotLookalike(t *testing.T) {
	ctx := context.Background()
	d, _, runner := newEventDispatcher(t)
	bindTicket(t, d, "jira", "10001", "PSP-348", "PSP-348 — [ask-du] ship the thing")

	ev := testEvent("comment-1")
	ev.PRTitle = "Fix UTF-8 decoding for PSP-348"
	ev.TicketRefs = ticketpkg.Refs(ev.PRTitle, "", "")

	task, ref, reason := d.resolveEventTask(ctx, ev)
	if task == nil || ref != "PSP-348" || reason != "" {
		t.Fatalf("resolveEventTask = (task %v, ref %q, reason %q), want the bound PSP-348", task, ref, reason)
	}

	d.routePREvent(ctx, ev)
	waitIdle(t, d)

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.envs) != 1 {
		t.Fatalf("expected 1 agent run, got %d", len(runner.envs))
	}
	if got := runner.envs[0]["APIARY_EVENT_TICKET"]; got != "PSP-348" {
		t.Errorf("APIARY_EVENT_TICKET = %q, want PSP-348 (the bound key, not the UTF-8 lookalike)", got)
	}
}

// TestPREventScope_ExposesResolvedTicket checks the scope and env expose the
// resolved reference and the PR metadata consistently.
func TestPREventScope_ExposesResolvedTicket(t *testing.T) {
	ev := testEvent("comment-1")
	ev.PRTitle = "Fix UTF-8 decoding for PSP-348"
	ev.PRBranch = "feat/PSP-348-thing"

	scope := prEventScope(ev, "PSP-348")
	for k, want := range map[string]string{
		"ticket":    "PSP-348",
		"pr_title":  "Fix UTF-8 decoding for PSP-348",
		"pr_branch": "feat/PSP-348-thing",
	} {
		if got := scope[k]; got != want {
			t.Errorf("event scope %s = %q, want %q", k, got, want)
		}
	}

	env := prEventEnv(ev, "PSP-348")
	for k, want := range map[string]string{
		"APIARY_EVENT_TICKET":    "PSP-348",
		"APIARY_EVENT_PR_TITLE":  "Fix UTF-8 decoding for PSP-348",
		"APIARY_EVENT_PR_BRANCH": "feat/PSP-348-thing",
	} {
		if got := env[k]; got != want {
			t.Errorf("env %s = %q, want %q", k, got, want)
		}
	}
}

// An unresolved reference must stay empty rather than panic or leak a stale
// value; the accessor then resolves as missing ("").
func TestPREventScope_EmptyTicketWhenUnresolved(t *testing.T) {
	if got, ok := prEventScope(testEvent("comment-1"), "")["ticket"]; !ok || got != "" {
		t.Errorf("event scope ticket = (%q, present=%v), want an empty value", got, ok)
	}
	if got, ok := prEventEnv(testEvent("comment-1"), "")["APIARY_EVENT_TICKET"]; !ok || got != "" {
		t.Errorf("APIARY_EVENT_TICKET = (%q, present=%v), want an empty value", got, ok)
	}
}
