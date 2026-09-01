package db

import (
	"context"
	"strings"

	"github.com/orlandoburli/apiary/internal/state"
)

// TaskStepRow is one step of one instance belonging to one task, as returned by
// ListStepProgressForTasks. An instance that has not started a step yet still
// yields a row, with StepID empty.
type TaskStepRow struct {
	TaskID        string
	InstanceID    string
	WorkflowID    string
	InstanceState string
	StepID        string
	StepState     string
	BlockedReason string
}

// ListStepProgressForTasks returns the step rows of every non-terminal instance
// belonging to the given tasks, ordered oldest instance first and, within an
// instance, in execution order.
//
// It exists so the Tasks list can show how far a task has got without issuing a
// query per visible row. The list re-renders on a timer against the same SQLite
// file the daemon is writing to, so a per-row query would mean a page of
// queries every couple of seconds; this is one.
//
// Interrupted instances are excluded: they are blocked, but dead, and reporting
// a dead instance's step as a task's current position would be misleading. An
// instance with no step runs yet is included with an empty StepID, so a task
// that has just been dispatched is distinguishable from one with no instance.
func (c *Client) ListStepProgressForTasks(ctx context.Context, taskIDs []string) ([]TaskStepRow, error) {
	if len(taskIDs) == 0 {
		return nil, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(taskIDs)), ",")
	args := make([]any, 0, len(taskIDs)+1)
	for _, id := range taskIDs {
		args = append(args, id)
	}
	args = append(args, string(state.ReasonInterrupted))

	rows, err := c.db.QueryContext(ctx, `
		SELECT COALESCE(wi.task_id,''), wi.id, wi.workflow_id, wi.state,
		       COALESCE(sr.step_id,''), COALESCE(sr.state,''), COALESCE(sr.blocked_reason,'')
		FROM workflow_instances wi
		LEFT JOIN step_runs sr ON sr.workflow_instance_id = wi.id
		WHERE wi.task_id IN (`+placeholders+`)
		  AND wi.state NOT IN ('done','failed','canceled')
		  AND wi.state <> 'interrupted'
		  AND NOT (wi.state = 'blocked' AND COALESCE(wi.blocked_reason,'') = ?)
		ORDER BY wi.created_at ASC, sr.rowid ASC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TaskStepRow
	for rows.Next() {
		var r TaskStepRow
		if err := rows.Scan(&r.TaskID, &r.InstanceID, &r.WorkflowID, &r.InstanceState,
			&r.StepID, &r.StepState, &r.BlockedReason); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
