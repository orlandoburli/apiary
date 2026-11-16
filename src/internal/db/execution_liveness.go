package db

import "time"

// ExecutionHeartbeatWindow is how old a heartbeat may be and still count as
// proof that the agent process is working.
//
// The runner refreshes a running execution's heartbeat every 15 seconds
// (internal/runner/execution/cli.go), so 60 seconds is four missed beats —
// long enough that a busy process is never mistaken for a dead one, short
// enough that a dead one is noticed promptly.
//
// This is the single definition of "alive" in the system. The dashboard's
// Running tab reports a row as alive by this rule, and the daemon's reaper
// decides whether to close a row by the same rule, so the two can never
// disagree about the same execution.
const ExecutionHeartbeatWindow = 60 * time.Second

// ExecutionHeartbeatGrace is how long an execution row may carry no evidence
// of a live process — no heartbeat, and no pid either — before it counts as
// stale.
//
// The daemon records the pid and the first heartbeat together when the runner
// starts, so a row with neither is a run that never got that far, or a row
// whose values were lost. Either way it is not evidence of a live process, but
// a row the daemon is merely about to write must not be closed out. The grace
// period is measured from the row's own start, so a run that began a second
// ago is never mistaken for a leftover.
const ExecutionHeartbeatGrace = 10 * time.Minute

// ExecutionIsAlive reports whether an execution row that still says 'running'
// is backed by a process that is actually working.
//
// The execution row is written immediately before the runner is invoked, so a
// row left in that state can mean two very different things: a process that is
// working right now, or one that died without updating its row.
//
// Two signals say a process is running, and they are checked independently:
//
//   - A fresh heartbeat. The runner refreshes it every 15 seconds while it
//     works, so a heartbeat from the last minute is proof of life on its own.
//     This is the signal that matters; the daemon writes it on every tick.
//   - A pid, with no heartbeat to judge by. The daemon records the pid when
//     the runner starts, so a pid with no heartbeat is a process whose
//     heartbeat was never written or was lost. It is only treated as alive if
//     the row is also young enough that the heartbeat cannot have been lost
//     yet.
//
// A row with neither a fresh heartbeat nor a usable pid is not alive: nothing
// about it says a process is running.
func ExecutionIsAlive(pid int, heartbeat *time.Time, started *time.Time, now time.Time) bool {
	if heartbeat != nil {
		return now.Sub(*heartbeat) <= ExecutionHeartbeatWindow
	}
	// No heartbeat to judge by: the pid is all there is, and it is only
	// trusted while the row is young enough that the heartbeat it should have
	// carried cannot have been lost yet.
	if pid <= 0 || started == nil {
		return false
	}
	return now.Sub(*started) < ExecutionHeartbeatGrace
}

// ExecutionIsStale reports whether an execution row that says 'running' is
// provably not backed by a live process, and so can be closed out.
//
// It is the complement of ExecutionIsAlive: a row that is not alive is stale
// once it is old enough that the absence of evidence cannot be a write that
// has not landed yet.
//
// The two functions are defined together so that a row cannot be simultaneously
// alive and stale — the dashboard and the reaper must never disagree about the
// same execution.
func ExecutionIsStale(pid int, heartbeat *time.Time, started *time.Time, now time.Time) bool {
	if heartbeat != nil {
		// A heartbeat is either fresh (alive) or stale. There is no third
		// state, and the row's age does not change that: a heartbeat that
		// stopped two hours ago is proof the process is gone, whatever the
		// row's own timestamps say.
		return now.Sub(*heartbeat) > ExecutionHeartbeatWindow
	}
	// No heartbeat at all: nothing says a process is running, but a row the
	// daemon is merely about to write must not be closed out. The grace
	// period is measured from the row's own start.
	if started == nil {
		// Nothing to measure the absence against. Leave it alone rather than
		// guess: closing a row whose age cannot be established could close
		// work that is only moments old.
		return false
	}
	return now.Sub(*started) >= ExecutionHeartbeatGrace
}
