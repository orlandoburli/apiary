package db

import (
	"context"
	"time"
)

// UnpricedExecution is a finished execution awaiting a cost back-fill: it ran
// on a runner that does not report cost (cursor-cli), consumed tokens, and has
// cost_usd = 0.
type UnpricedExecution struct {
	ID                 int64
	StartedAt          time.Time
	CompletedAt        time.Time
	TotalTokens        int
	WorkflowInstanceID string
	StepID             string
}

// ListUnpricedExecutions returns terminal executions of the given runner with
// zero cost and non-zero token usage, completed since the given time. Ordered
// oldest-first so back-fill progress is stable across sweeps.
func (c *Client) ListUnpricedExecutions(ctx context.Context, runner string, since time.Time) ([]UnpricedExecution, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, started_at, completed_at, total_tokens,
		       COALESCE(workflow_instance_id, ''), COALESCE(step_id, '')
		FROM task_executions
		WHERE runner = ? AND status != 'running' AND cost_usd = 0
		  AND total_tokens > 0
		  AND started_at IS NOT NULL AND completed_at IS NOT NULL
		  AND completed_at >= ?
		ORDER BY completed_at ASC
	`, runner, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []UnpricedExecution
	for rows.Next() {
		var u UnpricedExecution
		if err := rows.Scan(&u.ID, &u.StartedAt, &u.CompletedAt, &u.TotalTokens, &u.WorkflowInstanceID, &u.StepID); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetExecutionCost back-fills cost_usd on one execution without touching any
// other column (the row may be re-read concurrently by dashboards).
func (c *Client) SetExecutionCost(ctx context.Context, execID int64, costUSD float64) error {
	_, err := c.db.ExecContext(ctx, `
		UPDATE task_executions SET cost_usd = ? WHERE id = ?
	`, costUSD, execID)
	return err
}

// RefreshStepRunCost re-derives a step run's cost_usd as the sum of its linked
// executions (a step may have several attempts via failover), so instance
// detail views stay consistent after an execution-level cost back-fill.
func (c *Client) RefreshStepRunCost(ctx context.Context, instanceID, stepID string) error {
	if instanceID == "" || stepID == "" {
		return nil
	}
	_, err := c.db.ExecContext(ctx, `
		UPDATE step_runs
		SET cost_usd = (
			SELECT COALESCE(SUM(cost_usd), 0) FROM task_executions
			WHERE workflow_instance_id = ? AND step_id = ?
		)
		WHERE workflow_instance_id = ? AND step_id = ?
	`, instanceID, stepID, instanceID, stepID)
	return err
}
