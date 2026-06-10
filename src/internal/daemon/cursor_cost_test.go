package daemon

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/cursorusage"
	"github.com/orlandoburli/apiary/internal/db"
)

type fakeFetcher struct {
	events []cursorusage.UsageEvent
	calls  int
}

func (f *fakeFetcher) FetchEvents(ctx context.Context, start, end time.Time) ([]cursorusage.UsageEvent, error) {
	f.calls++
	return f.events, nil
}

// insertCursorExec creates a finished cursor-cli execution with an 800/200
// token tuple (1000 total). Its window starts at CreateExecution time (now)
// and ends at end; tests place events relative to start.
func insertCursorExec(t *testing.T, dbc *db.Client, ctx context.Context, instance, step string, end time.Time) (int64, time.Time) {
	return insertCursorExecTokens(t, dbc, ctx, instance, step, end, 800, 200)
}

func insertCursorExecTokens(t *testing.T, dbc *db.Client, ctx context.Context, instance, step string, end time.Time, in, out int) (int64, time.Time) {
	t.Helper()
	exec, err := dbc.CreateExecution(ctx, "42", "engineer", "title", "42", "", "composer-2", "cursor-cli", 1)
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}
	exec.Status = "success"
	exec.CompletedAt = &end
	exec.InputTokens = in
	exec.OutputTokens = out
	exec.TotalTokens = in + out
	if err := dbc.UpdateExecution(ctx, exec); err != nil {
		t.Fatalf("finish execution: %v", err)
	}
	if instance != "" {
		if err := dbc.SetStepLink(ctx, exec.ID, instance, step); err != nil {
			t.Fatalf("set step link: %v", err)
		}
	}
	return exec.ID, *exec.StartedAt
}

func execCost(t *testing.T, dbc *db.Client, ctx context.Context, id int64) float64 {
	t.Helper()
	execs, err := dbc.ListUnpricedExecutions(ctx, "cursor-cli", time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, e := range execs {
		if e.ID == id {
			return 0 // still unpriced
		}
	}
	// Priced (or filtered out): read the rollup through GetStepUsage-free path —
	// use ListUnpricedExecutions absence as "non-zero" signal plus exact value
	// via the dashboard usage helper below.
	usage, err := dbc.GetInstanceStepUsage(ctx, "wf_1")
	if err != nil {
		t.Fatalf("instance usage: %v", err)
	}
	if u, ok := usage["implement"]; ok {
		return u.CostUSD
	}
	return -1
}

func TestBackfillCursorCosts(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbc.Close()

	execID, start := insertCursorExec(t, dbc, ctx, "wf_1", "implement", time.Now().Add(10*time.Minute))
	sr := &db.StepRun{ID: "sr_1", WorkflowInstanceID: "wf_1", StepID: "implement", State: "passed"}
	if err := dbc.CreateStepRun(ctx, sr); err != nil {
		t.Fatalf("create step_run: %v", err)
	}

	fetcher := &fakeFetcher{events: []cursorusage.UsageEvent{
		{
			Timestamp:    strconv.FormatInt(start.Add(2*time.Minute).UnixMilli(), 10),
			Kind:         "USAGE_EVENT_KIND_USAGE_BASED",
			ChargedCents: 124.73,
			TokenUsage:   &cursorusage.TokenUsage{InputTokens: 800, OutputTokens: 200},
		},
		{
			Timestamp:    strconv.FormatInt(start.Add(40*time.Minute).UnixMilli(), 10),
			Kind:         "USAGE_EVENT_KIND_USAGE_BASED",
			ChargedCents: 99999, // outside the run window: unrelated activity
		},
	}}

	d := &Dispatcher{db: dbc, cfg: &config.Config{}}
	attempts := make(map[int64]int)
	if err := d.backfillCursorCosts(ctx, fetcher, attempts); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	if got := execCost(t, dbc, ctx, execID); got != 1.2473 {
		t.Errorf("execution cost = %v, want 1.2473", got)
	}
	srs, err := dbc.ListStepRuns(ctx, "wf_1")
	if err != nil {
		t.Fatalf("list step runs: %v", err)
	}
	if len(srs) != 1 || srs[0].CostUSD != 1.2473 {
		t.Errorf("step_run cost = %+v, want 1.2473 (refreshed from executions)", srs)
	}

	// Resolved rows must not be re-fetched on the next sweep.
	if err := d.backfillCursorCosts(ctx, fetcher, attempts); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if fetcher.calls != 1 {
		t.Errorf("fetcher calls = %d, want 1 (nothing pending after back-fill)", fetcher.calls)
	}
	if len(attempts) != 0 {
		t.Errorf("attempts map = %v, want empty after resolution", attempts)
	}
}

func TestBackfillCursorCostsGivesUpAfterMaxAttempts(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbc.Close()

	insertCursorExec(t, dbc, ctx, "", "", time.Now().Add(10*time.Minute))

	// No events ever match (e.g. billing data never appeared).
	fetcher := &fakeFetcher{}
	d := &Dispatcher{db: dbc, cfg: &config.Config{}}
	attempts := make(map[int64]int)
	for i := 0; i < cursorCostMaxAttempts; i++ {
		if err := d.backfillCursorCosts(ctx, fetcher, attempts); err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
	}
	if fetcher.calls != cursorCostMaxAttempts {
		t.Fatalf("fetcher calls = %d, want %d", fetcher.calls, cursorCostMaxAttempts)
	}
	// The row exhausted its retries: further sweeps must skip the fetch.
	if err := d.backfillCursorCosts(ctx, fetcher, attempts); err != nil {
		t.Fatalf("post-exhaustion sweep: %v", err)
	}
	if fetcher.calls != cursorCostMaxAttempts {
		t.Errorf("fetcher calls = %d after exhaustion, want unchanged %d", fetcher.calls, cursorCostMaxAttempts)
	}
}

// Interactive IDE usage on the same Cursor account is indistinguishable from
// CLI events (isHeadless is false for both), so when the attributed events
// carry far more tokens than the run itself reported, the sweep must refuse
// to record the inflated amount.
func TestBackfillCursorCostsSkipsSuspectedPollution(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbc.Close()

	// Run reports 1000 tokens; attributed events total 5000 — pollution.
	id, start := insertCursorExec(t, dbc, ctx, "", "", time.Now().Add(10*time.Minute))
	fetcher := &fakeFetcher{events: []cursorusage.UsageEvent{{
		Timestamp:    strconv.FormatInt(start.Add(2*time.Minute).UnixMilli(), 10),
		Kind:         "USAGE_EVENT_KIND_USAGE_BASED",
		ChargedCents: 500,
		TokenUsage:   &cursorusage.TokenUsage{InputTokens: 4000, OutputTokens: 1000},
	}}}
	d := &Dispatcher{db: dbc, cfg: &config.Config{}}
	if err := d.backfillCursorCosts(ctx, fetcher, make(map[int64]int)); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	rows, err := dbc.ListUnpricedExecutions(ctx, "cursor-cli", time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.ID == id {
			found = true
		}
	}
	if !found {
		t.Errorf("execution %d was priced despite token mismatch; want skipped (suspected IDE pollution)", id)
	}
}

func TestBackfillCursorCostsOverlapStaysUnpriced(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbc.Close()

	// Two runs with overlapping windows.
	idA, startA := insertCursorExec(t, dbc, ctx, "", "", time.Now().Add(10*time.Minute))
	idB, _ := insertCursorExec(t, dbc, ctx, "", "", time.Now().Add(15*time.Minute))

	// The only event falls in both windows: must not be attributed to either.
	fetcher := &fakeFetcher{events: []cursorusage.UsageEvent{{
		Timestamp:    strconv.FormatInt(startA.Add(7*time.Minute).UnixMilli(), 10),
		Kind:         "USAGE_EVENT_KIND_USAGE_BASED",
		ChargedCents: 100,
	}}}
	d := &Dispatcher{db: dbc, cfg: &config.Config{}}
	if err := d.backfillCursorCosts(ctx, fetcher, make(map[int64]int)); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	rows, err := dbc.ListUnpricedExecutions(ctx, "cursor-cli", time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := map[int64]bool{}
	for _, r := range rows {
		got[r.ID] = true
	}
	if !got[idA] || !got[idB] {
		t.Errorf("unpriced rows = %v, want both %d and %d still unpriced (ambiguous event)", got, idA, idB)
	}
}

// The real-world shape that motivated fingerprint matching: concurrent cursor
// runs with fully overlapping windows. Their events are inside both windows,
// but distinct token tuples identify each run's event exactly — both get
// priced where the window-only matcher attributed neither.
func TestBackfillCursorCostsFingerprintResolvesOverlap(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbc.Close()

	idA, startA := insertCursorExecTokens(t, dbc, ctx, "", "", time.Now().Add(10*time.Minute), 30641, 12)
	idB, _ := insertCursorExecTokens(t, dbc, ctx, "", "", time.Now().Add(12*time.Minute), 105608, 153)

	fetcher := &fakeFetcher{events: []cursorusage.UsageEvent{
		{
			Timestamp:    strconv.FormatInt(startA.Add(5*time.Minute).UnixMilli(), 10),
			Kind:         "USAGE_EVENT_KIND_USAGE_BASED",
			ChargedCents: 100,
			TokenUsage:   &cursorusage.TokenUsage{InputTokens: 30641, OutputTokens: 12},
		},
		{
			Timestamp:    strconv.FormatInt(startA.Add(6*time.Minute).UnixMilli(), 10),
			Kind:         "USAGE_EVENT_KIND_USAGE_BASED",
			ChargedCents: 572,
			TokenUsage:   &cursorusage.TokenUsage{InputTokens: 105608, OutputTokens: 153},
		},
	}}
	d := &Dispatcher{db: dbc, cfg: &config.Config{}}
	if err := d.backfillCursorCosts(ctx, fetcher, make(map[int64]int)); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	rows, err := dbc.ListUnpricedExecutions(ctx, "cursor-cli", time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range rows {
		if r.ID == idA || r.ID == idB {
			t.Errorf("execution %d still unpriced; want fingerprint attribution despite overlap", r.ID)
		}
	}
}
