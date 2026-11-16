package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/orlandoburli/apiary/internal/state"
)

// StaleExecution is a running execution row whose process could not be found
// alive — the reaper's candidate for being closed out.
type StaleExecution struct {
	ID          int64
	TaskID      string
	AgentID     string
	PID         int
	HeartbeatAt *time.Time
	StartedAt   *time.Time
}

// ReapStaleExecutions closes out the running executions that are provably not
// backed by a live process.
//
// The startup reconciliation (ReconcileOrphanExecutions) clears every row that
// says 'running', because a fresh dispatcher process genuinely owns none of
// them. This one runs *while* the daemon is working, so it must be far more
// careful: it may only close a row whose process is provably gone, judged by
// exactly the rule the dashboard's Running tab uses (ExecutionIsAlive). A row
// that the dashboard shows as alive must survive here, or the reaper would
// close work that is still running.
//
// A row is closed as 'failed' with an error message saying the process is gone,
// and its step_run and workflow_instance are closed with it — otherwise the
// workflow monitor keeps rendering a phantom in-progress step under an
// instance nobody is working on, which is what the janitor's stuck-scan then
// reports as stuck.
//
// Returns the number of execution rows closed.
func (c *Client) ReapStaleExecutions(ctx context.Context, now time.Time) (int64, error) {
	candidates, err := c.staleExecutionCandidates(ctx, now)
	if err != nil {
		return 0, err
	}
	if len(candidates) == 0 {
		return 0, nil
	}

	closed := int64(0)
	for _, cand := range candidates {
		n, err := c.closeStaleExecution(ctx, cand, now)
		if err != nil {
			// One row that cannot be closed must not stop the rest: the rest
			// are just as dead, and leaving them behind because one row hit
			// an error is how the pile-up starts.
			continue
		}
		closed += n
	}
	return closed, nil
}

// staleExecutionCandidates returns the running executions that the liveness
// rule judges stale.
//
// The query fetches every running row and the decision is made in Go, because
// the rule reads a heartbeat and a pid per row and SQLite cannot express
// "alive" over the mix of shapes those columns take (a pid that was never
// recorded, a heartbeat that was never written, a heartbeat old enough).
func (c *Client) staleExecutionCandidates(ctx context.Context, now time.Time) ([]StaleExecution, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, task_id, agent_id, pid, heartbeat_at, started_at
		FROM task_executions
		WHERE status = 'running'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StaleExecution
	for rows.Next() {
		var e StaleExecution
		var pid sql.NullInt64
		var hbStr, startedStr sql.NullString
		if err := rows.Scan(&e.ID, &e.TaskID, &e.AgentID, &pid, &hbStr, &startedStr); err != nil {
			continue
		}
		if pid.Valid {
			e.PID = int(pid.Int64)
		}
		if hbStr.Valid {
			if t, ok := parseSQLiteTime(hbStr.String); ok {
				e.HeartbeatAt = &t
			}
		}
		if startedStr.Valid {
			if t, ok := parseSQLiteTime(startedStr.String); ok {
				e.StartedAt = &t
			}
		}
		if ExecutionIsStale(e.PID, e.HeartbeatAt, e.StartedAt, now) {
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

// closeStaleExecution closes one dead execution and the workflow state that
// hangs off it.
//
// The order matters: the step_run and the workflow_instance are closed first,
// so that a reader never sees an instance still marked 'running' whose step is
// already closed. The execution row is closed last, because that is the row
// the reaper's own candidate list is built from.
func (c *Client) closeStaleExecution(ctx context.Context, cand StaleExecution, now time.Time) (int64, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// Close the step run, if this execution had one.
	//
	// Matched on the (instance, step) pair rather than on the step name alone:
	// step_runs.id is the row's own id while step_id is the step's name, and
	// the execution records both — the instance it ran under and the step it
	// ran. Scoped to that pair rather than to every running step under the
	// instance, because an instance can carry several steps and one of them
	// may be live — a QA step running under the same instance as the engineer
	// step that died. Closing that one would take a live step down with a
	// dead one.
	if _, err := tx.ExecContext(ctx, `
		UPDATE step_runs
		SET state = ?, blocked_reason = ?, finished_at = COALESCE(finished_at, ?)
		WHERE (workflow_instance_id, step_id) IN (
		    SELECT workflow_instance_id, step_id FROM task_executions
		    WHERE id = ? AND workflow_instance_id IS NOT NULL AND workflow_instance_id != ''
		      AND step_id IS NOT NULL AND step_id != ''
		)
		  AND state = 'running'
		  AND finished_at IS NULL
	`, StepStateBlocked, string(state.ReasonInterrupted), now, cand.ID); err != nil {
		return 0, err
	}

	// Close the workflow instance, if this execution had one. Only an instance
	// whose own step is no longer running qualifies: an instance with a live
	// step must not be closed out from under it.
	if _, err := tx.ExecContext(ctx, `
		UPDATE workflow_instances
		SET state = ?, blocked_reason = ?, updated_at = ?
		WHERE id IN (
		    SELECT workflow_instance_id FROM task_executions
		    WHERE id = ? AND workflow_instance_id IS NOT NULL AND workflow_instance_id != ''
		)
		  AND state = 'running'
		  AND NOT EXISTS (
		    SELECT 1 FROM step_runs sr
		    WHERE sr.workflow_instance_id = workflow_instances.id
		      AND sr.state = 'running' AND sr.finished_at IS NULL
		  )
	`, InstanceStateBlocked, string(state.ReasonInterrupted), now, cand.ID); err != nil {
		return 0, err
	}

	// Close the execution row itself. The WHERE clause repeats the liveness
	// condition so that a row which became alive between the candidate query
	// and this write is not closed.
	res, err := tx.ExecContext(ctx, `
		UPDATE task_executions
		SET status = 'failed',
		    completed_at = ?,
		    duration_ms = COALESCE(duration_ms, CAST((julianday(?) - julianday(COALESCE(started_at, created_at))) * 86400000 AS INTEGER)),
		    error_message = CASE
		        WHEN error_message IS NULL OR error_message = '' THEN ?
		        ELSE error_message
		    END
		WHERE id = ? AND status = 'running'
	`, now, now, staleExecutionMessage(now), cand.ID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// staleExecutionMessage is the error recorded on a row closed by the reaper.
//
// It names the last heartbeat rather than just saying "stale": a row whose
// process died without the runner ever reporting an exit leaves no other trace,
// so the heartbeat is the only evidence of when the process was last known to
// be alive — and it is what a later investigation needs.
func staleExecutionMessage(now time.Time) string {
	return fmt.Sprintf("process gone (no heartbeat for over %s; reaped at %s)",
		ExecutionHeartbeatWindow, now.Format(time.RFC3339))
}
