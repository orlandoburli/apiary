package db

import (
	"context"
	"database/sql"
	"time"
)

// Workflow instance states.
const (
	InstanceStatePending         = "pending"
	InstanceStateRunning         = "running"
	InstanceStateApprovalWaiting = "approval_waiting"
	InstanceStateInterrupted     = "interrupted"
	InstanceStateDone            = "done"
	InstanceStateFailed          = "failed"
)

// Step run states.
const (
	StepStatePending       = "pending"
	StepStateRunning       = "running"
	StepStatePassed        = "passed"
	StepStateFailed        = "failed"
	StepStateSkipped       = "skipped"
	StepStateSkippedCached = "skipped_cached"
)

// WorkflowInstance is a single execution of a workflow bound to a Cell.
type WorkflowInstance struct {
	ID               string
	WorkflowID       string
	CellID           string
	SourceID         string
	State            string
	ParentInstanceID string
	ResumedFrom      string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// StepRun records one step execution within a workflow instance.
type StepRun struct {
	ID                 string
	WorkflowInstanceID string
	StepID             string
	AgentID            string
	State              string
	Output             string
	StructuredOutput   string // JSON-encoded
	Summary            string
	ExitCode           int
	SkippedCached      bool
	StartedAt          *time.Time
	FinishedAt         *time.Time
}

// CreateWorkflowInstance inserts a new workflow instance. The caller supplies
// the ID (the engine generates it) so persistence stays deterministic.
func (c *Client) CreateWorkflowInstance(ctx context.Context, inst *WorkflowInstance) error {
	now := time.Now()
	if inst.CreatedAt.IsZero() {
		inst.CreatedAt = now
	}
	inst.UpdatedAt = now
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO workflow_instances
		  (id, workflow_id, cell_id, source_id, state, parent_instance_id, resumed_from, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, inst.ID, inst.WorkflowID, inst.CellID, nullStr(inst.SourceID), inst.State,
		nullStr(inst.ParentInstanceID), nullStr(inst.ResumedFrom), inst.CreatedAt, inst.UpdatedAt)
	return err
}

// UpdateWorkflowInstanceState transitions an instance to a new state.
func (c *Client) UpdateWorkflowInstanceState(ctx context.Context, id, state string) error {
	_, err := c.db.ExecContext(ctx, `
		UPDATE workflow_instances SET state = ?, updated_at = ? WHERE id = ?
	`, state, time.Now(), id)
	return err
}

// GetWorkflowInstance fetches an instance by ID, or (nil, nil) if not found.
func (c *Client) GetWorkflowInstance(ctx context.Context, id string) (*WorkflowInstance, error) {
	row := c.db.QueryRowContext(ctx, `
		SELECT id, workflow_id, cell_id, COALESCE(source_id,''), state,
		       COALESCE(parent_instance_id,''), COALESCE(resumed_from,''), created_at, updated_at
		FROM workflow_instances WHERE id = ?
	`, id)
	inst, err := scanInstance(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return inst, err
}

// ListWorkflowInstancesByState returns all instances in the given state, oldest first.
func (c *Client) ListWorkflowInstancesByState(ctx context.Context, state string) ([]WorkflowInstance, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, workflow_id, cell_id, COALESCE(source_id,''), state,
		       COALESCE(parent_instance_id,''), COALESCE(resumed_from,''), created_at, updated_at
		FROM workflow_instances WHERE state = ? ORDER BY created_at ASC
	`, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanInstances(rows)
}

// ListWorkflowInstances returns the most recent instances, newest first.
func (c *Client) ListWorkflowInstances(ctx context.Context, limit int) ([]WorkflowInstance, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, workflow_id, cell_id, COALESCE(source_id,''), state,
		       COALESCE(parent_instance_id,''), COALESCE(resumed_from,''), created_at, updated_at
		FROM workflow_instances ORDER BY created_at DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanInstances(rows)
}

// ReconcileOrphanWorkflowInstances marks any instance left in the 'running'
// state by a previously-killed process as 'interrupted'. Returns the count.
// approval_waiting instances are intentionally left untouched — their condition
// is re-evaluated against the live task on the next poll.
func (c *Client) ReconcileOrphanWorkflowInstances(ctx context.Context) (int64, error) {
	res, err := c.db.ExecContext(ctx, `
		UPDATE workflow_instances
		SET state = 'interrupted', updated_at = ?
		WHERE state = 'running'
	`, time.Now())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// CreateStepRun inserts a new step run row.
func (c *Client) CreateStepRun(ctx context.Context, sr *StepRun) error {
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO step_runs
		  (id, workflow_instance_id, step_id, agent_id, state, output, structured_output,
		   summary, exit_code, skipped_cached, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, sr.ID, sr.WorkflowInstanceID, sr.StepID, nullStr(sr.AgentID), sr.State,
		nullStr(sr.Output), nullStr(sr.StructuredOutput), nullStr(sr.Summary),
		sr.ExitCode, sr.SkippedCached, sr.StartedAt, sr.FinishedAt)
	return err
}

// UpdateStepRun persists the mutable fields of a step run (state, output,
// structured output, summary, exit code, skipped flag, finished timestamp).
func (c *Client) UpdateStepRun(ctx context.Context, sr *StepRun) error {
	_, err := c.db.ExecContext(ctx, `
		UPDATE step_runs
		SET state = ?, output = ?, structured_output = ?, summary = ?,
		    exit_code = ?, skipped_cached = ?, started_at = ?, finished_at = ?
		WHERE id = ?
	`, sr.State, nullStr(sr.Output), nullStr(sr.StructuredOutput), nullStr(sr.Summary),
		sr.ExitCode, sr.SkippedCached, sr.StartedAt, sr.FinishedAt, sr.ID)
	return err
}

// ListStepRuns returns all step runs for an instance, in insertion order.
func (c *Client) ListStepRuns(ctx context.Context, instanceID string) ([]StepRun, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, workflow_instance_id, step_id, COALESCE(agent_id,''), state,
		       COALESCE(output,''), COALESCE(structured_output,''), COALESCE(summary,''),
		       COALESCE(exit_code,0), COALESCE(skipped_cached,0), started_at, finished_at
		FROM step_runs WHERE workflow_instance_id = ? ORDER BY rowid ASC
	`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StepRun
	for rows.Next() {
		var sr StepRun
		if err := rows.Scan(&sr.ID, &sr.WorkflowInstanceID, &sr.StepID, &sr.AgentID, &sr.State,
			&sr.Output, &sr.StructuredOutput, &sr.Summary, &sr.ExitCode, &sr.SkippedCached,
			&sr.StartedAt, &sr.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, sr)
	}
	return out, rows.Err()
}

// scanner abstracts *sql.Row and *sql.Rows for shared scanning.
type scanner interface {
	Scan(dest ...any) error
}

func scanInstance(s scanner) (*WorkflowInstance, error) {
	var inst WorkflowInstance
	err := s.Scan(&inst.ID, &inst.WorkflowID, &inst.CellID, &inst.SourceID, &inst.State,
		&inst.ParentInstanceID, &inst.ResumedFrom, &inst.CreatedAt, &inst.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &inst, nil
}

func scanInstances(rows *sql.Rows) ([]WorkflowInstance, error) {
	var out []WorkflowInstance
	for rows.Next() {
		inst, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *inst)
	}
	return out, rows.Err()
}

// nullStr converts an empty string to a SQL NULL so optional columns stay null
// rather than storing empty strings.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
