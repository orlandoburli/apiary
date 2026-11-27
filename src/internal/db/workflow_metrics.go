package db

import (
	"context"
	"time"
)

// WorkflowMetrics is one workflow's activity over a window.
//
// It is the same two-window idea the Overview uses, per workflow: Runs counts
// the instances that started inside the window, while Completed/Failed count
// the ones that reached a terminal state inside it. A workflow that started
// before the window and finished inside it is a completion but not a run.
type WorkflowMetrics struct {
	WorkflowID string
	Runs       int
	Completed  int
	Failed     int
	// AvgDurationMs is measured over the cohort that both started and finished
	// in the window. An instance created days ago and finished today would
	// otherwise report its whole blocked life as duration.
	AvgDurationMs int64
}

// StepMetrics is one step's activity across a workflow's instances.
type StepMetrics struct {
	WorkflowID string
	StepID     string
	AgentID    string
	Runs       int
	Completed  int
	Failed     int
	// AvgDurationMs is measured over finished runs only: a run that has not
	// finished has no duration to average.
	AvgDurationMs int64
}

// GetWorkflowMetrics returns per-workflow and per-step activity since a window
// start, ordered by activity so the busiest workflow and its busiest steps
// come first.
//
// The lists are returned separately rather than nested: the Workflows tab draws
// the list and the selected workflow's steps from different panels, and a
// caller should not have to walk a tree to render one row.
func (c *Client) GetWorkflowMetrics(ctx context.Context, since time.Time) ([]WorkflowMetrics, []StepMetrics, error) {
	instRows, err := c.db.QueryContext(ctx, `
		SELECT workflow_id,
		       COUNT(CASE WHEN created_at >= ? THEN 1 END),
		       COUNT(CASE WHEN state = 'done' AND updated_at >= ? THEN 1 END),
		       COUNT(CASE WHEN state = 'failed' AND updated_at >= ? THEN 1 END),
		       COALESCE(CAST(AVG(CASE
		         WHEN state IN ('done','failed') AND created_at >= ? AND updated_at >= ?
		         THEN (julianday(updated_at) - julianday(created_at)) * 86400000
		       END) AS INTEGER), 0)
		FROM workflow_instances
		WHERE created_at >= ? OR updated_at >= ?
		GROUP BY workflow_id
		ORDER BY COUNT(CASE WHEN created_at >= ? THEN 1 END) DESC, workflow_id
	`, since, since, since, since, since, since, since, since)
	if err != nil {
		return nil, nil, err
	}
	defer instRows.Close()

	var workflows []WorkflowMetrics
	for instRows.Next() {
		var w WorkflowMetrics
		if err := instRows.Scan(&w.WorkflowID, &w.Runs, &w.Completed, &w.Failed, &w.AvgDurationMs); err != nil {
			return nil, nil, err
		}
		workflows = append(workflows, w)
	}
	if err := instRows.Err(); err != nil {
		return nil, nil, err
	}

	stepRows, err := c.db.QueryContext(ctx, `
		SELECT wi.workflow_id, sr.step_id, COALESCE(MAX(sr.agent_id),''),
		       COUNT(CASE WHEN sr.started_at >= ? THEN 1 END),
		       COUNT(CASE WHEN sr.state = 'done' AND sr.started_at >= ? THEN 1 END),
		       COUNT(CASE WHEN sr.state = 'failed' AND sr.started_at >= ? THEN 1 END),
		       COALESCE(CAST(AVG(CASE
		         WHEN sr.state IN ('done','failed')
		          AND sr.started_at >= ? AND sr.finished_at >= ?
		         THEN (julianday(sr.finished_at) - julianday(sr.started_at)) * 86400000
		       END) AS INTEGER), 0)
		FROM step_runs sr
		JOIN workflow_instances wi ON wi.id = sr.workflow_instance_id
		WHERE sr.started_at >= ? OR sr.finished_at >= ?
		GROUP BY wi.workflow_id, sr.step_id
		ORDER BY wi.workflow_id, sr.step_id
	`, since, since, since, since, since, since, since)
	if err != nil {
		return nil, nil, err
	}
	defer stepRows.Close()

	var steps []StepMetrics
	for stepRows.Next() {
		var s StepMetrics
		if err := stepRows.Scan(&s.WorkflowID, &s.StepID, &s.AgentID, &s.Runs, &s.Completed, &s.Failed, &s.AvgDurationMs); err != nil {
			return nil, nil, err
		}
		steps = append(steps, s)
	}
	return workflows, steps, stepRows.Err()
}
