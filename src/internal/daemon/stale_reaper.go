package daemon

import (
	"context"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"
)

// staleReapInterval is how often the daemon looks for executions whose process
// is gone.
//
// Five minutes is a compromise: a row that has been lying about being live
// should not sit there for long, and the check is a scan of the executions
// table — cheap, but not free, and it grows with the hive's history.
const staleReapInterval = 5 * time.Minute

// staleReapStartupDelay is how long the first reaping pass waits.
//
// The startup reconciliations above have already closed everything a previous
// process left behind, so an immediate pass would find nothing and only risk
// racing them. Delaying it lets the daemon settle, and lets a run that has
// just started record its pid and first heartbeat before anything looks at it.
const staleReapStartupDelay = 60 * time.Second

// reapStaleExecutionsLoop closes out executions whose agent process is gone,
// every staleReapInterval, until the daemon stops.
//
// A run that dies leaves its execution row saying 'running'. The startup
// reconciliation cannot help with that — it only runs once, and it clears
// everything precisely because a fresh process owns none of it. Without a
// periodic pass those rows accumulate for the daemon's whole life: the
// dashboard counts them as work in flight, the agent list shows those agents
// as busy, and the stuck-scan reports their instances as stuck.
func (d *Dispatcher) reapStaleExecutionsLoop(ctx context.Context) {
	timer := time.NewTimer(staleReapStartupDelay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		d.reapStaleExecutionsOnce(ctx)

		timer.Reset(staleReapInterval)
	}
}

// reapStaleExecutionsOnce runs a single reaping pass and reports what it
// closed.
//
// Split out from the loop so it can be exercised directly, without waiting on
// a timer.
func (d *Dispatcher) reapStaleExecutionsOnce(ctx context.Context) {
	if d.db == nil {
		return
	}
	n, err := d.db.ReapStaleExecutions(ctx, time.Now())
	if err != nil {
		aplog.Warn("reap stale executions: %v", err)
		return
	}
	if n > 0 {
		// Logged, not silent: a row closed here is a run that died without
		// the runner ever reporting an exit, and that is worth seeing.
		aplog.Info("reaped %d execution(s) whose agent process is gone", n)
	}
}
