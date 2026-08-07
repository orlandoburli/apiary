package daemon

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/queue"
)

// flakyAdapter records the watermark of every Poll call and fails the calls whose
// index is listed in failOn, so a test can observe what the poll loop does with
// the incremental watermark across a failure.
type flakyAdapter struct {
	mu     sync.Mutex
	since  []time.Time
	failOn map[int]bool
	items  []model.SourceItem
}

func (a *flakyAdapter) ID() string                                    { return "src" }
func (a *flakyAdapter) Connect(context.Context, map[string]any) error { return nil }
func (a *flakyAdapter) Poll(_ context.Context, since time.Time) ([]model.SourceItem, error) {
	a.mu.Lock()
	index := len(a.since)
	a.since = append(a.since, since)
	a.mu.Unlock()
	if a.failOn[index] {
		return nil, errors.New("source unavailable")
	}
	return a.items, nil
}
func (a *flakyAdapter) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (a *flakyAdapter) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}
func (a *flakyAdapter) watermarks() []time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]time.Time, len(a.since))
	copy(out, a.since)
	return out
}

func eventsWithReason(t *testing.T, dbc *db.Client, reason string) []db.ExecutionEvent {
	t.Helper()
	all, err := dbc.ListExecutionEvents(context.Background(), db.ExecutionEventFilter{Type: "dispatch.dropped", Limit: 500})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var out []db.ExecutionEvent
	for _, event := range all {
		if got, _ := event.Metadata["reason"].(string); got == reason {
			out = append(out, event)
		}
	}
	return out
}

// Issue #375: a poll that never reached the source must not advance the
// incremental watermark. Advancing it makes every item updated during the
// outage window permanently invisible — the exact "watermark advanced past the
// update" shape in the report, where the hand-off label was never seen again
// and only a restart's full rescan recovered it.
func TestPollLoop_FailedPollDoesNotAdvanceWatermark(t *testing.T) {
	adapter := &flakyAdapter{failOn: map[int]bool{1: true}}
	d, _ := newWorkerDispatcher(t, filepath.Join(t.TempDir(), "queue.db"), &lockedAdapter{items: []model.SourceItem{{ID: "c1", SourceID: "src"}}})
	d.sources["src"] = adapter
	sc := d.cfg.Sources[0]
	sc.PollInterval = "40ms"

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.pollLoop(ctx, sc, adapter) }()

	deadline := time.Now().Add(5 * time.Second)
	for len(adapter.watermarks()) < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	marks := adapter.watermarks()
	if len(marks) < 4 {
		t.Fatalf("only %d poll(s) ran, need at least 4", len(marks))
	}
	// marks[1] is the failed poll. The poll after it must ask for changes since
	// no later than the failed poll's own watermark, or the window it never saw
	// is skipped for good.
	if marks[2].After(marks[1]) {
		t.Fatalf("watermark advanced across a failed poll: failed poll asked since %s, next poll asked since %s", marks[1], marks[2])
	}
}

// Issue #375's original (disproved) diagnosis was a leaked in-flight marker. If
// it ever does leak, the only evidence was a DEBUG line with no age, so the
// failure was undiagnosable from a normal log. A marker held implausibly long
// must now be reported.
func TestPoll_ReportsStaleInFlightMarker(t *testing.T) {
	ctx := context.Background()
	adapter := &lockedAdapter{items: []model.SourceItem{{ID: "c1", SourceID: "src", Title: "Do it", Labels: []string{"ai-ready"}}}}
	d, dbc := newWorkerDispatcher(t, filepath.Join(t.TempDir(), "queue.db"), adapter)

	d.inFlight.Store("c1", time.Now().Add(-2*inFlightStaleAfter))
	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})

	events := eventsWithReason(t, dbc, "stale in-flight marker")
	if len(events) != 1 {
		t.Fatalf("stale in-flight marker produced %d event(s), want 1 — the leak is invisible", len(events))
	}

	// Reported once, not once per poll.
	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})
	if events := eventsWithReason(t, dbc, "stale in-flight marker"); len(events) != 1 {
		t.Fatalf("re-poll re-reported the same stale marker: %d event(s)", len(events))
	}
}

// Issue #375, "enqueued but never leased": a job every worker *could* run on
// pool/labels/capabilities, that nothing has leased, is invisible — the
// capability watchdog passes it and `apiary status` shows a healthy worker. The
// watchdog must explain the stall with the worker counters behind it.
func TestQueueWatchdog_ReportsSatisfiableButUnleasedJob(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "queue.db")
	adapter := &lockedAdapter{items: []model.SourceItem{{ID: "c1", SourceID: "src", Title: "Do it", Labels: []string{"ai-ready"}}}}
	d, dbc := newWorkerDispatcher(t, dbPath, adapter)

	// Register the embedded worker without running it, then saturate it the way
	// an orphaned lease does against the default capacity of 1.
	if err := dbc.Queue().RegisterWorker(ctx, &d.queueWorker); err != nil {
		t.Fatalf("register worker: %v", err)
	}
	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})
	job := queueJobFor(t, dbc, "triage")
	if job.State != queue.JobQueued {
		t.Fatalf("job state = %q, want queued", job.State)
	}
	setStaleActiveJobs(t, dbPath, d.queueWorkerID, 1)
	backdateJob(t, dbPath, job.ID, time.Now().Add(-2*queueStallThreshold))

	d.warnUnsatisfiableQueueJobs(ctx)
	events := eventsWithReason(t, dbc, "queued but never leased")
	if len(events) != 1 {
		t.Fatalf("a satisfiable job queued past the stall threshold produced %d event(s), want 1", len(events))
	}
	if detail, _ := events[0].Metadata["detail"].(string); detail == "" {
		t.Fatal("stall event carries no explanation of which worker is blocking it")
	}

	// Warned once per job, not once per watchdog tick.
	d.warnUnsatisfiableQueueJobs(ctx)
	if events := eventsWithReason(t, dbc, "queued but never leased"); len(events) != 1 {
		t.Fatalf("watchdog re-reported the same stalled job: %d event(s)", len(events))
	}
}

// backdateJob rewrites a queue job's created_at so a test can reach the
// watchdog's stall threshold without waiting.
func backdateJob(t *testing.T, dbPath, jobID string, created time.Time) {
	t.Helper()
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`UPDATE dispatch_jobs SET created_at=? WHERE id=?`, created.UTC(), jobID); err != nil {
		t.Fatalf("backdate job: %v", err)
	}
}
