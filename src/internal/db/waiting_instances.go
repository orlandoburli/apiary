package db

import (
	"context"
	"time"
)

// WaitingInstance is a workflow instance that has not reached a terminal state
// — it is either running, or parked on something (an approval, a CI run, a
// blocker issue, a retry).
type WaitingInstance struct {
	ID         string
	WorkflowID string
	TaskID     string
	CellID     string
	Title      string
	State      string
	// BlockedReason explains a blocked instance: approval | ci | dependency |
	// retry_backoff | interrupted. Empty when the instance is not blocked.
	BlockedReason string
	AgentID       string // the agent of the step that is open, when there is one
	StepID        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ListNonTerminalInstances returns every workflow instance that has not
// reached a terminal state, newest first, with the task's title and the step
// that is currently open.
//
// Terminal means done, failed or canceled — the three states from which no
// further transition happens without operator action. Everything else is
// either running or waiting for something, and both are worth showing: a
// parked workflow is work the hive has in hand but cannot advance.
//
// The step join is what distinguishes the two. An instance with an open
// (running, unfinished) step has an agent working on it; an instance without
// one is parked — waiting on an approval, a CI run, a blocker, or the debris of
// a run that died.
func (c *Client) ListNonTerminalInstances(ctx context.Context, limit int) ([]WaitingInstance, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := c.db.QueryContext(ctx, `
		SELECT wi.id, wi.workflow_id, COALESCE(wi.task_id,''), wi.cell_id,
		       COALESCE(it.title,''), wi.state, COALESCE(wi.blocked_reason,''),
		       COALESCE(sr.agent_id,''), COALESCE(sr.step_id,''),
		       wi.created_at, wi.updated_at
		FROM workflow_instances wi
		LEFT JOIN internal_tasks it ON it.id = wi.task_id
		LEFT JOIN step_runs sr
		       ON sr.workflow_instance_id = wi.id
		      AND sr.state = 'running'
		      AND sr.finished_at IS NULL
		WHERE wi.state NOT IN ('done','failed','canceled')
		ORDER BY wi.updated_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WaitingInstance
	for rows.Next() {
		var w WaitingInstance
		if err := rows.Scan(&w.ID, &w.WorkflowID, &w.TaskID, &w.CellID, &w.Title,
			&w.State, &w.BlockedReason, &w.AgentID, &w.StepID, &w.CreatedAt, &w.UpdatedAt); err != nil {
			continue
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
