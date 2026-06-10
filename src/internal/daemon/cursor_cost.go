package daemon

import (
	"context"
	"os"
	"time"

	"github.com/orlandoburli/apiary/internal/cursorusage"
	aplog "github.com/orlandoburli/apiary/internal/log"
)

// cursorCostRunner is the runner type whose executions need a cost back-fill:
// the Cursor agent CLI streams token counts but no dollar cost.
const cursorCostRunner = "cursor-cli"

// cursorCostSkew widens each run's [started_at, completed_at] window when
// matching dashboard events, absorbing clock drift between the local machine
// and Cursor's billing timestamps.
const cursorCostSkew = 2 * time.Minute

// cursorCostMaxAttempts caps how many sweeps may retry one execution before it
// is abandoned (events never appeared, or stay ambiguous). In-memory: a daemon
// restart re-arms abandoned rows, which is harmless — the sweep is idempotent
// and bounded by settings.cursor_cost.max_age.
const cursorCostMaxAttempts = 10

// cursorEventFetcher is the slice of cursorusage.Client the sweep needs;
// narrowed for tests.
type cursorEventFetcher interface {
	FetchEvents(ctx context.Context, start, end time.Time) ([]cursorusage.UsageEvent, error)
}

// cursorCostLoop periodically back-fills cost_usd on finished cursor-cli
// executions from Cursor's dashboard usage API. Runs once at startup and then
// every settings.cursor_cost.interval until ctx is cancelled.
func (d *Dispatcher) cursorCostLoop(ctx context.Context) {
	cc := d.cfg.Settings.CursorCost
	token := cc.SessionToken
	if token == "" {
		token = os.Getenv("CURSOR_SESSION_TOKEN")
	}
	if token == "" {
		aplog.Warn("cursor_cost enabled but no session token (settings.cursor_cost.session_token or CURSOR_SESSION_TOKEN); back-fill disabled")
		return
	}
	client := &cursorusage.Client{Token: token}
	attempts := make(map[int64]int)

	sweep := func() {
		if err := d.backfillCursorCosts(ctx, client, attempts); err != nil {
			aplog.Warn("cursor cost back-fill: %v", err)
		}
	}
	sweep()

	ticker := time.NewTicker(cc.IntervalDuration())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

// backfillCursorCosts runs one sweep: list unpriced cursor-cli executions,
// fetch the dashboard events spanning their windows in one call, attribute
// events to runs by time window, and write back the resolved costs. attempts
// tracks per-execution retries across sweeps so rows whose events never
// resolve are eventually abandoned.
func (d *Dispatcher) backfillCursorCosts(ctx context.Context, fetcher cursorEventFetcher, attempts map[int64]int) error {
	since := time.Now().Add(-d.cfg.Settings.CursorCost.MaxAgeDuration())
	rows, err := d.db.ListUnpricedExecutions(ctx, cursorCostRunner, since)
	if err != nil {
		return err
	}
	// Drop rows that exhausted their retries; forget attempt counts for rows
	// that no longer come back (resolved or aged out).
	live := make(map[int64]bool, len(rows))
	pending := rows[:0]
	for _, r := range rows {
		live[r.ID] = true
		if attempts[r.ID] >= cursorCostMaxAttempts {
			continue
		}
		pending = append(pending, r)
	}
	for id := range attempts {
		if !live[id] {
			delete(attempts, id)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	windows := make([]cursorusage.RunWindow, 0, len(pending))
	first, last := pending[0].StartedAt, pending[0].CompletedAt
	for _, r := range pending {
		attempts[r.ID]++
		windows = append(windows, cursorusage.RunWindow{ID: r.ID, Start: r.StartedAt, End: r.CompletedAt})
		if r.StartedAt.Before(first) {
			first = r.StartedAt
		}
		if r.CompletedAt.After(last) {
			last = r.CompletedAt
		}
	}

	events, err := fetcher.FetchEvents(ctx, first.Add(-cursorCostSkew), last.Add(cursorCostSkew))
	if err != nil {
		return err
	}

	attributed := cursorusage.Attribute(events, windows, cursorCostSkew)
	for _, r := range pending {
		a := attributed[r.ID]
		if a == nil || a.Events == 0 {
			if a != nil && a.Ambiguous > 0 {
				aplog.Warn("cursor cost: execution %d overlaps another cursor run; %d event(s) ambiguous, cost not attributed", r.ID, a.Ambiguous)
			}
			continue
		}
		if a.Ambiguous > 0 {
			aplog.Warn("cursor cost: execution %d has %d ambiguous event(s) from overlapping runs; recorded $%.4f is a lower bound", r.ID, a.Ambiguous, a.CostUSD)
		}
		matched := a.InputTokens + a.OutputTokens
		if matched > 0 && r.TotalTokens > 0 && (matched > r.TotalTokens*3 || matched*3 < r.TotalTokens) {
			aplog.Warn("cursor cost: execution %d attributed events total %d tokens vs run's %d; window match may be off", r.ID, matched, r.TotalTokens)
		}
		if a.CostUSD <= 0 {
			// All matched events were not charged (errored/included). Leave the row
			// at 0; retries are capped by attempts.
			continue
		}
		if err := d.db.SetExecutionCost(ctx, r.ID, a.CostUSD); err != nil {
			aplog.Warn("cursor cost: update execution %d: %v", r.ID, err)
			continue
		}
		if err := d.db.RefreshStepRunCost(ctx, r.WorkflowInstanceID, r.StepID); err != nil {
			aplog.Warn("cursor cost: refresh step run for execution %d: %v", r.ID, err)
		}
		delete(attempts, r.ID)
		aplog.Info("cursor cost: execution %d back-filled $%.4f from %d usage event(s)", r.ID, a.CostUSD, a.Events)
	}
	return nil
}
