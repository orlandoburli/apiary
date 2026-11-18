package db

import (
	"context"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/state"
)

// legacyInstanceStateInterrupted is the pre-canonical instance state for an
// interrupted run. Every state is 'blocked' now and the three parked states
// differ only by their reason, but a database written before that migration
// still carries the old name, and it must be cleaned up too — otherwise the
// rows it leaves behind are exactly the ones nobody can explain.
const legacyInstanceStateInterrupted = "interrupted"

// ReconcileAbandonedInstances closes out the workflow instances that no run is
// working on and no job is waiting to start.
//
// The periodic reaper (ReapStaleExecutions) closes an execution whose process
// is gone, and with it the step and the instance that execution belonged to. It
// works *from* a running execution, so it cannot reach an instance whose
// execution was already closed — or that never had one.
//
// Those accumulate: an instance whose step was interrupted stays non-terminal
// forever, and the dashboard's waiting section lists it as "run died" for as
// long as the database lives. On a hive with a routine that runs every thirty
// minutes, that is one such row per dead routine run, and the list of
// "waiting" workflows becomes a list of corpses.
//
// An instance is abandoned when nothing could still make progress on it:
//
//   - it has no step run that is still open (no agent is working on it), and
//   - it has no execution that is still running (no process is working on it),
//     and
//   - no dispatch job is waiting to start it.
//
// The third condition is what keeps this from closing an instance that is
// merely waiting for a worker: a queued job is a promise that the run is still
// coming.
//
// The instance is closed as failed — a terminal state. Not as blocked/
// interrupted, which is where the debris already is: that would be a no-op, and
// the pass would keep matching the same rows forever. 'failed' says what is
// true — the round is over and will not be resumed — and being terminal it
// drops the instance out of the dashboard's waiting list and makes the pass
// idempotent.
//
// The step runs under it are closed with it, so the dashboard does not render a
// phantom in-progress step under an instance nobody is working on.
//
// Only the interrupted states are touched. An instance parked on an approval, a
// CI run or a blocker is waiting for something that may still arrive, and
// closing it would throw away work that is genuinely pending.
func (c *Client) ReconcileAbandonedInstances(ctx context.Context) (int64, error) {
	now := time.Now()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// The instances to close, collected before anything is written. Collecting
	// them first is what lets the step runs be closed against exactly these
	// instances — after the update they are indistinguishable from any other
	// failed instance.
	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM workflow_instances
		WHERE (
		      (state = ? AND COALESCE(blocked_reason, '') = ?)
		   OR (state = ? AND COALESCE(blocked_reason, '') IN ('', ?))
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM step_runs sr
		    WHERE sr.workflow_instance_id = workflow_instances.id
		      AND sr.state = 'running'
		      AND sr.finished_at IS NULL
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM task_executions e
		    WHERE e.status = 'running'
		      AND (e.workflow_instance_id = workflow_instances.id
		           OR e.task_id = workflow_instances.task_id)
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM dispatch_jobs dj
		    WHERE dj.state IN ('queued','leased')
		      AND json_extract(dj.payload, '$.match.instance_id') = workflow_instances.id
		  )
	`, InstanceStateBlocked, string(state.ReasonInterrupted),
		legacyInstanceStateInterrupted, string(state.ReasonInterrupted))
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	// Close the step runs left open under them. A step that never finished is
	// what makes the dashboard render a phantom in-progress step under an
	// instance nobody is working on.
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+3)
	args = append(args, StepStateFailed, string(state.ReasonInterrupted), now)
	for _, id := range ids {
		args = append(args, id)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE step_runs
		SET state = ?, blocked_reason = ?, finished_at = COALESCE(finished_at, ?)
		WHERE state IN ('running', 'queued', 'blocked')
		  AND finished_at IS NULL
		  AND workflow_instance_id IN (`+placeholders+`)
	`, args...); err != nil {
		return 0, err
	}

	// And the instances themselves.
	args = args[:0]
	args = append(args, InstanceStateFailed, now)
	for _, id := range ids {
		args = append(args, id)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE workflow_instances
		SET state = ?, blocked_reason = '', updated_at = ?
		WHERE id IN (`+placeholders+`)
	`, args...); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(ids)), nil
}
