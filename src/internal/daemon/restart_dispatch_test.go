package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/queue"
)

// pollableAdapter is a mutableAdapter that also satisfies source.TaskPoller, which
// ForceRestart needs to fetch the item it re-routes.
type pollableAdapter struct{ mutableAdapter }

func (a *pollableAdapter) PollTask(_ context.Context, id string) (model.SourceItem, error) {
	for _, item := range a.items {
		if item.ID == id {
			return item, nil
		}
	}
	return model.SourceItem{}, nil
}

func jobsFor(t *testing.T, d *Dispatcher, workflowID string) []queue.Job {
	t.Helper()
	all, err := d.db.Queue().ListJobs(context.Background(), "", 100)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	var out []queue.Job
	for _, job := range all {
		if job.WorkflowID == workflowID {
			out = append(out, job)
		}
	}
	return out
}

// TestForceRestart_DispatchesImmediately is the headline behaviour: restart must
// produce a new run right away. It used to only reset state and leave the actual
// re-dispatch to the next poll tick, so for up to a full poll_interval a restart
// had no observable effect at all — the dashboard's R looked like a no-op.
func TestForceRestart_DispatchesImmediately(t *testing.T) {
	ctx := context.Background()
	d, base, dbc := newQueueDispatcher(t, false)
	adapter := &pollableAdapter{mutableAdapter: *base}
	d.sources["src"] = adapter

	// One normal poll puts the cell in flight with a queued job.
	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})
	if got := len(jobsFor(t, d, "triage")); got != 1 {
		t.Fatalf("setup: %d triage job(s) after the first poll, want 1", got)
	}
	task := taskForCell(t, dbc, "src", "c1")

	res, err := d.ForceRestart(ctx, "c1")
	if err != nil {
		t.Fatalf("ForceRestart: %v", err)
	}

	if res.Dispatched != 1 {
		t.Fatalf("restart dispatched %d workflow(s), want 1 — restart must not wait for the next poll", res.Dispatched)
	}
	if len(res.Workflows) != 1 || res.Workflows[0] != "triage" {
		t.Errorf("restart dispatched %v, want [triage]", res.Workflows)
	}

	// A fresh job exists, and the interrupted round's job is no longer claimable.
	jobs := jobsFor(t, d, "triage")
	if len(jobs) != 2 {
		t.Fatalf("after restart there are %d triage job(s), want 2 (the interrupted one plus the new one)", len(jobs))
	}
	var live, canceled int
	for _, job := range jobs {
		if job.State == queue.JobCanceled {
			canceled++
			continue
		}
		live++
	}
	if canceled != 1 {
		t.Errorf("restart left %d cancelled job(s), want 1 — the interrupted round's job stays claimable and would double-run", canceled)
	}
	if live != 1 {
		t.Errorf("restart left %d live job(s), want exactly 1", live)
	}

	// The generation moved, which is what frees the idempotency key.
	gen, err := dbc.InternalTasks().Generation(ctx, task.ID)
	if err != nil {
		t.Fatalf("generation: %v", err)
	}
	if gen == 0 {
		t.Error("dispatch generation still 0 after restart — the re-enqueue would collide with the restarted round's key")
	}
}

// TestForceRestart_BumpsGenerationForRunningTask covers the reason restart could
// not dispatch on the queue path at all. Queue jobs are keyed
// taskID:generation:routeID, and the generation only advances implicitly for a
// task in done/failed. A force-restarted task is typically still *running* — the
// exact case restart exists for — so its generation never moved and every
// re-enqueue was swallowed as a duplicate of the round being restarted.
func TestForceRestart_BumpsGenerationForRunningTask(t *testing.T) {
	ctx := context.Background()
	d, base, dbc := newQueueDispatcher(t, false)
	adapter := &pollableAdapter{mutableAdapter: *base}
	d.sources["src"] = adapter

	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})
	task := taskForCell(t, dbc, "src", "c1")
	if task.State == model.TaskStateDone || task.State == model.TaskStateFailed {
		t.Fatalf("setup: task is %q; this test needs the running case", task.State)
	}
	before, _ := dbc.InternalTasks().Generation(ctx, task.ID)

	if _, err := d.ForceRestart(ctx, "c1"); err != nil {
		t.Fatalf("ForceRestart: %v", err)
	}

	after, _ := dbc.InternalTasks().Generation(ctx, task.ID)
	if after <= before {
		t.Fatalf("generation went %d → %d, want it to advance for a running task", before, after)
	}

	// Distinct keys are the observable consequence: same task, same route, two
	// dispatchable jobs.
	keys := map[string]bool{}
	for _, job := range jobsFor(t, d, "triage") {
		if keys[job.IdempotencyKey] {
			t.Fatalf("duplicate idempotency key %q — the re-dispatch was deduplicated away", job.IdempotencyKey)
		}
		keys[job.IdempotencyKey] = true
	}
	if len(keys) != 2 {
		t.Errorf("got %d distinct job key(s), want 2", len(keys))
	}
}

// TestForceRestart_OverridesOnceGuard: `once: true` stops automatic re-polling
// from re-running a completed workflow, but an explicit human restart is the
// documented escape hatch — and a once-only workflow is one of the states people
// actually reach for restart from. The override is reported, never silent.
func TestForceRestart_OverridesOnceGuard(t *testing.T) {
	ctx := context.Background()
	d, base, dbc := newQueueDispatcher(t, true) // triage is once:true
	adapter := &pollableAdapter{mutableAdapter: *base}
	d.sources["src"] = adapter

	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})
	first := queueJobFor(t, dbc, "triage")
	if res := d.ExecuteQueuedJob(ctx, first, "w1"); !res.Success {
		t.Fatalf("triage job failed: %+v", res)
	}
	task := taskForCell(t, dbc, "src", "c1")

	// A normal poll is now a no-op: the once guard drops the only match.
	d.poll(ctx, d.cfg.Sources[0], adapter, time.Now())
	if got := len(jobsFor(t, d, "triage")); got != 1 {
		t.Fatalf("setup: poll enqueued %d triage job(s), want 1 — the once guard should have dropped it", got)
	}

	res, err := d.ForceRestart(ctx, "c1")
	if err != nil {
		t.Fatalf("ForceRestart: %v", err)
	}

	if res.Dispatched != 1 {
		t.Fatalf("restart dispatched %d workflow(s), want 1 — a once-only workflow must still be restartable", res.Dispatched)
	}
	if len(res.Overridden) != 1 {
		t.Fatalf("restart reported overrides %v, want exactly one (the once guard)", res.Overridden)
	}
	if got := len(jobsFor(t, d, "triage")); got != 2 {
		t.Errorf("after restart there are %d triage job(s), want 2", got)
	}
	if _, err := dbc.InternalTasks().Generation(ctx, task.ID); err != nil {
		t.Fatalf("generation: %v", err)
	}
}

// TestForceRestart_ReportsNothingDispatched keeps the no-op case honest: when the
// cell matches no workflow, restart must say so rather than report a bare success
// the caller cannot distinguish from a real dispatch.
func TestForceRestart_ReportsNothingDispatched(t *testing.T) {
	ctx := context.Background()
	d, base, _ := newQueueDispatcher(t, false)
	adapter := &pollableAdapter{mutableAdapter: *base}
	d.sources["src"] = adapter

	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})

	// Drop the label every trigger matches on, so nothing routes any more.
	adapter.items[0].Labels = nil

	res, err := d.ForceRestart(ctx, "c1")
	if err != nil {
		t.Fatalf("ForceRestart: %v", err)
	}
	if res.Dispatched != 0 || len(res.Workflows) != 0 {
		t.Fatalf("restart reported %d dispatched (%v), want 0 — nothing matches the cell", res.Dispatched, res.Workflows)
	}
}

// TestForceRestart_ReleasesInFlightWhenNothingDispatches guards the failure mode
// that would be worse than the original bug: a restart that takes the in-flight
// marker and never gives it back would make the cell permanently invisible to
// every later poll (#375).
func TestForceRestart_ReleasesInFlightWhenNothingDispatches(t *testing.T) {
	ctx := context.Background()
	d, base, _ := newQueueDispatcher(t, false)
	adapter := &pollableAdapter{mutableAdapter: *base}
	d.sources["src"] = adapter

	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})
	adapter.items[0].Labels = nil

	if _, err := d.ForceRestart(ctx, "c1"); err != nil {
		t.Fatalf("ForceRestart: %v", err)
	}
	if _, held := d.inFlight.Load("c1"); held {
		t.Fatal("in-flight marker still held after a restart that dispatched nothing — the cell would never be polled again")
	}
}
