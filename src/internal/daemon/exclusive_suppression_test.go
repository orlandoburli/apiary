package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/router"
	runnerpkg "github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/source"
)

// exclusiveSuppressionFixture builds a dispatcher over two workflows that both
// match the same item: `decompose` is exclusive AND `once: true`, so it claims the
// task alone; `review` sits one priority below it and never gets evaluated while
// the exclusive one matches.
//
// The task already has a completed `decompose` instance, which is exactly the
// state where the gap shows: RouteAll returns [decompose], dropOnceMatches removes
// it, and the poll ends with zero matches even though `review` matched.
func exclusiveSuppressionFixture(t *testing.T) (*Dispatcher, *db.Client, *recordingAdapter, config.SourceConfig, string) {
	t.Helper()
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "exclusive.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	item := model.SourceItem{ID: "ISSUE-9", SourceID: "src", Number: "#9", Title: "Ship it", State: "todo"}
	adapter := &recordingAdapter{items: []model.SourceItem{item}}

	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{ID: "src", Type: "fake"}},
		Agents:  []config.AgentConfig{{ID: "a", Model: "test/model"}},
		Workflows: []config.WorkflowConfig{
			{ID: "decompose",
				Trigger: &config.TriggerConfig{Priority: 10, Exclusive: true, Once: true, Match: config.RouteMatch{Source: "src"}},
				Steps:   []config.StepConfig{{ID: "split", Agent: "a"}}},
			{ID: "review",
				Trigger: &config.TriggerConfig{Priority: 20, Match: config.RouteMatch{Source: "src"}},
				Steps:   []config.StepConfig{{ID: "look", Agent: "a"}}},
		},
	}
	rt, err := router.New(cfg)
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	d := &Dispatcher{
		cfg:         cfg,
		db:          dbc,
		router:      rt,
		sources:     map[string]source.Adapter{"src": adapter},
		runners:     map[string]runnerpkg.Runner{"agent-a": &stepRecordingRunner{}},
		agentRunner: map[string]string{"a": "claude"},
		agentSem:    map[string]chan struct{}{},
		stats:       map[string]*sourceStat{"src": {}},
	}
	d.binder = source.NewSourceBinder(dbc)

	// Bind the item exactly as a poll would, then record the spent `once` run so
	// the guard has something to drop on the next poll.
	task, err := d.binder.Bind(ctx, item)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{
		ID: "i-done", WorkflowID: "decompose", CellID: item.ID, SourceID: "src",
		TaskID: task.ID, State: db.InstanceStateDone,
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	return d, dbc, adapter, cfg.Sources[0], task.ID
}

// An exclusive trigger that a pre-dispatch guard later removes leaves the task
// with nothing to run, and the routes it suppressed are deliberately NOT
// reconsidered — re-routing to them would start work the exclusive trigger exists
// to prevent. The INFO report must therefore name them, so the operator can see
// that a lower-priority workflow matched and was suppressed rather than reading
// "nothing will run" as "nothing matched".
func TestPoll_ExclusiveWinnerDroppedNamesSuppressedRoutes(t *testing.T) {
	logs := captureLogs(t)
	d, dbc, adapter, sc, taskID := exclusiveSuppressionFixture(t)
	ctx := context.Background()

	if err := d.poll(ctx, sc, adapter, time.Time{}); err != nil {
		t.Fatalf("poll: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	var info string
	for _, line := range logs() {
		if strings.HasPrefix(line, "INFO ") && strings.Contains(line, "dropped before dispatch") {
			info = line
		}
	}
	if info == "" {
		t.Fatalf("a fully-dropped dispatch must log at INFO; got %v", logs())
	}
	for _, want := range []string{"decompose", "once", "review", "suppressed"} {
		if !strings.Contains(info, want) {
			t.Errorf("INFO line must mention %q so the suppression is visible; got %q", want, info)
		}
	}

	// The diagnostic must not become a behaviour change: nothing may be dispatched
	// for the suppressed route.
	instances, err := dbc.ListWorkflowInstancesByTask(ctx, taskID)
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	for i := range instances {
		if instances[i].WorkflowID != "decompose" {
			t.Fatalf("suppressed route %q was dispatched; the exclusive claim must still hold", instances[i].WorkflowID)
		}
	}

	// The same fact is queryable per task: the exclusive workflow's drop event
	// carries the routes it suppressed.
	events, err := dbc.ListExecutionEvents(ctx, db.ExecutionEventFilter{TaskID: taskID, Type: "dispatch.dropped"})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 dispatch.dropped event, got %d", len(events))
	}
	if got, _ := events[0].Metadata["suppressed"].(string); got != "review" {
		t.Errorf("dispatch.dropped metadata must name the suppressed routes, got %q", got)
	}
}

// Without an exclusive winner the report is unchanged: no suppression note, no
// suppression metadata. Guards against the diagnostic leaking into ordinary drops.
func TestReportFullyDropped_NoSuppressionNoteWithoutExclusive(t *testing.T) {
	logs := captureLogs(t)
	ctx := context.Background()
	dbc := openDropTestDB(t, "no-suppression.db")
	d := &Dispatcher{db: dbc}

	d.reportFullyDropped(ctx, model.SourceItem{ID: "ISSUE-9", SourceID: "src"}, model.InternalTask{ID: "T1"},
		[]droppedMatch{{WorkflowID: "impl", Reason: "active instance"}}, suppressedRoutes{})

	for _, line := range logs() {
		if strings.Contains(line, "dropped before dispatch") && strings.Contains(line, "suppressed") {
			t.Fatalf("no exclusive route matched, so nothing was suppressed; got %q", line)
		}
	}
	events, err := dbc.ListExecutionEvents(ctx, db.ExecutionEventFilter{TaskID: "T1", Type: "dispatch.dropped"})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 dispatch.dropped event, got %d", len(events))
	}
	if _, ok := events[0].Metadata["suppressed"]; ok {
		t.Errorf("suppressed metadata must be absent when nothing was suppressed: %+v", events[0].Metadata)
	}
}

// The repeat-suppression signature must account for the suppressed set: a config
// change that adds a fallback below the exclusive route changes the picture the
// operator needs, so it has to be reported rather than swallowed as a repeat.
func TestDropSignature_ChangesWithSuppressedRoutes(t *testing.T) {
	dropped := []droppedMatch{{WorkflowID: "decompose", Reason: "once"}}
	bare := dropSignature(dropped, suppressedRoutes{})
	with := dropSignature(dropped, suppressedRoutes{ExclusiveWorkflowID: "decompose", WorkflowIDs: []string{"review"}})
	if bare == with {
		t.Fatalf("signature must distinguish a suppressed fallback from none, both %q", bare)
	}
}
