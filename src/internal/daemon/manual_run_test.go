package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
)

// manualDispatcher wires the queue harness with a source that can also fetch a
// single item, which is what an item-bound manual run needs.
func manualDispatcher(t *testing.T) (*Dispatcher, *pollableAdapter, *db.Client) {
	t.Helper()
	d, base, dbc := newQueueDispatcher(t, false)
	adapter := &pollableAdapter{mutableAdapter: *base}
	d.sources["src"] = adapter
	return d, adapter, dbc
}

// A manual run starts the workflow it names, not the one the item matches. The
// harness item carries `ai-ready`, so `impl` (label ai-implement) would never be
// routed to it by a poll.
func TestRunWorkflowManual_IgnoresTriggerMatch(t *testing.T) {
	ctx := context.Background()
	d, adapter, dbc := manualDispatcher(t)

	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{}) // binds the item
	if got := len(jobsFor(t, d, "impl")); got != 0 {
		t.Fatalf("setup: %d impl job(s) from the poll, want 0 — the item does not match its trigger", got)
	}
	task := taskForCell(t, dbc, "src", "c1")

	res, err := d.RunWorkflowManual(ctx, ManualRunRequest{WorkflowID: "impl", ItemRef: "c1"})
	if err != nil {
		t.Fatalf("manual run: %v", err)
	}
	if res.TaskID != task.ID {
		t.Errorf("bound task = %q, want the item's task %q", res.TaskID, task.ID)
	}
	if res.Standalone {
		t.Error("run reported standalone despite an item reference")
	}
	if got := len(jobsFor(t, d, "impl")); got != 1 {
		t.Fatalf("manual run produced %d impl job(s), want 1", got)
	}
	if len(res.Bypassed) == 0 {
		t.Error("result named no bypassed guards; a silent bypass is the thing to avoid")
	}
}

// "We can even start more than one, if there is one already running": two manual
// runs of the same workflow on the same task are two runs, not a duplicate the
// queue's idempotency key swallows.
func TestRunWorkflowManual_StartsConcurrentInstance(t *testing.T) {
	ctx := context.Background()
	d, adapter, dbc := manualDispatcher(t)

	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})
	if got := len(jobsFor(t, d, "triage")); got != 1 {
		t.Fatalf("setup: %d triage job(s) after the poll, want 1", got)
	}
	task := taskForCell(t, dbc, "src", "c1")

	// A live instance for the route — exactly what dropActiveMatches blocks on.
	if err := dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{
		ID: "inst-live", WorkflowID: "triage", TaskID: task.ID, CellID: "c1", SourceID: "src",
		State: db.InstanceStateRunning,
	}); err != nil {
		t.Fatalf("seed live instance: %v", err)
	}

	first, err := d.RunWorkflowManual(ctx, ManualRunRequest{WorkflowID: "triage", ItemRef: "c1"})
	if err != nil {
		t.Fatalf("first manual run: %v", err)
	}
	if !first.Concurrent {
		t.Error("Concurrent = false, want true — a running instance existed and the caller must be told")
	}
	second, err := d.RunWorkflowManual(ctx, ManualRunRequest{WorkflowID: "triage", ItemRef: "c1"})
	if err != nil {
		t.Fatalf("second manual run: %v", err)
	}
	if second.TaskID != first.TaskID {
		t.Fatalf("second run targeted task %q, want the same task %q", second.TaskID, first.TaskID)
	}

	// One job from the poll plus one per manual run, each with its own key.
	jobs := jobsFor(t, d, "triage")
	if len(jobs) != 3 {
		t.Fatalf("got %d triage job(s), want 3 (poll + two manual runs)", len(jobs))
	}
	keys := map[string]bool{}
	for _, job := range jobs {
		if keys[job.IdempotencyKey] {
			t.Fatalf("duplicate idempotency key %q — the second manual run was de-duplicated away", job.IdempotencyKey)
		}
		keys[job.IdempotencyKey] = true
	}
}

// A manual run must not clear an in-flight marker a poll owns: doing so would
// release the poll's guard early and let the next tick dispatch the cell again.
func TestRunWorkflowManual_LeavesForeignInFlightMarker(t *testing.T) {
	ctx := context.Background()
	d, adapter, _ := manualDispatcher(t)

	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})
	d.inFlight.Store("c1", time.Now()) // stand in for a poll still working the cell

	if _, err := d.RunWorkflowManual(ctx, ManualRunRequest{WorkflowID: "impl", ItemRef: "c1"}); err != nil {
		t.Fatalf("manual run: %v", err)
	}
	if _, held := d.inFlight.Load("c1"); !held {
		t.Fatal("manual run cleared the in-flight marker it did not own")
	}
}

// Without an item the run gets its own task, with no source binding.
func TestRunWorkflowManual_StandaloneCreatesTask(t *testing.T) {
	ctx := context.Background()
	d, _, dbc := manualDispatcher(t)

	res, err := d.RunWorkflowManual(ctx, ManualRunRequest{
		WorkflowID: "triage",
		Input:      map[string]any{"scope": "q3"},
		Title:      "audit run",
	})
	if err != nil {
		t.Fatalf("standalone run: %v", err)
	}
	if !res.Standalone {
		t.Error("Standalone = false for a run with no item reference")
	}
	if res.CellID != "" {
		t.Errorf("CellID = %q, want empty for a standalone run", res.CellID)
	}

	task, err := dbc.InternalTasks().GetTask(ctx, res.TaskID)
	if err != nil || task == nil {
		t.Fatalf("get created task: %v", err)
	}
	if task.Title != "audit run" {
		t.Errorf("task title = %q, want the requested title", task.Title)
	}
	if task.Metadata.Type != "manual" {
		t.Errorf("task type = %q, want %q", task.Metadata.Type, "manual")
	}
	if task.Metadata.Source != "" {
		t.Errorf("task source = %q, want empty — a standalone run has no source", task.Metadata.Source)
	}
	if got, ok := task.Input["scope"]; !ok || got != "q3" {
		t.Errorf("task input = %v, want scope=q3", task.Input)
	}
	if bindings, err := dbc.ListBindingsByTask(ctx, task.ID); err != nil {
		t.Fatalf("list bindings: %v", err)
	} else if len(bindings) != 0 {
		t.Errorf("standalone task has %d source binding(s), want 0", len(bindings))
	}

	jobs := jobsFor(t, d, "triage")
	if len(jobs) != 1 {
		t.Fatalf("standalone run produced %d job(s), want 1", len(jobs))
	}
	if jobs[0].TaskID != task.ID {
		t.Errorf("job task = %q, want the standalone task %q", jobs[0].TaskID, task.ID)
	}
}

// An unknown workflow id must fail closed: resolveWorkflow answers it with an
// empty definition, which would run an instance with no steps and report success.
func TestRunWorkflowManual_UnknownWorkflow(t *testing.T) {
	ctx := context.Background()
	d, _, _ := manualDispatcher(t)

	_, err := d.RunWorkflowManual(ctx, ManualRunRequest{WorkflowID: "nope"})
	if !errors.Is(err, ErrUnknownWorkflow) {
		t.Fatalf("error = %v, want ErrUnknownWorkflow", err)
	}
	// The message names the alternatives, which is the whole recovery path.
	if !strings.Contains(err.Error(), "triage") || !strings.Contains(err.Error(), "impl") {
		t.Errorf("error %q does not list the known workflows", err)
	}
	if jobs, _ := d.db.Queue().ListJobs(ctx, "", 100); len(jobs) != 0 {
		t.Fatalf("unknown workflow still enqueued %d job(s)", len(jobs))
	}
}

// An item reference that resolves to nothing must not silently degrade into a
// standalone run against an empty target (the mis-targeting class of #377).
func TestRunWorkflowManual_UnknownItem(t *testing.T) {
	ctx := context.Background()
	d, _, dbc := manualDispatcher(t)

	_, err := d.RunWorkflowManual(ctx, ManualRunRequest{WorkflowID: "triage", ItemRef: "ghost"})
	if !errors.Is(err, ErrUnknownCell) {
		t.Fatalf("error = %v, want ErrUnknownCell", err)
	}
	if jobs, _ := d.db.Queue().ListJobs(ctx, "", 100); len(jobs) != 0 {
		t.Fatalf("unknown item still enqueued %d job(s)", len(jobs))
	}
	if tasks, err := dbc.InternalTasks().ListTasks(ctx, 100); err != nil {
		t.Fatalf("list tasks: %v", err)
	} else if len(tasks) != 0 {
		t.Fatalf("unknown item created %d task(s), want 0", len(tasks))
	}
}

func TestManualRunHandler(t *testing.T) {
	ctx := context.Background()
	d, _, dbc := manualDispatcher(t)
	handler := d.manualRunHandler(ctx)

	t.Run("starts a standalone run", func(t *testing.T) {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodPost, "/workflows/triage/run",
			strings.NewReader(`{"title":"from http","input":{"scope":"q3"}}`)))
		if w.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
		}
		var res ManualRunResult
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if res.WorkflowID != "triage" || !res.Standalone {
			t.Fatalf("result = %+v, want a standalone triage run", res)
		}
		task, err := dbc.InternalTasks().GetTask(ctx, res.TaskID)
		if err != nil || task == nil {
			t.Fatalf("get task: %v", err)
		}
		if task.Title != "from http" {
			t.Errorf("title = %q, want the body's title", task.Title)
		}
	})

	t.Run("empty body is fine", func(t *testing.T) {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodPost, "/workflows/triage/run", nil))
		if w.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("unknown workflow is 404", func(t *testing.T) {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodPost, "/workflows/nope/run", nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", w.Code)
		}
	})

	t.Run("unknown item is 400", func(t *testing.T) {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodPost, "/workflows/triage/run?item=ghost", nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("malformed body is 400", func(t *testing.T) {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodPost, "/workflows/triage/run", strings.NewReader("{oops")))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})

	t.Run("missing the run suffix is 404", func(t *testing.T) {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodPost, "/workflows/triage", nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", w.Code)
		}
	})

	t.Run("GET is not allowed", func(t *testing.T) {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, "/workflows/triage/run", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", w.Code)
		}
	})
}

// `once: true` is spent after a completed instance; a manual run ignores it.
func TestRunWorkflowManual_IgnoresSpentOnce(t *testing.T) {
	ctx := context.Background()
	d, base, dbc := newQueueDispatcher(t, true) // triage is once: true
	adapter := &pollableAdapter{mutableAdapter: *base}
	d.sources["src"] = adapter

	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})
	job := queueJobFor(t, dbc, "triage")
	if res := d.ExecuteQueuedJob(ctx, job, "w1"); !res.Success {
		t.Fatalf("triage job failed: %+v", res)
	}
	task := taskForCell(t, dbc, "src", "c1")

	// A poll now drops the workflow: `once` is spent.
	d.poll(ctx, d.cfg.Sources[0], adapter, time.Now())
	if got := len(jobsFor(t, d, "triage")); got != 1 {
		t.Fatalf("setup: re-poll of a spent `once` workflow produced %d job(s), want 1", got)
	}

	if _, err := d.RunWorkflowManual(ctx, ManualRunRequest{WorkflowID: "triage", ItemRef: "c1"}); err != nil {
		t.Fatalf("manual run: %v", err)
	}
	if got := len(jobsFor(t, d, "triage")); got != 2 {
		t.Fatalf("manual run of a spent `once` workflow produced %d job(s) in total, want 2", got)
	}
	if got := len(instancesFor(t, dbc, task.ID, "triage")); got != 1 {
		t.Fatalf("sanity: %d instance(s) before the queued manual job runs, want 1", got)
	}
}
