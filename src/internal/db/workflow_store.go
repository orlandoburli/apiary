package db

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/state"
)

// Workflow instance states — canonical values from internal/state (#465).
//
// The three parked states collapse onto 'blocked' and are told apart by
// blocked_reason, so InstanceStateApprovalWaiting, InstanceStateWaiting and
// InstanceStateInterrupted are all "blocked" and differ only in the reason
// written alongside. Compare on the reason, never on these three names, when the
// distinction matters — see ReconcileOrphanTaskCounters for the one place it does.
const (
	InstanceStateQueued   = "queued"
	InstanceStateRunning  = "running"
	InstanceStateBlocked  = "blocked"
	InstanceStateDone     = "done"
	InstanceStateFailed   = "failed"
	InstanceStateCanceled = "canceled"
)

// InterruptedInstance reports whether an instance is an orphan: blocked because
// execution stopped abnormally, rather than blocked awaiting something that will
// arrive. The distinction used to be carried by a separate 'interrupted' state;
// it is now the reason, and every caller that cared about it must say so
// explicitly. The legacy value is still recognised for un-migrated rows.
func InterruptedInstance(st, reason string) bool {
	return st == "interrupted" ||
		(st == InstanceStateBlocked && reason == string(state.ReasonInterrupted))
}

// Step run states.
const (
	StepStateQueued  = "queued"
	StepStateRunning = "running"
	StepStateBlocked = "blocked"
	StepStatePassed  = "done"
	StepStateFailed  = "failed"
	StepStateSkipped = "skipped"
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
	ID         string
	WorkflowID string
	TaskID     string
	CellID     string
	SourceID   string
	State      string
	// BlockedReason explains a State of "blocked": approval | ci | dependency |
	// retry_backoff | interrupted. Empty for every other state.
	BlockedReason    string
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
	// BlockedReason explains a State of "blocked"; SkippedReason explains
	// "skipped" (currently only "cached"). Both empty otherwise.
	BlockedReason    string
	SkippedReason    string
	Output           string
	StructuredOutput string // JSON-encoded
	Summary          string
	ExitCode         int
	SkippedCached    bool
	// PublishPayload is the APIARY_PUBLISH text the step's agent emitted, if any.
	PublishPayload string
	// PublishState records the write-back outcome: sent | failed | skipped.
	// Empty when the step emitted no publish payload.
	PublishState string
	// SpawnedTaskID is the child InternalTask id created when the step emitted an
	// APIARY_SPAWN request. Empty when the step spawned nothing.
	SpawnedTaskID string
	// InputPrompt is the composed prompt of the step's final (winning) attempt.
	InputPrompt string
	// Token/cost rollup, summed across the step's failover attempts. Per-attempt
	// detail lives in the linked task_executions rows.
	InputTokens         int
	OutputTokens        int
	TotalTokens         int
	CacheCreationTokens int
	CacheReadTokens     int
	NumTurns            int
	NumToolCalls        int
	CostUSD             float64
	// StepTiming is the wall-clock attribution rollup, summed across the step's
	// failover attempts alongside the token columns above (issue #399).
	StepTiming
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// CreateWorkflowInstance inserts a new workflow instance. The caller supplies
// the ID (the engine generates it) so persistence stays deterministic. The row
// is stamped with the owning task's current dispatch generation (0 for
// transient/unknown task ids), which scopes failure aggregation to the round
// the instance was dispatched in.
func (c *Client) CreateWorkflowInstance(ctx context.Context, inst *WorkflowInstance) error {
	now := time.Now()
	if inst.CreatedAt.IsZero() {
		inst.CreatedAt = now
	}
	inst.UpdatedAt = now
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO workflow_instances
		  (id, workflow_id, task_id, cell_id, source_id, state, blocked_reason, parent_instance_id, resumed_from,
		   task_generation, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?,
		        COALESCE((SELECT generation FROM internal_tasks WHERE id = ?), 0), ?, ?)
	`, inst.ID, inst.WorkflowID, nullStr(inst.TaskID), inst.CellID, nullStr(inst.SourceID), inst.State,
		nullStr(inst.BlockedReason),
		nullStr(inst.ParentInstanceID), nullStr(inst.ResumedFrom), inst.TaskID, inst.CreatedAt, inst.UpdatedAt)
	if err == nil {
		c.recordWorkflowEvent(ctx, inst, "workflow.started", map[string]any{"state": inst.State})
		if inst.ResumedFrom != "" {
			c.recordWorkflowEvent(ctx, inst, "workflow.resumed", map[string]any{"resumed_from": inst.ResumedFrom})
		}
	}
	return err
}

// UpdateWorkflowInstanceState transitions an instance to a new state, together
// with the reason when that state is "blocked".
//
// The reason is a parameter rather than something the store infers because
// "blocked" alone does not say whether the run is waiting on a human, on CI, or
// was interrupted — and those three had distinct states before the vocabulary
// was unified (#465). Pass "" for any state other than blocked.
func (c *Client) UpdateWorkflowInstanceState(ctx context.Context, id, newState, reason string) error {
	inst, _ := c.GetWorkflowInstance(ctx, id)
	if inst != nil && inst.State == newState && inst.BlockedReason == reason {
		return nil
	}
	_, err := c.db.ExecContext(ctx, `
		UPDATE workflow_instances SET state = ?, blocked_reason = ?, updated_at = ? WHERE id = ?
	`, newState, nullStr(reason), time.Now(), id)
	if err == nil && inst != nil {
		inst.State = newState
		inst.BlockedReason = reason
		if eventType := workflowStateEventType(newState, reason); eventType != "" {
			meta := map[string]any{"state": newState}
			if reason != "" {
				meta["reason"] = reason
			}
			c.recordWorkflowEvent(ctx, inst, eventType, meta)
		}
	}
	return err
}

// GetWorkflowInstance fetches an instance by ID, or (nil, nil) if not found.
func (c *Client) GetWorkflowInstance(ctx context.Context, id string) (*WorkflowInstance, error) {
	row := c.db.QueryRowContext(ctx, `
		SELECT id, workflow_id, cell_id, COALESCE(source_id,''), state, COALESCE(blocked_reason,''),
		       COALESCE(parent_instance_id,''), COALESCE(resumed_from,''), created_at, updated_at, COALESCE(task_id,'')
		FROM workflow_instances WHERE id = ?
	`, id)
	inst, err := scanInstance(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return inst, err
}

// HasFailedInstance reports whether any workflow instance from the task's
// current dispatch generation is in the failed state. The engine calls it when
// a task's last outstanding workflow settles, to choose between the tasks:
// on_complete and on_fail hooks. Scoping to the current generation keeps
// any-fail semantics within a round (a failed sibling in a parallel fan-out
// still fails the task) while letting a later successful re-dispatch or
// escalation settle the task as done instead of being poisoned by earlier
// rounds' failures.
func (c *Client) HasFailedInstance(ctx context.Context, taskID string) (bool, error) {
	var n int
	err := c.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM workflow_instances
		WHERE task_id = ? AND state = ?
		  AND task_generation = COALESCE((SELECT generation FROM internal_tasks WHERE id = ?), 0)
	`, taskID, InstanceStateFailed, taskID).Scan(&n)
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
// instance for the given workflow — running, approval_waiting, or waiting.
// The dispatcher uses it as a source-agnostic in-flight guard: it stops a later
// poll from dispatching a duplicate while the workflow runs or, crucially, waits
// at an approval or poll step (where the in-memory inFlight marker has already
// been released). Keyed on (task, workflow) so a completed earlier workflow (e.g.
// triage) does not block the next one a hand-off routes to. terminal/interrupted
// states do not block — they remain eligible for retry or manual resume.
func (c *Client) HasActiveInstanceForRoute(ctx context.Context, taskID, workflowID string) (bool, error) {
	var n int
	// Active means running or parked-and-alive. An interrupted instance is
	// blocked but dead: it must NOT shadow a re-dispatch, or a daemon restart
	// would strand the workflow forever. The legacy 'running'/'approval_waiting'
	// /'waiting' values are still matched so an un-migrated database behaves the
	// same (#465).
	err := c.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM workflow_instances
		WHERE task_id = ? AND workflow_id = ?
		  AND (
		        state IN ('running','approval_waiting','waiting')
		     OR (state = 'blocked' AND COALESCE(blocked_reason,'') <> ?)
		      )
	`, taskID, workflowID, string(state.ReasonInterrupted)).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return n > 0, err
}

// HasCompletedInstanceForRoute reports whether the task already has a successfully
// completed (done) instance of the given workflow. The dispatcher uses it to honor
// a trigger's `once: true`: a run-at-most-once workflow (e.g. a spec decomposition)
// is not re-dispatched after it has succeeded, even when its source item stays in
// the trigger set — the guard against duplicate fan-out (issue #119). Only the
// done state counts; a failed instance stays eligible for retry.
func (c *Client) HasCompletedInstanceForRoute(ctx context.Context, taskID, workflowID string) (bool, error) {
	var n int
	err := c.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM workflow_instances
		WHERE task_id = ? AND workflow_id = ? AND state = ?
	`, taskID, workflowID, InstanceStateDone).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return n > 0, err
}

// HasResumeDescendant reports whether some instance was already created as a
// resume of the given instance (its resumed_from points at it). Startup
// auto-resume (resume: auto) uses it so an interrupted instance is replayed at
// most once: a later restart must not fork a second descendant from an ancestor
// that has already been continued.
func (c *Client) HasResumeDescendant(ctx context.Context, instanceID string) (bool, error) {
	var n int
	err := c.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM workflow_instances WHERE resumed_from = ?
	`, instanceID).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return n > 0, err
}

// ListWorkflowInstancesByState returns all instances in the given state, oldest first.
func (c *Client) ListWorkflowInstancesByState(ctx context.Context, state string) ([]WorkflowInstance, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, workflow_id, cell_id, COALESCE(source_id,''), state, COALESCE(blocked_reason,''),
		       COALESCE(parent_instance_id,''), COALESCE(resumed_from,''), created_at, updated_at, COALESCE(task_id,'')
		FROM workflow_instances WHERE state = ? ORDER BY created_at ASC
	`, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanInstances(rows)
}

// ListWorkflowInstancesBlockedBy returns instances parked for any of the given
// reasons, oldest first.
//
// Rehydration needs this rather than a state lookup: every park is 'blocked'
// now, and an approval rehydrator that matched the state alone would also pick
// up CI waits and orphans (#465). The legacy per-reason states are matched too,
// so an un-migrated database rehydrates identically.
func (c *Client) ListWorkflowInstancesBlockedBy(ctx context.Context, reasons ...string) ([]WorkflowInstance, error) {
	if len(reasons) == 0 {
		return nil, nil
	}
	args := []any{}
	placeholders := ""
	for i, r := range reasons {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, r)
	}
	// Legacy state names carrying the same meaning as each reason.
	legacy := map[string]string{
		"approval":    "approval_waiting",
		"ci":          "waiting",
		"dependency":  "waiting",
		"interrupted": "interrupted",
	}
	legacyPlaceholders := ""
	seen := map[string]bool{}
	for _, r := range reasons {
		if l, ok := legacy[r]; ok && !seen[l] {
			seen[l] = true
			if legacyPlaceholders != "" {
				legacyPlaceholders += ","
			}
			legacyPlaceholders += "?"
			args = append(args, l)
		}
	}
	if legacyPlaceholders == "" {
		legacyPlaceholders = "NULL"
	}
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, workflow_id, cell_id, COALESCE(source_id,''), state, COALESCE(blocked_reason,''),
		       COALESCE(parent_instance_id,''), COALESCE(resumed_from,''), created_at, updated_at, COALESCE(task_id,'')
		FROM workflow_instances
		WHERE (state = 'blocked' AND COALESCE(blocked_reason,'') IN (`+placeholders+`))
		   OR state IN (`+legacyPlaceholders+`)
		ORDER BY created_at ASC
	`, args...)
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
		SELECT id, workflow_id, cell_id, COALESCE(source_id,''), state, COALESCE(blocked_reason,''),
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
		SELECT id, workflow_id, cell_id, COALESCE(source_id,''), state, COALESCE(blocked_reason,''),
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
		SELECT id, workflow_id, cell_id, COALESCE(source_id,''), state, COALESCE(blocked_reason,''),
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
		SELECT id, workflow_id, cell_id, COALESCE(source_id,''), state, COALESCE(blocked_reason,''),
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
		SELECT id, workflow_id, cell_id, COALESCE(source_id,''), state, COALESCE(blocked_reason,''),
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
//
// ticketSourceIDs, when non-nil, restricts the result to instances whose
// source_id is in that set — the ticket-tracker allow-list computed from
// config.Config.TicketSourceIDs, used by `apiary instances --tickets-only` and
// the dashboard's equivalent toggle (issue #475) to exclude routine/plugin-
// sourced runs. A non-nil empty slice (no ticket-tracker sources configured)
// correctly yields no rows. Pass nil to keep today's unfiltered behavior.
func (c *Client) ListWorkflowInstanceViews(ctx context.Context, state, workflowID string, ticketSourceIDs []string, limit int) ([]WorkflowInstanceView, error) {
	if limit <= 0 {
		limit = 20
	}
	if ticketSourceIDs != nil && len(ticketSourceIDs) == 0 {
		return nil, nil
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
	if ticketSourceIDs != nil {
		q += " AND wi.source_id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ticketSourceIDs)), ",") + ")"
		for _, id := range ticketSourceIDs {
			args = append(args, id)
		}
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
// poll to dispatch fresh instances. approval_waiting and waiting instances
// are deliberately left untouched (the WHERE only matches 'running') — they are
// rehydrated separately via rehydrateParkedApprovals / rehydrateParkedWaits.
func (c *Client) ReconcileOrphanWorkflowInstances(ctx context.Context) (int64, error) {
	res, err := c.db.ExecContext(ctx, `
		UPDATE workflow_instances
		SET state = ?, blocked_reason = ?, updated_at = ?
		WHERE state = ?
	`, InstanceStateBlocked, string(state.ReasonInterrupted), time.Now(), InstanceStateRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ReconcileOrphanStepRuns marks step_runs left in a non-terminal state
// (running or pending) as 'interrupted' when their parent workflow_instance is
// itself interrupted. It is the step-level companion to
// ReconcileOrphanWorkflowInstances: when a daemon dies mid-step the step_runs
// row stays 'running' forever, and the dashboard renders a phantom in-progress
// step under an instance that is actually interrupted. Restricting the update to
// children of interrupted instances means a live step under a genuinely running
// instance is never disturbed; it must therefore be called *after*
// ReconcileOrphanWorkflowInstances has flipped the orphaned parents. finished_at
// is stamped only when absent so an already-recorded end time is preserved.
func (c *Client) ReconcileOrphanStepRuns(ctx context.Context) (int64, error) {
	res, err := c.db.ExecContext(ctx, `
		UPDATE step_runs
		SET state = ?, blocked_reason = ?, finished_at = COALESCE(finished_at, ?)
		WHERE state IN (?, ?)
		  AND workflow_instance_id IN (
		    SELECT id FROM workflow_instances
		    WHERE state = ? AND blocked_reason = ?
		  )
	`, StepStateBlocked, string(state.ReasonInterrupted), time.Now(),
		StepStateRunning, StepStateQueued,
		InstanceStateBlocked, string(state.ReasonInterrupted))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PropagateInterruptedToTasks marks a task blocked/interrupted when it has no
// live workflow instance left and at least one interrupted one.
//
// This is the fix for the oldest complaint about the Tasks view: a task whose
// every instance was orphaned by a restart stayed in 'registered' and rendered
// as "queued", indistinguishable from a task that arrived three seconds ago.
// The information was always there in workflow_instances; nothing carried it up
// to the task (#465).
//
// Scoped to non-terminal, non-running tasks with a zero outstanding counter, so
// it runs after ReconcileOrphanTaskCounters has recounted and cannot disturb a
// task that is genuinely working. Idempotent: flipped rows stop matching.
func (c *Client) PropagateInterruptedToTasks(ctx context.Context) (int64, error) {
	res, err := c.db.ExecContext(ctx, `
		UPDATE internal_tasks
		SET state = 'blocked', blocked_reason = ?, updated_at = ?
		WHERE state NOT IN ('done','failed','canceled','running')
		  AND COALESCE(blocked_reason,'') <> ?
		  AND COALESCE(outstanding_workflows, 0) = 0
		  AND EXISTS (
		        SELECT 1 FROM workflow_instances wi
		         WHERE wi.task_id = internal_tasks.id
		           AND (wi.state = 'interrupted'
		                OR (wi.state = 'blocked' AND COALESCE(wi.blocked_reason,'') = ?))
		      )
		  AND NOT EXISTS (
		        SELECT 1 FROM workflow_instances wi
		         WHERE wi.task_id = internal_tasks.id
		           AND (wi.state IN ('running','pending','queued','approval_waiting','waiting')
		                OR (wi.state = 'blocked' AND COALESCE(wi.blocked_reason,'') <> ?))
		      )
	`, string(state.ReasonInterrupted), time.Now(), string(state.ReasonInterrupted),
		string(state.ReasonInterrupted), string(state.ReasonInterrupted))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ReconcileOrphanTaskCounters repairs the outstanding_workflows accounting after
// a daemon restart. The counter is incremented at dispatch and decremented only
// by Engine.completeTask when an instance settles inside a live process — an
// instance flipped to 'interrupted' by ReconcileOrphanWorkflowInstances never
// decrements it, so every mid-flight restart leaks +1 and the task can no longer
// reach zero: it stays 'running' forever even after a later instance completes
// (issue #198).
//
// Two self-healing passes, in order, both scoped to non-terminal tasks:
//
//  1. Recount: set outstanding_workflows to the number of live instances
//     (pending/running/approval_waiting/waiting). Parked instances count — they
//     are rehydrated and will still complete-and-decrement; interrupted/done/
//     failed do not. Recounting (rather than decrementing the rows just
//     interrupted) also repairs counters already leaked by earlier restarts.
//  2. Settle: a 'running' task whose recounted counter is zero and whose current
//     generation has at least one terminal instance gets the lifecycle
//     transition completeTask would have applied — 'failed' if any instance of
//     the current generation failed (mirroring HasFailedInstance), else 'done'.
//     A task whose current-generation instances were all interrupted is left
//     'running': the next poll re-dispatches it and the now-correct counter
//     settles it on completion.
//
// Must run after ReconcileOrphanWorkflowInstances (so orphans are already
// interrupted) and before the rehydration passes.
func (c *Client) ReconcileOrphanTaskCounters(ctx context.Context) (recounted, settled int64, err error) {
	now := time.Now()
	// Live means queued, running, or blocked-for-a-reason-that-will-resolve.
	// Interruption is the exception: a blocked_reason of 'interrupted' marks an
	// orphan that will never complete on its own, so it must not count towards
	// outstanding_workflows or the task can never settle.
	//
	// The legacy vocabulary is still accepted on the read side (a database not
	// yet migrated, or written by an older binary) — see internal/state.
	liveStates := `(
		'queued','running','pending','approval_waiting','waiting'
		)`
	// Qualified with wi. deliberately: the enclosing UPDATE targets
	// internal_tasks, which now has its own state and blocked_reason columns.
	// SQLite would resolve the unqualified names to the inner scope, but an
	// ambiguous-looking correlated subquery is not worth the risk.
	liveBlocked := `(wi.state = 'blocked' AND COALESCE(wi.blocked_reason,'') <> '` + string(state.ReasonInterrupted) + `')`

	res, err := c.db.ExecContext(ctx, `
		UPDATE internal_tasks
		SET outstanding_workflows = (
		      SELECT COUNT(*) FROM workflow_instances wi
		       WHERE wi.task_id = internal_tasks.id AND (wi.state IN `+liveStates+` OR `+liveBlocked+`)),
		    updated_at = ?
		WHERE state NOT IN ('done','failed','canceled')
		  AND COALESCE(outstanding_workflows, 0) <> (
		      SELECT COUNT(*) FROM workflow_instances wi
		       WHERE wi.task_id = internal_tasks.id AND (wi.state IN `+liveStates+` OR `+liveBlocked+`))
	`, now)
	if err != nil {
		return 0, 0, err
	}
	recounted, _ = res.RowsAffected()

	res, err = c.db.ExecContext(ctx, `
		UPDATE internal_tasks
		SET state = CASE WHEN EXISTS (
		      SELECT 1 FROM workflow_instances wi
		       WHERE wi.task_id = internal_tasks.id AND wi.state = ?
		         AND wi.task_generation = internal_tasks.generation)
		    THEN 'failed' ELSE 'done' END,
		    updated_at = ?
		WHERE state = 'running'
		  AND COALESCE(outstanding_workflows, 0) = 0
		  AND EXISTS (
		      SELECT 1 FROM workflow_instances wi
		       WHERE wi.task_id = internal_tasks.id
		         AND wi.state IN (?, ?)
		         AND wi.task_generation = internal_tasks.generation)
	`, InstanceStateFailed, now, InstanceStateDone, InstanceStateFailed)
	if err != nil {
		return recounted, 0, err
	}
	settled, _ = res.RowsAffected()
	return recounted, settled, nil
}

// StepRunHasUsage reports whether a step run carries its own token/cost rollup
// (populated since step_runs gained the usage columns). Rows written earlier
// leave these at zero, in which case callers fall back to GetStepUsage.
func StepRunHasUsage(s StepRun) bool {
	return s.TotalTokens > 0 || s.InputTokens > 0 || s.OutputTokens > 0 || s.CostUSD > 0
}

// CreateStepRun inserts a new step run row.
func (c *Client) CreateStepRun(ctx context.Context, sr *StepRun) error {
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO step_runs
		  (id, workflow_instance_id, step_id, agent_id, state, blocked_reason, skipped_reason, output, structured_output,
		   summary, exit_code, skipped_cached, publish_payload, publish_state, spawned_task_id,
		   input_prompt, input_tokens, output_tokens, total_tokens,
		   cache_creation_tokens, cache_read_tokens, num_turns, num_tool_calls, cost_usd,
		   time_thinking_ms, time_writing_ms, time_model_ms, time_tool_wait_ms,
		   time_other_ms, time_background_ms, slow_tools,
		   started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, sr.ID, sr.WorkflowInstanceID, sr.StepID, nullStr(sr.AgentID), sr.State,
		nullStr(sr.BlockedReason), nullStr(sr.SkippedReason),
		nullStr(sr.Output), nullStr(sr.StructuredOutput), nullStr(sr.Summary),
		sr.ExitCode, sr.SkippedCached, nullStr(sr.PublishPayload), nullStr(sr.PublishState),
		nullStr(sr.SpawnedTaskID), nullStr(sr.InputPrompt), sr.InputTokens, sr.OutputTokens,
		sr.TotalTokens, sr.CacheCreationTokens, sr.CacheReadTokens, sr.NumTurns, sr.NumToolCalls,
		sr.CostUSD, sr.ThinkingMS, sr.WritingMS, sr.ModelMS, sr.ToolWaitMS, sr.OtherMS,
		sr.BackgroundMS, nullStr(sr.SlowTools), sr.StartedAt, sr.FinishedAt)
	if err == nil {
		c.recordStepEvent(ctx, sr, stepStateEventType(sr.State, sr.BlockedReason))
	}
	return err
}

// UpdateStepRun persists the mutable fields of a step run (state, output,
// structured output, summary, exit code, skipped flag, finished timestamp).
func (c *Client) UpdateStepRun(ctx context.Context, sr *StepRun) error {
	var previous string
	_ = c.db.QueryRowContext(ctx, `SELECT state FROM step_runs WHERE id = ?`, sr.ID).Scan(&previous)
	_, err := c.db.ExecContext(ctx, `
		UPDATE step_runs
		SET state = ?, blocked_reason = ?, skipped_reason = ?, output = ?, structured_output = ?, summary = ?,
		    exit_code = ?, skipped_cached = ?, publish_payload = ?, publish_state = ?,
		    spawned_task_id = ?, input_prompt = ?, input_tokens = ?, output_tokens = ?,
		    total_tokens = ?, cache_creation_tokens = ?, cache_read_tokens = ?,
		    num_turns = ?, num_tool_calls = ?, cost_usd = ?,
		    time_thinking_ms = ?, time_writing_ms = ?, time_model_ms = ?,
		    time_tool_wait_ms = ?, time_other_ms = ?, time_background_ms = ?, slow_tools = ?,
		    started_at = ?, finished_at = ?
		WHERE id = ?
	`, sr.State, nullStr(sr.BlockedReason), nullStr(sr.SkippedReason),
		nullStr(sr.Output), nullStr(sr.StructuredOutput), nullStr(sr.Summary),
		sr.ExitCode, sr.SkippedCached, nullStr(sr.PublishPayload), nullStr(sr.PublishState),
		nullStr(sr.SpawnedTaskID), nullStr(sr.InputPrompt), sr.InputTokens, sr.OutputTokens,
		sr.TotalTokens, sr.CacheCreationTokens, sr.CacheReadTokens, sr.NumTurns, sr.NumToolCalls,
		sr.CostUSD, sr.ThinkingMS, sr.WritingMS, sr.ModelMS, sr.ToolWaitMS, sr.OtherMS,
		sr.BackgroundMS, nullStr(sr.SlowTools), sr.StartedAt, sr.FinishedAt, sr.ID)
	if err == nil && previous != sr.State {
		c.recordStepEvent(ctx, sr, stepStateEventType(sr.State, sr.BlockedReason))
	}
	return err
}

// workflowStateEventType maps a state transition to its lifecycle event.
//
// It takes the reason as well as the state because the canonical vocabulary
// collapses approval waits, CI waits and interruption onto "blocked" (#465).
// Only interruption is a cancellation; emitting workflow.cancelled when a run
// merely parks on an approval gate would be wrong.
func workflowStateEventType(st, reason string) string {
	switch st {
	case InstanceStateDone:
		return "workflow.completed"
	case InstanceStateFailed:
		return "workflow.failed"
	case InstanceStateCanceled:
		return "workflow.cancelled"
	case InstanceStateBlocked:
		if reason == string(state.ReasonInterrupted) {
			return "workflow.cancelled"
		}
	}
	return ""
}

// stepStateEventType is the step-level companion to workflowStateEventType, and
// takes the reason for the same purpose: a step parked on an approval is blocked
// but not cancelled.
func stepStateEventType(st, reason string) string {
	switch st {
	case StepStateRunning:
		return "step.started"
	case StepStatePassed, StepStateSkipped:
		return "step.completed"
	case StepStateFailed:
		return "step.failed"
	case StepStateBlocked:
		if reason == string(state.ReasonInterrupted) {
			return "step.cancelled"
		}
	}
	return ""
}

func (c *Client) recordWorkflowEvent(ctx context.Context, inst *WorkflowInstance, eventType string, metadata map[string]any) {
	if eventType == "" || inst == nil {
		return
	}
	_ = c.RecordExecutionEvent(ctx, &ExecutionEvent{Type: eventType, TaskID: inst.TaskID, WorkflowID: inst.WorkflowID, WorkflowInstanceID: inst.ID, Metadata: metadata})
}

func (c *Client) recordStepEvent(ctx context.Context, sr *StepRun, eventType string) {
	if eventType == "" || sr == nil {
		return
	}
	inst, _ := c.GetWorkflowInstance(ctx, sr.WorkflowInstanceID)
	event := &ExecutionEvent{Type: eventType, WorkflowInstanceID: sr.WorkflowInstanceID, StepID: sr.StepID,
		Metadata: map[string]any{"state": sr.State, "agent_id": sr.AgentID, "exit_code": sr.ExitCode, "cached": sr.SkippedCached,
			"total_tokens": sr.TotalTokens, "cost_usd": sr.CostUSD}}
	if inst != nil {
		event.TaskID, event.WorkflowID = inst.TaskID, inst.WorkflowID
	}
	_ = c.RecordExecutionEvent(ctx, event)
}

// UpdateStepRunState rewrites only the state (and output) of an existing step
// run. The workflow scheduler calls it when a post-execution gate — fail_when
// (reject_when) or on_missing_output — fails a step whose runner exited 0, so
// the persisted row matches the decision the workflow actually took (#390). A
// state change emits the usual step.* execution event.
func (c *Client) UpdateStepRunState(ctx context.Context, id, newState, reason, output string) error {
	var previous, stepID, instanceID string
	if err := c.db.QueryRowContext(ctx,
		`SELECT state, step_id, workflow_instance_id FROM step_runs WHERE id = ?`, id).
		Scan(&previous, &stepID, &instanceID); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx,
		`UPDATE step_runs SET state = ?, blocked_reason = ?, output = ? WHERE id = ?`,
		newState, nullStr(reason), nullStr(output), id); err != nil {
		return err
	}
	if previous != newState {
		c.recordStepEvent(ctx, &StepRun{ID: id, WorkflowInstanceID: instanceID, StepID: stepID, State: newState, BlockedReason: reason},
			stepStateEventType(newState, reason))
	}
	return nil
}

// ListStepRuns returns all step runs for an instance, in insertion order.
func (c *Client) ListStepRuns(ctx context.Context, instanceID string) ([]StepRun, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, workflow_instance_id, step_id, COALESCE(agent_id,''), state,
		       COALESCE(blocked_reason,''), COALESCE(skipped_reason,''),
		       COALESCE(output,''), COALESCE(structured_output,''), COALESCE(summary,''),
		       COALESCE(exit_code,0), COALESCE(skipped_cached,0),
		       COALESCE(publish_payload,''), COALESCE(publish_state,''),
		       COALESCE(spawned_task_id,''), COALESCE(input_prompt,''),
		       COALESCE(input_tokens,0), COALESCE(output_tokens,0), COALESCE(total_tokens,0),
		       COALESCE(cache_creation_tokens,0), COALESCE(cache_read_tokens,0),
		       COALESCE(num_turns,0), COALESCE(num_tool_calls,0), COALESCE(cost_usd,0.0),
		       COALESCE(time_thinking_ms,0), COALESCE(time_writing_ms,0), COALESCE(time_model_ms,0),
		       COALESCE(time_tool_wait_ms,0), COALESCE(time_other_ms,0),
		       COALESCE(time_background_ms,0), COALESCE(slow_tools,''),
		       started_at, finished_at
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
			&sr.BlockedReason, &sr.SkippedReason,
			&sr.Output, &sr.StructuredOutput, &sr.Summary, &sr.ExitCode, &sr.SkippedCached,
			&sr.PublishPayload, &sr.PublishState, &sr.SpawnedTaskID, &sr.InputPrompt,
			&sr.InputTokens, &sr.OutputTokens, &sr.TotalTokens,
			&sr.CacheCreationTokens, &sr.CacheReadTokens, &sr.NumTurns, &sr.NumToolCalls,
			&sr.CostUSD, &sr.ThinkingMS, &sr.WritingMS, &sr.ModelMS, &sr.ToolWaitMS,
			&sr.OtherMS, &sr.BackgroundMS, &sr.SlowTools,
			&sr.StartedAt, &sr.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, sr)
	}
	return out, rows.Err()
}

// CIPollCheck is one poll of a wait_for step's external status (e.g. a CI status
// check on a PR). One row is written per poll, giving an auditable history of
// how many times the step polled, when, and what each poll returned.
type CIPollCheck struct {
	ID                 int64
	WorkflowInstanceID string
	StepID             string
	Status             string // passed|failed|pending|timeout|error|unknown
	PRURL              string
	Detail             string // JSON of per-check states, or an error message
	CheckedAt          time.Time
}

// RecordCIPollCheck appends one CI poll result for a wait_for step. checked_at is
// set by the database default (CURRENT_TIMESTAMP).
func (c *Client) RecordCIPollCheck(ctx context.Context, p *CIPollCheck) error {
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO ci_poll_checks
		  (workflow_instance_id, step_id, status, pr_url, detail)
		VALUES (?, ?, ?, ?, ?)
	`, p.WorkflowInstanceID, p.StepID, p.Status, nullStr(p.PRURL), nullStr(p.Detail))
	return err
}

// ListCIPollChecks returns all CI poll checks for an instance, oldest first.
func (c *Client) ListCIPollChecks(ctx context.Context, instanceID string) ([]CIPollCheck, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, workflow_instance_id, step_id, status,
		       COALESCE(pr_url,''), COALESCE(detail,''), checked_at
		FROM ci_poll_checks WHERE workflow_instance_id = ?
		ORDER BY checked_at ASC, id ASC
	`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CIPollCheck
	for rows.Next() {
		var p CIPollCheck
		if err := rows.Scan(&p.ID, &p.WorkflowInstanceID, &p.StepID, &p.Status,
			&p.PRURL, &p.Detail, &p.CheckedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// scanner abstracts *sql.Row and *sql.Rows for shared scanning.
type scanner interface {
	Scan(dest ...any) error
}

func scanInstance(s scanner) (*WorkflowInstance, error) {
	var inst WorkflowInstance
	err := s.Scan(&inst.ID, &inst.WorkflowID, &inst.CellID, &inst.SourceID, &inst.State, &inst.BlockedReason,
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
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

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
	stmt = "DELETE FROM workflow_instance_snapshots WHERE instance_id IN (" + placeholders + ")"
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
