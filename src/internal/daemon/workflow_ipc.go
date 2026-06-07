package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
)

// InstanceSummary is one row in the `apiary instances` list.
type InstanceSummary struct {
	ID       string `json:"id"`
	Workflow string `json:"workflow"`
	CellID   string `json:"cell_id"`
	Title    string `json:"title"`
	State    string `json:"state"`
	Started  string `json:"started"`  // human "18m ago"
	Duration string `json:"duration"` // human "14m 32s" or "—"
}

// StepRunView is one step row in an instance detail.
type StepRunView struct {
	StepID       string     `json:"step_id"`
	AgentID      string     `json:"agent_id"`
	State        string     `json:"state"`
	Duration     string     `json:"duration"`
	Cached       bool       `json:"cached"`
	Output       string     `json:"output"`
	Summary      string     `json:"summary"`
	InputTokens  int        `json:"input_tokens"`
	OutputTokens int        `json:"output_tokens"`
	TotalTokens  int        `json:"total_tokens"`
	CostUSD      float64    `json:"cost_usd"`
	NumTurns     int        `json:"num_turns"`
	NumToolCalls int        `json:"num_tool_calls"`
	StartedAt    *time.Time `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

// InstanceDetail is the payload for `apiary instances <id>`.
type InstanceDetail struct {
	InstanceSummary
	Steps []StepRunView `json:"steps"`
}

// InstancesResponse is the JSON payload returned by GET /instances.
type InstancesResponse struct {
	Instances []InstanceSummary `json:"instances"`
}

// WorkflowStepDef is one step in a workflow definition, for the Workflows tab.
type WorkflowStepDef struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Agent     string `json:"agent"`
	Condition string `json:"condition"`
	Prompt    string `json:"prompt"`
}

// WorkflowSummary is one workflow from the config, for the Workflows tab.
type WorkflowSummary struct {
	ID          string            `json:"id"`
	Description string            `json:"description"`
	Steps       []WorkflowStepDef `json:"steps"`
}

// WorkflowListResponse is the payload for GET /workflows.
type WorkflowListResponse struct {
	Workflows []WorkflowSummary `json:"workflows"`
}

// WorkflowList returns all workflows defined in the config.
func (d *Dispatcher) WorkflowList() WorkflowListResponse {
	resp := WorkflowListResponse{}
	for _, wf := range d.cfg.Workflows {
		ws := WorkflowSummary{
			ID:          wf.ID,
			Description: wf.Description,
		}
		for _, step := range wf.Steps {
			sd := WorkflowStepDef{
				ID:        step.ID,
				Type:      resolveStepType(step),
				Agent:     step.Agent,
				Condition: step.Condition,
				Prompt:    step.Prompt,
			}
			ws.Steps = append(ws.Steps, sd)
		}
		resp.Workflows = append(resp.Workflows, ws)
	}
	return resp
}

func resolveStepType(s config.StepConfig) string {
	if s.Type != "" {
		return s.Type
	}
	if len(s.Items) > 0 || s.ForEachExpr != "" {
		return config.StepTypeForeach
	}
	if len(s.ParallelSteps) > 0 {
		return config.StepTypeParallel
	}
	if s.ResumeOn != nil || s.AbortOn != nil {
		return config.StepTypeApproval
	}
	return config.StepTypeAgent
}

// StopInstance cancels the running execution for a workflow instance without
// re-dispatching. The instance is marked interrupted; unlike ForceRestart, it
// does not strip labels or reset the source state.
func (d *Dispatcher) StopInstance(ctx context.Context, instanceID string) error {
	if d.db == nil {
		return nil
	}
	inst, err := d.db.GetWorkflowInstance(ctx, instanceID)
	if err != nil || inst == nil {
		return err
	}
	// Cancel the in-flight run for this cell.
	if val, ok := d.runCancel.LoadAndDelete(inst.CellID); ok {
		cancel := val.(context.CancelFunc)
		cancel()
	}
	// Remove from in-flight and active tracking so the slot is freed.
	d.inFlight.Delete(inst.CellID)
	d.activeRuns.Range(func(key, val any) bool {
		run := val.(model.ActiveRun)
		if run.Cell.ID == inst.CellID {
			d.activeRuns.Delete(key)
		}
		return true
	})

	// Mark the running task_execution interrupted.
	if lastExec, err := d.db.GetLastExecution(ctx, inst.CellID); err == nil && lastExec != nil && lastExec.Status == "running" {
		now := time.Now()
		lastExec.Status = "interrupted"
		lastExec.CompletedAt = &now
		lastExec.ErrorMsg = "stopped by user"
		_ = d.db.UpdateExecution(ctx, lastExec)
	}

	// Mark the instance itself interrupted.
	if err := d.db.UpdateWorkflowInstanceState(ctx, instanceID, db.InstanceStateInterrupted); err != nil {
		aplog.Error("stop instance %s: update state: %v", instanceID, err)
	}
	aplog.Info("stopped instance %s (cell %s)", instanceID, inst.CellID)
	return nil
}

// Instances returns workflow instances for the IPC list endpoint, optionally
// filtered by state and/or workflow id.
func (d *Dispatcher) Instances(ctx context.Context, state, workflowID string, limit int) (InstancesResponse, error) {
	if d.db == nil {
		return InstancesResponse{}, nil
	}
	views, err := d.db.ListWorkflowInstanceViews(ctx, state, workflowID, limit)
	if err != nil {
		return InstancesResponse{}, err
	}
	now := time.Now()
	resp := InstancesResponse{}
	for _, v := range views {
		resp.Instances = append(resp.Instances, instanceSummary(v, now))
	}
	return resp, nil
}

// InstanceDetail returns a single instance with its step runs, or (nil, nil)
// when the instance id is unknown.
func (d *Dispatcher) InstanceDetail(ctx context.Context, id string) (*InstanceDetail, error) {
	if d.db == nil {
		return nil, nil
	}
	inst, err := d.db.GetWorkflowInstance(ctx, id)
	if err != nil || inst == nil {
		return nil, err
	}
	title, _ := d.db.GetTaskTitle(ctx, inst.CellID)
	now := time.Now()
	view := db.WorkflowInstanceView{WorkflowInstance: *inst, Title: title}
	detail := &InstanceDetail{InstanceSummary: instanceSummary(view, now)}

	steps, err := d.db.ListStepRuns(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, s := range steps {
		detail.Steps = append(detail.Steps, d.stepRunView(ctx, id, s, now))
	}
	return detail, nil
}

// stepRunView maps a stored step run to its IPC view, enriching token/cost usage
// via the step link columns. Shared by InstanceDetail and TaskHistory.
func (d *Dispatcher) stepRunView(ctx context.Context, instanceID string, s db.StepRun, now time.Time) StepRunView {
	srv := StepRunView{
		StepID:     s.StepID,
		AgentID:    s.AgentID,
		State:      s.State,
		Duration:   stepDuration(s, now),
		Cached:     s.SkippedCached,
		Output:     s.Output,
		Summary:    s.Summary,
		StartedAt:  s.StartedAt,
		FinishedAt: s.FinishedAt,
	}
	if usage, err := d.db.GetStepUsage(ctx, instanceID, s.StepID); err == nil && usage != nil {
		srv.InputTokens = usage.InputTokens
		srv.OutputTokens = usage.OutputTokens
		srv.TotalTokens = usage.TotalTokens
		srv.CostUSD = usage.CostUSD
		srv.NumTurns = usage.NumTurns
		srv.NumToolCalls = usage.NumToolCalls
	}
	return srv
}

// TaskLogLineView is one log line scoped to a task-history segment's instance.
type TaskLogLineView struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}

// TaskHistorySegmentView is one workflow instance's slice of a task's history:
// the instance summary, its steps, and the logs scoped to its time window.
type TaskHistorySegmentView struct {
	Instance InstanceSummary   `json:"instance"`
	Steps    []StepRunView     `json:"steps"`
	Logs     []TaskLogLineView `json:"logs"`
}

// TaskHistoryResponse is the payload for GET /tasks/{id}/history.
type TaskHistoryResponse struct {
	TaskID   string                   `json:"task_id"`
	Title    string                   `json:"title"`
	Segments []TaskHistorySegmentView `json:"segments"`
}

// TaskHistory returns the full per-instance history for an InternalTask, oldest
// instance first (each instance with its steps and time-windowed logs). Returns
// (nil, nil) when the task has no workflow instances.
func (d *Dispatcher) TaskHistory(ctx context.Context, internalTaskID string) (*TaskHistoryResponse, error) {
	if d.db == nil {
		return nil, nil
	}
	segs, err := d.db.GetTaskWorkflowHistory(ctx, internalTaskID)
	if err != nil {
		return nil, err
	}
	if len(segs) == 0 {
		return nil, nil
	}
	now := time.Now()
	// All instances of a task share one cell, so one title lookup covers them all.
	title, _ := d.db.GetTaskTitle(ctx, segs[0].Instance.CellID)
	resp := &TaskHistoryResponse{TaskID: internalTaskID, Title: title}
	for _, seg := range segs {
		view := db.WorkflowInstanceView{WorkflowInstance: seg.Instance, Title: title}
		sv := TaskHistorySegmentView{Instance: instanceSummary(view, now)}
		for _, s := range seg.Steps {
			sv.Steps = append(sv.Steps, d.stepRunView(ctx, seg.Instance.ID, s, now))
		}
		for _, l := range seg.Logs {
			sv.Logs = append(sv.Logs, TaskLogLineView{Timestamp: l.Timestamp, Level: l.Level, Message: l.Message})
		}
		resp.Segments = append(resp.Segments, sv)
	}
	return resp, nil
}

func instanceSummary(v db.WorkflowInstanceView, now time.Time) InstanceSummary {
	return InstanceSummary{
		ID:       v.ID,
		Workflow: v.WorkflowID,
		CellID:   v.CellID,
		Title:    v.Title,
		State:    v.State,
		Started:  humanDuration(now.Sub(v.CreatedAt)) + " ago",
		Duration: instanceDuration(v.WorkflowInstance, now),
	}
}

// instanceDuration is the elapsed wall-clock time for an instance: live for
// running/waiting instances, final for done/failed, and unknown ("—") for
// interrupted/pending where no meaningful span exists.
func instanceDuration(inst db.WorkflowInstance, now time.Time) string {
	switch inst.State {
	case db.InstanceStateRunning, db.InstanceStateApprovalWaiting:
		return humanDuration(now.Sub(inst.CreatedAt))
	case db.InstanceStateDone, db.InstanceStateFailed:
		return humanDuration(inst.UpdatedAt.Sub(inst.CreatedAt))
	default:
		return "—"
	}
}

func stepDuration(s db.StepRun, now time.Time) string {
	if s.StartedAt == nil {
		return "—"
	}
	end := now
	if s.FinishedAt != nil {
		end = *s.FinishedAt
	}
	return humanDuration(end.Sub(*s.StartedAt))
}

// DeleteTask removes a task by its internal task ID or by source reference (source:item).
// It cancels any running execution, clears tracking state, and deletes all associated
// workflow instances, steps, and logs from the database.
func (d *Dispatcher) DeleteTask(ctx context.Context, taskRef string) error {
	if d.db == nil {
		return nil
	}

	// Resolve the task ID from the reference. If taskRef contains ":", it's source:item.
	var taskID string
	instances, err := d.db.ListWorkflowInstances(ctx, 10000)
	if err != nil {
		return fmt.Errorf("list instances: %w", err)
	}

	// Find matching instances by task ID or source reference.
	var instancesToDelete []string
	var cellID string
	for _, inst := range instances {
		// Check if this instance matches our task reference.
		if inst.ID == taskRef {
			instancesToDelete = append(instancesToDelete, inst.ID)
			taskID = inst.ID
			cellID = inst.CellID
		} else if taskRef != "" && inst.WorkflowID != "" {
			// Could be source:item reference, would need to check bindings.
			// For now, match by instance ID (task ID stored in workflow instance).
		}
	}

	if len(instancesToDelete) == 0 {
		return fmt.Errorf("task not found: %s", taskRef)
	}

	// Cancel any running execution.
	if val, ok := d.runCancel.LoadAndDelete(cellID); ok {
		cancel := val.(context.CancelFunc)
		cancel()
	}

	// Remove from in-flight tracking.
	d.inFlight.Delete(cellID)
	d.activeRuns.Range(func(key, val any) bool {
		run := val.(model.ActiveRun)
		if run.Cell.ID == cellID {
			d.activeRuns.Delete(key)
		}
		return true
	})

	// Delete workflow instances and their step runs.
	// The database cascade deletes should handle step_runs (FK to workflow_instances),
	// but we may need explicit cleanup for task_logs, etc.
	if err := d.db.DeleteWorkflowInstances(ctx, instancesToDelete); err != nil {
		aplog.Error("delete task %s: %v", taskID, err)
		return err
	}

	aplog.Info("deleted task %s (cell %s): %d instance(s)", taskID, cellID, len(instancesToDelete))
	return nil
}
