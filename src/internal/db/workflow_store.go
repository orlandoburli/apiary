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
	InstanceStatePollWaiting     = "poll_waiting"
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

// Publish states for a step run's APIARY_PUBLISH write-back.
const (
	PublishStateSent    = "sent"    // payload posted to all source bindings
	PublishStateFailed  = "failed"  // a binding's PostComment returned an error
	PublishStateSkipped = "skipped" // payload present but task has no bindings
)

// WorkflowInstance is a single execution of a workflow bound to an InternalTask.
// TaskID is the canonical link; CellID/SourceID are retained (and still populated
// from the task's primary source binding) for the dashboard until a future change.
type WorkflowInstance struct {
	ID               string
	WorkflowID       string
	TaskID           string
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
	// PublishPayload is the APIARY_PUBLISH text the step's agent emitted, if any.
	PublishPayload string
	// PublishState records the write-back outcome: sent | failed | skipped.
	// Empty when the step emitted no publish payload.
	PublishState string
	// SpawnedTaskID is the child InternalTask id created when the step emitted an
	// APIARY_SPAWN request. Empty when the step spawned nothing.
	SpawnedTaskID string
	StartedAt     *time.Time
	FinishedAt    *time.Time
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
		  (id, workflow_id, task_id, cell_id, source_id, state, parent_instance_id, resumed_from, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, inst.ID, inst.WorkflowID, nullStr(inst.TaskID), inst.CellID, nullStr(inst.SourceID), inst.State,
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
		       COALESCE(parent_instance_id,''), COALESCE(resumed_from,''), created_at, updated_at, COALESCE(task_id,'')
		FROM workflow_instances WHERE id = ?
	`, id)
	inst, err := scanInstance(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return inst, err
}

// HasFailedInstance reports whether any workflow instance bound to the given task
// is in the failed state. The engine calls it when a task's last outstanding
// workflow settles, to choose between the tasks: on_complete and on_fail hooks.
func (c *Client) HasFailedInstance(ctx context.Context, taskID string) (bool, error) {
	var n int
	err := c.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM workflow_instances WHERE task_id = ? AND state = ?
	`, taskID, InstanceStateFailed).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return n > 0, err
}

// CountConsecutiveFailedInstances returns how many of the most recent workflow
// instances for (task, workflow) are in the failed state, counting back from the
// newest until a non-failed one (or the start). A later success/done resets the
// count to zero. Ordered by rowid (insertion order) so it is robust to the
// timestamp format stored in created_at. Used as the re-dispatch failure cap.
func (c *Client) CountConsecutiveFailedInstances(ctx context.Context, taskID, workflowID string) (int, error) {
	var n int
	err := c.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM workflow_instances
		WHERE task_id = ? AND workflow_id = ? AND state = ?
		  AND rowid > COALESCE((
		    SELECT MAX(rowid) FROM workflow_instances
		    WHERE task_id = ? AND workflow_id = ? AND state != ?
		  ), 0)
	`, taskID, workflowID, InstanceStateFailed, taskID, workflowID, InstanceStateFailed).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

// HasActiveInstanceForRoute reports whether the task already has a non-terminal
// instance for the given workflow — running, approval_waiting, or poll_waiting.
// The dispatcher uses it as a source-agnostic in-flight guard: it stops a later
// poll from dispatching a duplicate while the workflow runs or, crucially, waits
// at an approval or poll step (where the in-memory inFlight marker has already
// been released). Keyed on (task, workflow) so a completed earlier workflow (e.g.
// triage) does not block the next one a hand-off routes to. terminal/interrupted
// states do not block — they remain eligible for retry or manual resume.
func (c *Client) HasActiveInstanceForRoute(ctx context.Context, taskID, workflowID string) (bool, error) {
	var n int
	err := c.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM workflow_instances
		WHERE task_id = ? AND workflow_id = ? AND state IN (?, ?, ?)
	`, taskID, workflowID, InstanceStateRunning, InstanceStateApprovalWaiting, InstanceStatePollWaiting).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return n > 0, err
}

// ListWorkflowInstancesByState returns all instances in the given state, oldest first.
func (c *Client) ListWorkflowInstancesByState(ctx context.Context, state string) ([]WorkflowInstance, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, workflow_id, cell_id, COALESCE(source_id,''), state,
		       COALESCE(parent_instance_id,''), COALESCE(resumed_from,''), created_at, updated_at, COALESCE(task_id,'')
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
		       COALESCE(parent_instance_id,''), COALESCE(resumed_from,''), created_at, updated_at, COALESCE(task_id,'')
		FROM workflow_instances ORDER BY created_at DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanInstances(rows)
}

// GetLatestInstanceByCell returns the most recent workflow instance bound to a
// cell, or (nil, nil) when the cell has no workflow instance.
func (c *Client) GetLatestInstanceByCell(ctx context.Context, cellID string) (*WorkflowInstance, error) {
	row := c.db.QueryRowContext(ctx, `
		SELECT id, workflow_id, cell_id, COALESCE(source_id,''), state,
		       COALESCE(parent_instance_id,''), COALESCE(resumed_from,''), created_at, updated_at, COALESCE(task_id,'')
		FROM workflow_instances WHERE cell_id = ? ORDER BY created_at DESC LIMIT 1
	`, cellID)
	inst, err := scanInstance(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return inst, err
}

// ListWorkflowInstancesByCell returns every workflow instance keyed by a cell id,
// newest first. Used by task deletion to catch orphaned instances and instances
// written before task_id existed, which ListWorkflowInstancesByTask would miss.
func (c *Client) ListWorkflowInstancesByCell(ctx context.Context, cellID string) ([]WorkflowInstance, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, workflow_id, cell_id, COALESCE(source_id,''), state,
		       COALESCE(parent_instance_id,''), COALESCE(resumed_from,''), created_at, updated_at, COALESCE(task_id,'')
		FROM workflow_instances WHERE cell_id = ? ORDER BY created_at DESC
	`, cellID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanInstances(rows)
}

// ListWorkflowInstancesByTask returns every workflow instance bound to an
// InternalTask, newest first. A task may fan out to several workflows (Phase 4),
// so the dashboard lists all of them per task. Backed by idx_wf_instances_task.
func (c *Client) ListWorkflowInstancesByTask(ctx context.Context, taskID string) ([]WorkflowInstance, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, workflow_id, cell_id, COALESCE(source_id,''), state,
		       COALESCE(parent_instance_id,''), COALESCE(resumed_from,''), created_at, updated_at, COALESCE(task_id,'')
		FROM workflow_instances WHERE task_id = ? ORDER BY created_at DESC
	`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanInstances(rows)
}

// LatestResumableInstance returns the most recent failed or interrupted
// instance of a workflow, or (nil, nil) when none exists.
func (c *Client) LatestResumableInstance(ctx context.Context, workflowID string) (*WorkflowInstance, error) {
	row := c.db.QueryRowContext(ctx, `
		SELECT id, workflow_id, cell_id, COALESCE(source_id,''), state,
		       COALESCE(parent_instance_id,''), COALESCE(resumed_from,''), created_at, updated_at, COALESCE(task_id,'')
		FROM workflow_instances
		WHERE workflow_id = ? AND state IN ('failed','interrupted')
		ORDER BY created_at DESC LIMIT 1
	`, workflowID)
	inst, err := scanInstance(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return inst, err
}

// WorkflowInstanceView is a WorkflowInstance enriched with the cell's title,
// used by the `apiary instances` listing.
type WorkflowInstanceView struct {
	WorkflowInstance
	Title string
}

// ListWorkflowInstanceViews returns recent instances (newest first) joined with
// the cell title, optionally filtered by state and/or workflow id.
func (c *Client) ListWorkflowInstanceViews(ctx context.Context, state, workflowID string, limit int) ([]WorkflowInstanceView, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `
		SELECT wi.id, wi.workflow_id, wi.cell_id, COALESCE(wi.source_id,''), wi.state,
		       COALESCE(wi.parent_instance_id,''), COALESCE(wi.resumed_from,''),
		       wi.created_at, wi.updated_at, COALESCE(wi.task_id,''), COALESCE(t.title,'')
		FROM workflow_instances wi
		LEFT JOIN tasks t ON t.id = wi.cell_id
		WHERE 1=1`
	var args []any
	if state != "" {
		q += " AND wi.state = ?"
		args = append(args, state)
	}
	if workflowID != "" {
		q += " AND wi.workflow_id = ?"
		args = append(args, workflowID)
	}
	q += " ORDER BY wi.created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := c.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WorkflowInstanceView
	for rows.Next() {
		var v WorkflowInstanceView
		if err := rows.Scan(&v.ID, &v.WorkflowID, &v.CellID, &v.SourceID, &v.State,
			&v.ParentInstanceID, &v.ResumedFrom, &v.CreatedAt, &v.UpdatedAt, &v.TaskID, &v.Title); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetTaskTitle returns the stored title for a cell/task id, or "" if unknown.
func (c *Client) GetTaskTitle(ctx context.Context, id string) (string, error) {
	var title string
	err := c.db.QueryRowContext(ctx, `SELECT COALESCE(title,'') FROM tasks WHERE id = ?`, id).Scan(&title)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return title, err
}

// ReconcileOrphanWorkflowInstances marks any instance left in the 'running'
// state by a previously-killed process as 'interrupted'. A fresh daemon process
// owns no in-flight workflow runs, so rows left 'running' are orphans that would
// otherwise cause tasks to appear stuck. Marking them interrupted allows the next
// poll to dispatch fresh instances. approval_waiting and poll_waiting instances
// are deliberately left untouched (the WHERE only matches 'running') — they are
// rehydrated separately via rehydrateParkedApprovals / rehydrateParkedPolls.
func (c *Client) ReconcileOrphanWorkflowInstances(ctx context.Context) (int64, error) {
	res, err := c.db.ExecContext(ctx, `
		UPDATE workflow_instances
		SET state = ?, updated_at = ?
		WHERE state = ?
	`, InstanceStateInterrupted, time.Now(), InstanceStateRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CreateStepRun inserts a new step run row.
func (c *Client) CreateStepRun(ctx context.Context, sr *StepRun) error {
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO step_runs
		  (id, workflow_instance_id, step_id, agent_id, state, output, structured_output,
		   summary, exit_code, skipped_cached, publish_payload, publish_state, spawned_task_id,
		   started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, sr.ID, sr.WorkflowInstanceID, sr.StepID, nullStr(sr.AgentID), sr.State,
		nullStr(sr.Output), nullStr(sr.StructuredOutput), nullStr(sr.Summary),
		sr.ExitCode, sr.SkippedCached, nullStr(sr.PublishPayload), nullStr(sr.PublishState),
		nullStr(sr.SpawnedTaskID), sr.StartedAt, sr.FinishedAt)
	return err
}

// UpdateStepRun persists the mutable fields of a step run (state, output,
// structured output, summary, exit code, skipped flag, finished timestamp).
func (c *Client) UpdateStepRun(ctx context.Context, sr *StepRun) error {
	_, err := c.db.ExecContext(ctx, `
		UPDATE step_runs
		SET state = ?, output = ?, structured_output = ?, summary = ?,
		    exit_code = ?, skipped_cached = ?, publish_payload = ?, publish_state = ?,
		    spawned_task_id = ?, started_at = ?, finished_at = ?
		WHERE id = ?
	`, sr.State, nullStr(sr.Output), nullStr(sr.StructuredOutput), nullStr(sr.Summary),
		sr.ExitCode, sr.SkippedCached, nullStr(sr.PublishPayload), nullStr(sr.PublishState),
		nullStr(sr.SpawnedTaskID), sr.StartedAt, sr.FinishedAt, sr.ID)
	return err
}

// ListStepRuns returns all step runs for an instance, in insertion order.
func (c *Client) ListStepRuns(ctx context.Context, instanceID string) ([]StepRun, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, workflow_instance_id, step_id, COALESCE(agent_id,''), state,
		       COALESCE(output,''), COALESCE(structured_output,''), COALESCE(summary,''),
		       COALESCE(exit_code,0), COALESCE(skipped_cached,0),
		       COALESCE(publish_payload,''), COALESCE(publish_state,''),
		       COALESCE(spawned_task_id,''), started_at, finished_at
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
			&sr.PublishPayload, &sr.PublishState, &sr.SpawnedTaskID, &sr.StartedAt, &sr.FinishedAt); err != nil {
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
		&inst.ParentInstanceID, &inst.ResumedFrom, &inst.CreatedAt, &inst.UpdatedAt, &inst.TaskID)
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

// DeleteWorkflowInstances removes workflow instances and all their step runs and logs.
// Safe to call with a list of instance IDs; deleted instances are cascaded via foreign keys.
func (c *Client) DeleteWorkflowInstances(ctx context.Context, instanceIDs []string) error {
	if len(instanceIDs) == 0 {
		return nil
	}

	// Build a placeholder list for the IN clause.
	placeholders := ""
	args := make([]any, len(instanceIDs))
	for i, id := range instanceIDs {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args[i] = id
	}

	// Delete step_runs for these instances (cascading delete would handle this,
	// but being explicit is safer).
	stmt := "DELETE FROM step_runs WHERE workflow_instance_id IN (" + placeholders + ")"
	if _, err := c.db.ExecContext(ctx, stmt, args...); err != nil {
		return err
	}

	// Delete the workflow instances themselves.
	stmt = "DELETE FROM workflow_instances WHERE id IN (" + placeholders + ")"
	if _, err := c.db.ExecContext(ctx, stmt, args...); err != nil {
		return err
	}

	return nil
}
