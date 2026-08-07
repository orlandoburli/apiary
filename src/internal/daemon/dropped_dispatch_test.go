package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/queue"
	"github.com/orlandoburli/apiary/internal/router"
)

// captureLogs redirects the global log sink for the duration of a test and
// returns an accessor for everything logged. The sink is process-global, so
// tests that use it must not run in parallel.
func captureLogs(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var lines []string
	aplog.SetSink(func(level, msg string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, level+" "+msg)
	})
	t.Cleanup(func() { aplog.SetSink(nil) })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}
}

func openDropTestDB(t *testing.T, name string) *db.Client {
	t.Helper()
	dbc, err := db.New(context.Background(), filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })
	return dbc
}

// TestReportFullyDroppedIsVisible is the core regression for #380: when every
// matched workflow is removed by a pre-dispatch guard, nothing runs and no
// instance is created — that outcome must produce an INFO line naming the task,
// the workflow and the drop reason, plus a queryable `dispatch.dropped` event.
// It used to be reported only as a DEBUG "no matching route" line that did not
// even distinguish "nothing matched" from "everything was dropped", so a wedged
// task was indistinguishable from an idle one.
func TestReportFullyDroppedIsVisible(t *testing.T) {
	ctx := context.Background()
	dbc := openDropTestDB(t, "dropped.db")
	logs := captureLogs(t)

	// jira-implement already completed; its trigger is once: true, so the guard
	// removes it and the task has nothing left to dispatch.
	_ = dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{ID: "i1", WorkflowID: "jira-implement", CellID: "295651", TaskID: "T1", State: db.InstanceStateDone})

	d := &Dispatcher{db: dbc}
	matches := []router.Match{{Route: config.RouteConfig{ID: "jira-implement", Once: true}}}

	kept, dropped := d.dropOnceMatches(ctx, "T1", matches)
	if len(kept) != 0 {
		t.Fatalf("expected the once-route to be dropped, kept %d", len(kept))
	}
	if len(dropped) != 1 || dropped[0].WorkflowID != "jira-implement" || dropped[0].Reason != "once" {
		t.Fatalf("guard must report which workflow it dropped and why; got %+v", dropped)
	}

	cell := model.SourceItem{ID: "295651", SourceID: "rl-jira", Title: "PSP-199"}
	task := model.InternalTask{ID: "T1"}
	d.reportFullyDropped(ctx, cell, task, dropped, suppressedRoutes{})

	var info string
	for _, line := range logs() {
		if strings.HasPrefix(line, "INFO ") && strings.Contains(line, "dropped before dispatch") {
			info = line
		}
	}
	if info == "" {
		t.Fatalf("a fully-dropped dispatch must log at INFO; got %v", logs())
	}
	for _, want := range []string{"T1", "jira-implement", "once", "295651"} {
		if !strings.Contains(info, want) {
			t.Errorf("INFO line must name %q; got %q", want, info)
		}
	}

	events, err := dbc.ListExecutionEvents(ctx, db.ExecutionEventFilter{TaskID: "T1", Type: "dispatch.dropped"})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 dispatch.dropped event, got %d", len(events))
	}
	if events[0].WorkflowID != "jira-implement" || events[0].Metadata["reason"] != "once" {
		t.Errorf("event must carry workflow and reason; got %+v", events[0])
	}
}

// TestReportFullyDroppedSuppressesUnchangedRepeats keeps the new INFO line
// useful. A task whose single workflow is running is "fully dropped" on every
// poll; re-logging that every poll interval would bury the signal it exists to
// provide. The line is emitted when the reason set first appears and again only
// when it changes.
func TestReportFullyDroppedSuppressesUnchangedRepeats(t *testing.T) {
	ctx := context.Background()
	dbc := openDropTestDB(t, "dropped-repeat.db")
	logs := captureLogs(t)

	d := &Dispatcher{db: dbc}
	cell := model.SourceItem{ID: "295651", SourceID: "rl-jira", Title: "PSP-199"}
	task := model.InternalTask{ID: "T1"}

	active := []droppedMatch{{WorkflowID: "jira-implement", Reason: "active instance"}}
	d.reportFullyDropped(ctx, cell, task, active, suppressedRoutes{})
	d.reportFullyDropped(ctx, cell, task, active, suppressedRoutes{})
	d.reportFullyDropped(ctx, cell, task, active, suppressedRoutes{})

	count := func() int {
		n := 0
		for _, line := range logs() {
			if strings.Contains(line, "dropped before dispatch") {
				n++
			}
		}
		return n
	}
	if got := count(); got != 1 {
		t.Fatalf("an unchanged reason set must be reported once, got %d lines", got)
	}

	// A different reason is new information and must be reported.
	d.reportFullyDropped(ctx, cell, task, []droppedMatch{{WorkflowID: "jira-implement", Reason: "capped"}}, suppressedRoutes{})
	if got := count(); got != 2 {
		t.Fatalf("a changed reason must be reported again, got %d lines", got)
	}

	// After a successful dispatch clears the marker, the same reason is news again.
	d.dropNotified.Delete("T1")
	d.reportFullyDropped(ctx, cell, task, active, suppressedRoutes{})
	if got := count(); got != 3 {
		t.Fatalf("a re-wedge after a dispatch must be reported, got %d lines", got)
	}
}

// TestExecuteQueuedJobSkipIsVisible covers the queue half of #380. A job the
// dispatcher declines to execute (a redelivery of an already-completed workflow,
// or a `once: true` route) still finishes as succeeded — but it must say so at
// INFO, record a dispatch.dropped event, and carry a Note that lands on the
// attempt row, so "succeeded and did nothing" is distinguishable from
// "succeeded and ran a workflow" without inventing a new queue job state.
func TestExecuteQueuedJobSkipIsVisible(t *testing.T) {
	ctx := context.Background()
	dbc := openDropTestDB(t, "queue-skip.db")
	logs := captureLogs(t)

	_ = dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{ID: "i1", WorkflowID: "jira-implement", CellID: "295651", TaskID: "T1", State: db.InstanceStateDone})

	d := &Dispatcher{db: dbc}
	payload, err := json.Marshal(dispatchJobPayload{
		Version: dispatchPayloadVersion,
		Cell:    model.SourceItem{ID: "295651", SourceID: "rl-jira"},
		Task:    model.InternalTask{ID: "T1"},
		Match:   router.Match{Route: config.RouteConfig{ID: "jira-implement", Once: true}},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	result := d.ExecuteQueuedJob(ctx, queue.Job{ID: "job-1", TaskID: "T1", WorkflowID: "jira-implement", AttemptCount: 1, PayloadVersion: dispatchPayloadVersion, Payload: payload}, "worker-1")
	if !result.Success {
		t.Fatalf("a skipped job must still finish cleanly; got %+v", result)
	}
	if !strings.HasPrefix(result.Note, "skipped:") {
		t.Errorf("a skipped job must carry an explanatory Note; got %q", result.Note)
	}

	var found bool
	for _, line := range logs() {
		if strings.HasPrefix(line, "INFO ") && strings.Contains(line, "without creating an instance") {
			found = true
		}
	}
	if !found {
		t.Errorf("a skipped queue job must log at INFO; got %v", logs())
	}

	events, err := dbc.ListExecutionEvents(ctx, db.ExecutionEventFilter{TaskID: "T1", Type: "dispatch.dropped"})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 dispatch.dropped event for the skipped job, got %d", len(events))
	}
}

// TestWarnUnsatisfiableQueueJobsWarnsOncePerJob covers the #375 residue: a job
// enqueued after startup that no registered worker can lease sits queued forever
// while `apiary status` shows a healthy idle worker. The check now runs on a
// timer instead of only at boot, so it must warn once per job (not once per
// tick) and forget a job that becomes leasable again.
func TestWarnUnsatisfiableQueueJobsWarnsOncePerJob(t *testing.T) {
	ctx := context.Background()
	dbc := openDropTestDB(t, "unsatisfiable.db")
	logs := captureLogs(t)

	store := dbc.Queue()
	if err := store.RegisterWorker(ctx, &queue.Worker{ID: "worker-1", ProtocolVersion: queue.WorkerProtocolVersion, Pool: "default", Capabilities: []string{"apiary.workflow"}, Capacity: 1, Ready: true}); err != nil {
		t.Fatalf("register worker: %v", err)
	}
	job := &queue.Job{IdempotencyKey: "T1:0:jira-implement", TaskID: "T1", WorkflowID: "jira-implement", Pool: "default", RequiredCapabilities: []string{"apiary.workflow", "runner:cursor-cli"}, PayloadVersion: 1, Payload: []byte(`{}`)}
	if _, err := store.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	d := &Dispatcher{db: dbc, dispatchQueue: store}
	d.warnUnsatisfiableQueueJobs(ctx)
	d.warnUnsatisfiableQueueJobs(ctx)
	d.warnUnsatisfiableQueueJobs(ctx)

	warnings := 0
	for _, line := range logs() {
		if strings.HasPrefix(line, "WARN ") && strings.Contains(line, "not satisfiable by any registered worker") {
			warnings++
		}
	}
	if warnings != 1 {
		t.Fatalf("an unleasable job must be warned about once per job, got %d warnings", warnings)
	}

	events, err := dbc.ListExecutionEvents(ctx, db.ExecutionEventFilter{TaskID: "T1", Type: "dispatch.dropped"})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 || events[0].Metadata["reason"] != "unsatisfiable by any worker" {
		t.Fatalf("expected one unsatisfiable dispatch.dropped event, got %+v", events)
	}
}

// TestDropActiveMatchesTreatsInterruptedAsTerminal is a CONTROL for #380's
// second claim ("dropActiveMatches may treat interrupted as non-terminal, so a
// single restart wedges a task forever"). It passes both before and after this
// change: only running / approval_waiting / waiting shadow a workflow, and an
// instance stranded 'interrupted' by a restart stays eligible for re-dispatch.
func TestDropActiveMatchesTreatsInterruptedAsTerminal(t *testing.T) {
	ctx := context.Background()
	dbc := openDropTestDB(t, "interrupted.db")

	_ = dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{ID: "i1", WorkflowID: "jira-implement", CellID: "297869", TaskID: "T2", State: db.InstanceStateInterrupted})

	d := &Dispatcher{db: dbc}
	kept, dropped := d.dropActiveMatches(ctx, "T2", []router.Match{{Route: config.RouteConfig{ID: "jira-implement"}}})
	if len(kept) != 1 {
		t.Fatalf("an interrupted instance is terminal and must not shadow a re-dispatch; kept %d, dropped %+v", len(kept), dropped)
	}
}

// TestReconcileOrphanTaskCountersHealsPhantomOutstanding is a CONTROL for
// #380's phantom `outstanding_workflows` claim. It passes both before and after
// this change: the startup sweep recounts the counter from live instances for
// every non-terminal task state, so a counter left inflated by a dispatch that
// never produced an instance is reset to zero on the next daemon start.
func TestReconcileOrphanTaskCountersHealsPhantomOutstanding(t *testing.T) {
	ctx := context.Background()
	dbc := openDropTestDB(t, "counters.db")
	tasks := dbc.InternalTasks()

	task := &model.InternalTask{ID: "T3", Title: "PSP-199", State: model.TaskStateRunning}
	if err := tasks.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := tasks.IncrementOutstanding(ctx, "T3", 1); err != nil {
		t.Fatalf("increment: %v", err)
	}
	// Both instances are terminal: the counter is a phantom.
	_ = dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{ID: "i1", WorkflowID: "jira-assigned-to-me", CellID: "295651", TaskID: "T3", State: db.InstanceStateDone})
	_ = dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{ID: "i2", WorkflowID: "jira-implement", CellID: "295651", TaskID: "T3", State: db.InstanceStateDone})

	if _, _, err := dbc.ReconcileOrphanTaskCounters(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	healed, err := tasks.GetTask(ctx, "T3")
	if err != nil || healed == nil {
		t.Fatalf("get task: %v", err)
	}
	if healed.OutstandingWorkflows != 0 {
		t.Errorf("outstanding counter must self-heal to 0 when no instance is live; got %d", healed.OutstandingWorkflows)
	}
}
