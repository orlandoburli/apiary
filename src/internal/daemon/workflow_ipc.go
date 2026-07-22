package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

// ErrTaskNotFound is returned by DeleteTask when no task, binding, or workflow
// instance resolves the given reference. The IPC handler maps it to HTTP 404 so
// the CLI can distinguish "nothing to delete" from a real server-side failure.
var ErrTaskNotFound = errors.New("task not found")

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
	StepID              string     `json:"step_id"`
	AgentID             string     `json:"agent_id"`
	State               string     `json:"state"`
	Duration            string     `json:"duration"`
	Cached              bool       `json:"cached"`
	Output              string     `json:"output"`
	Summary             string     `json:"summary"`
	InputPrompt         string     `json:"input_prompt,omitempty"`
	InputTokens         int        `json:"input_tokens"`
	OutputTokens        int        `json:"output_tokens"`
	TotalTokens         int        `json:"total_tokens"`
	CacheCreationTokens int        `json:"cache_creation_tokens"`
	CacheReadTokens     int        `json:"cache_read_tokens"`
	CostUSD             float64    `json:"cost_usd"`
	NumTurns            int        `json:"num_turns"`
	NumToolCalls        int        `json:"num_tool_calls"`
	StartedAt           *time.Time `json:"started_at"`
	FinishedAt          *time.Time `json:"finished_at"`
}

// CIPollView is one recorded wait_for CI poll, surfaced in instance and
// task-history detail so a parked CI wait reports how many times it polled,
// when, and what each poll returned.
type CIPollView struct {
	StepID    string    `json:"step_id"`
	Status    string    `json:"status"`
	PRURL     string    `json:"pr_url"`
	Detail    string    `json:"detail"`
	CheckedAt time.Time `json:"checked_at"`
}

// InstanceDetail is the payload for `apiary instances <id>`.
type InstanceDetail struct {
	InstanceSummary
	ResumedFrom string        `json:"resumed_from,omitempty"`
	Steps       []StepRunView `json:"steps"`
	CIPolls     []CIPollView  `json:"ci_polls"`
}

// ciPollViews maps stored CI poll rows to their IPC views.
func ciPollViews(checks []db.CIPollCheck) []CIPollView {
	out := make([]CIPollView, 0, len(checks))
	for _, c := range checks {
		out = append(out, CIPollView{
			StepID:    c.StepID,
			Status:    c.Status,
			PRURL:     c.PRURL,
			Detail:    c.Detail,
			CheckedAt: c.CheckedAt,
		})
	}
	return out
}

// InstancesResponse is the JSON payload returned by GET /instances.
type InstancesResponse struct {
	Instances []InstanceSummary `json:"instances"`
}

// PullRequestView is one pull request linked to a task, returned by the PR-refresh
// endpoint.
type PullRequestView struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"`
}

// TaskPullRequestsResponse is the JSON payload returned by
// POST /tasks/pulls/refresh/{ref}. Pulls is ordered oldest first; the last entry
// is the most recent PR.
type TaskPullRequestsResponse struct {
	TaskID string            `json:"task_id"`
	Pulls  []PullRequestView `json:"pulls"`
}

// RefreshTaskPullRequests resolves a task's source bindings, asks each source that
// can enumerate PRs for the ones linked to the bound item(s), persists the result,
// and returns the merged set. It is the daemon-side half of the dashboard's "open
// PR" shortcut: the dashboard reads its own DB for display, while only the daemon
// holds the source adapters/credentials to discover PRs.
//
// A source that errors or cannot list PRs is skipped WITHOUT touching its persisted
// rows, so a transient GitHub/auth failure never wipes the last-good PR list.
func (d *Dispatcher) RefreshTaskPullRequests(ctx context.Context, taskRef string) (*TaskPullRequestsResponse, error) {
	if d.db == nil {
		return &TaskPullRequestsResponse{}, nil
	}
	taskID, _, err := d.resolveTaskRef(ctx, taskRef)
	if err != nil {
		return nil, err
	}
	bindings, err := d.db.ListBindingsByTask(ctx, taskID)
	if err != nil {
		return nil, err
	}

	// Group bound item ids by source so a task with several bindings on the same
	// source is replaced in one shot (a per-binding replace would clobber the
	// earlier binding's rows, since the delete is scoped to source_id).
	itemsBySource := make(map[string][]string)
	order := make([]string, 0)
	for _, b := range bindings {
		if _, seen := itemsBySource[b.SourceID]; !seen {
			order = append(order, b.SourceID)
		}
		itemsBySource[b.SourceID] = append(itemsBySource[b.SourceID], b.SourceItemID)
	}

	for _, sourceID := range order {
		adapter, ok := d.sources[sourceID]
		if !ok {
			continue
		}
		lister, ok := adapter.(source.PullRequestLister)
		if !ok {
			continue // source can't enumerate PRs (e.g. Plane) — leave its rows alone
		}

		var refs []source.PullRequestRef
		failed := false
		seen := make(map[int]bool)
		for _, itemID := range itemsBySource[sourceID] {
			prs, err := lister.ListPullRequests(ctx, itemID)
			if err != nil {
				// Transient/auth error: keep last-good rows for this source.
				aplog.Debug("refresh PRs for task %s (%s/%s): %v", taskID, sourceID, itemID, err)
				failed = true
				break
			}
			for _, p := range prs {
				if seen[p.Number] {
					continue
				}
				seen[p.Number] = true
				refs = append(refs, p)
			}
		}
		if failed {
			continue
		}

		rows := make([]db.TaskPullRequest, 0, len(refs))
		for _, p := range refs {
			rows = append(rows, db.TaskPullRequest{
				SourceID: sourceID,
				PRNumber: p.Number,
				PRURL:    p.URL,
				PRState:  p.State,
			})
		}
		if err := d.db.ReplaceTaskPullRequests(ctx, taskID, sourceID, rows); err != nil {
			return nil, err
		}
	}

	stored, err := d.db.ListTaskPullRequests(ctx, taskID)
	if err != nil {
		return nil, err
	}
	resp := &TaskPullRequestsResponse{TaskID: taskID}
	for _, p := range stored {
		resp.Pulls = append(resp.Pulls, PullRequestView{Number: p.PRNumber, URL: p.PRURL, State: p.PRState})
	}
	return resp, nil
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
	detail := &InstanceDetail{InstanceSummary: instanceSummary(view, now), ResumedFrom: inst.ResumedFrom}

	steps, err := d.db.ListStepRuns(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, s := range steps {
		detail.Steps = append(detail.Steps, d.stepRunView(ctx, id, s, now))
	}
	if polls, err := d.db.ListCIPollChecks(ctx, id); err == nil {
		detail.CIPolls = ciPollViews(polls)
	}
	return detail, nil
}

// stepRunView maps a stored step run to its IPC view, enriching token/cost usage
// via the step link columns. Shared by InstanceDetail and TaskHistory.
func (d *Dispatcher) stepRunView(ctx context.Context, instanceID string, s db.StepRun, now time.Time) StepRunView {
	srv := StepRunView{
		StepID:      s.StepID,
		AgentID:     s.AgentID,
		State:       s.State,
		Duration:    stepDuration(s, now),
		Cached:      s.SkippedCached,
		Output:      s.Output,
		Summary:     s.Summary,
		InputPrompt: s.InputPrompt,
		StartedAt:   s.StartedAt,
		FinishedAt:  s.FinishedAt,
	}
	// Prefer the step's own usage rollup (summed across failover attempts). Fall
	// back to the latest task_execution for rows written before step_runs carried
	// these columns.
	if db.StepRunHasUsage(s) {
		srv.InputTokens = s.InputTokens
		srv.OutputTokens = s.OutputTokens
		srv.TotalTokens = s.TotalTokens
		srv.CacheCreationTokens = s.CacheCreationTokens
		srv.CacheReadTokens = s.CacheReadTokens
		srv.CostUSD = s.CostUSD
		srv.NumTurns = s.NumTurns
		srv.NumToolCalls = s.NumToolCalls
	} else if usage, err := d.db.GetStepUsage(ctx, instanceID, s.StepID); err == nil && usage != nil {
		srv.InputTokens = usage.InputTokens
		srv.OutputTokens = usage.OutputTokens
		srv.TotalTokens = usage.TotalTokens
		srv.CacheCreationTokens = usage.CacheCreationTokens
		srv.CacheReadTokens = usage.CacheReadTokens
		srv.CostUSD = usage.CostUSD
		srv.NumTurns = usage.NumTurns
		srv.NumToolCalls = usage.NumToolCalls
	}
	return srv
}

type StepComparison struct {
	StepID          string       `json:"step_id"`
	Before          *StepRunView `json:"before,omitempty"`
	After           *StepRunView `json:"after,omitempty"`
	InputChanged    bool         `json:"input_changed"`
	OutputChanged   bool         `json:"output_changed"`
	TokenDelta      int          `json:"token_delta"`
	CostDeltaUSD    float64      `json:"cost_delta_usd"`
	DurationDeltaMS int64        `json:"duration_delta_ms"`
	BeforeModel     string       `json:"before_model,omitempty"`
	AfterModel      string       `json:"after_model,omitempty"`
	BeforeRunner    string       `json:"before_runner,omitempty"`
	AfterRunner     string       `json:"after_runner,omitempty"`
}

type InstanceComparison struct {
	BeforeID string           `json:"before_id"`
	AfterID  string           `json:"after_id"`
	Steps    []StepComparison `json:"steps"`
}

func (d *Dispatcher) CompareInstances(ctx context.Context, beforeID, afterID string) (*InstanceComparison, error) {
	before, err := d.InstanceDetail(ctx, beforeID)
	if err != nil || before == nil {
		return nil, err
	}
	after, err := d.InstanceDetail(ctx, afterID)
	if err != nil || after == nil {
		return nil, err
	}
	bm, am := map[string]StepRunView{}, map[string]StepRunView{}
	order, seen := []string{}, map[string]bool{}
	for _, s := range before.Steps {
		bm[s.StepID] = s
		if !seen[s.StepID] {
			order = append(order, s.StepID)
			seen[s.StepID] = true
		}
	}
	for _, s := range after.Steps {
		am[s.StepID] = s
		if !seen[s.StepID] {
			order = append(order, s.StepID)
			seen[s.StepID] = true
		}
	}
	out := &InstanceComparison{BeforeID: beforeID, AfterID: afterID}
	for _, id := range order {
		row := StepComparison{StepID: id}
		if s, ok := bm[id]; ok {
			copy := s
			row.Before = &copy
		}
		if s, ok := am[id]; ok {
			copy := s
			row.After = &copy
		}
		if row.Before != nil {
			if usage, err := d.db.GetStepUsage(ctx, beforeID, id); err == nil && usage != nil {
				row.BeforeModel, row.BeforeRunner = usage.Model, usage.Runner
				if row.Before.InputPrompt == "" {
					row.Before.InputPrompt = usage.InputPrompt
				}
			}
		}
		if row.After != nil {
			if usage, err := d.db.GetStepUsage(ctx, afterID, id); err == nil && usage != nil {
				row.AfterModel, row.AfterRunner = usage.Model, usage.Runner
				if row.After.InputPrompt == "" {
					row.After.InputPrompt = usage.InputPrompt
				}
			}
		}
		if row.After != nil && row.After.Cached && row.AfterModel == "" {
			row.AfterModel, row.AfterRunner = row.BeforeModel, row.BeforeRunner
		}
		if row.Before != nil && row.After != nil {
			row.InputChanged = row.Before.InputPrompt != row.After.InputPrompt
			row.OutputChanged = row.Before.Output != row.After.Output
			row.TokenDelta = row.After.TotalTokens - row.Before.TotalTokens
			row.CostDeltaUSD = row.After.CostUSD - row.Before.CostUSD
			row.DurationDeltaMS = stepViewDurationMS(*row.After) - stepViewDurationMS(*row.Before)
		} else {
			row.InputChanged, row.OutputChanged = true, true
		}
		out.Steps = append(out.Steps, row)
	}
	return out, nil
}

func stepViewDurationMS(step StepRunView) int64 {
	if step.StartedAt == nil || step.FinishedAt == nil {
		return 0
	}
	return step.FinishedAt.Sub(*step.StartedAt).Milliseconds()
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
	CIPolls  []CIPollView      `json:"ci_polls"`
	Logs     []TaskLogLineView `json:"logs"`
}

// TaskHistoryResponse is the payload for GET /tasks/{id}/history.
type TaskHistoryResponse struct {
	TaskID   string                   `json:"task_id"`
	Title    string                   `json:"title"`
	Events   []db.ExecutionEvent      `json:"events"`
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
	resp.Events, _ = d.db.ListExecutionEvents(ctx, db.ExecutionEventFilter{TaskID: internalTaskID, Limit: 1000})
	for _, seg := range segs {
		view := db.WorkflowInstanceView{WorkflowInstance: seg.Instance, Title: title}
		sv := TaskHistorySegmentView{Instance: instanceSummary(view, now)}
		for _, s := range seg.Steps {
			sv.Steps = append(sv.Steps, d.stepRunView(ctx, seg.Instance.ID, s, now))
		}
		if polls, err := d.db.ListCIPollChecks(ctx, seg.Instance.ID); err == nil {
			sv.CIPolls = ciPollViews(polls)
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
	case db.InstanceStateRunning, db.InstanceStateApprovalWaiting, db.InstanceStateWaiting:
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

// DeleteTask permanently removes a task and everything attached to it. The
// reference may be (1) a source:item pair (e.g. "github:1956"), (2) the canonical
// InternalTask id, or (3) a bare source-item id / dashboard DrillKey (e.g. a
// GitHub issue number "1956", which is the workflow_instances.cell_id). It cancels
// any in-flight run, clears tracking state, and deletes the workflow instances,
// step runs, task logs, source bindings, and the internal_tasks row — so the same
// source item rebinds to a fresh task on the next dispatch cycle. Returns
// ErrTaskNotFound (wrapped) when the reference resolves to nothing.
func (d *Dispatcher) DeleteTask(ctx context.Context, taskRef string) error {
	if d.db == nil {
		return nil
	}

	taskID, cellID, err := d.resolveTaskRef(ctx, taskRef)
	if err != nil {
		return err
	}

	// Cancel any in-flight run and free its slot, keyed by cell id.
	if val, ok := d.runCancel.LoadAndDelete(cellID); ok {
		cancel := val.(context.CancelFunc)
		cancel()
	}
	d.inFlight.Delete(cellID)
	d.activeRuns.Range(func(key, val any) bool {
		run := val.(model.ActiveRun)
		if run.Cell.ID == cellID {
			d.activeRuns.Delete(key)
		}
		return true
	})

	// Workflow instances (and their step runs) attached to the task or its cell.
	instanceIDs, err := d.instanceIDsForDelete(ctx, taskID, cellID)
	if err != nil {
		aplog.Error("delete task %s: list instances: %v", taskRef, err)
		return err
	}
	if len(instanceIDs) > 0 {
		if err := d.db.DeleteWorkflowInstances(ctx, instanceIDs); err != nil {
			aplog.Error("delete task %s: instances: %v", taskRef, err)
			return err
		}
	}

	// Task logs and executions are keyed by cell id.
	if err := d.db.ClearTaskLogs(ctx, cellID); err != nil {
		aplog.Error("delete task %s: logs: %v", taskRef, err)
		return err
	}

	// Bindings and the canonical task row. Skipped for orphaned cells (taskID == "").
	if taskID != "" {
		if err := d.db.SourceBindings().DeleteBindingsByTask(ctx, taskID); err != nil {
			aplog.Error("delete task %s: bindings: %v", taskRef, err)
			return err
		}
		if err := d.db.InternalTasks().DeleteTask(ctx, taskID); err != nil {
			aplog.Error("delete task %s: task row: %v", taskRef, err)
			return err
		}
	}

	aplog.Info("deleted task %s (task=%q cell=%q): %d instance(s)", taskRef, taskID, cellID, len(instanceIDs))
	return nil
}

// resolveTaskRef maps a user-supplied task reference to its canonical task id and
// cell id. See DeleteTask for the accepted reference forms. taskID is empty only
// for an orphaned cell (workflow instances whose task row is already gone); cellID
// is always set on success. Returns a wrapped ErrTaskNotFound when nothing matches.
func (d *Dispatcher) resolveTaskRef(ctx context.Context, taskRef string) (taskID, cellID string, err error) {
	bindings := d.db.SourceBindings()
	tasks := d.db.InternalTasks()

	// 1. Explicit source:item reference.
	if src, item, ok := strings.Cut(taskRef, ":"); ok && src != "" && item != "" {
		b, err := bindings.GetBindingBySourceItem(ctx, src, item)
		if err != nil {
			return "", "", err
		}
		if b != nil {
			return b.TaskID, b.SourceItemID, nil
		}
		return "", "", fmt.Errorf("%w: %s", ErrTaskNotFound, taskRef)
	}

	// 2. Canonical InternalTask id.
	if t, err := tasks.GetTask(ctx, taskRef); err != nil {
		return "", "", err
	} else if t != nil {
		return t.ID, d.cellIDForTask(ctx, t.ID), nil
	}

	// 3. Bare source-item id / DrillKey (e.g. a GitHub issue number).
	if b, err := bindings.GetBindingBySourceItemID(ctx, taskRef); err != nil {
		return "", "", err
	} else if b != nil {
		return b.TaskID, b.SourceItemID, nil
	}

	// 4. Orphaned cell: workflow instances keyed by this cell id with no task row.
	if inst, err := d.db.GetLatestInstanceByCell(ctx, taskRef); err != nil {
		return "", "", err
	} else if inst != nil {
		return inst.TaskID, taskRef, nil
	}

	return "", "", fmt.Errorf("%w: %s", ErrTaskNotFound, taskRef)
}

// cellIDForTask returns the cell id a task's legacy machinery (instances, logs,
// executions) is keyed by: the primary binding's source-item id when bound,
// otherwise the task's own id (the engine uses it as the cell id for spawned,
// binding-less tasks). Mirrors the dashboard's drillKeyFor.
func (d *Dispatcher) cellIDForTask(ctx context.Context, taskID string) string {
	if bs, err := d.db.ListBindingsByTask(ctx, taskID); err == nil && len(bs) > 0 {
		return bs[0].SourceItemID
	}
	return taskID
}

// instanceIDsForDelete collects the workflow-instance ids to delete for a task,
// matching both by task id and by cell id (the latter catches orphaned instances
// whose task row is gone, and legacy instances written before task_id existed).
func (d *Dispatcher) instanceIDsForDelete(ctx context.Context, taskID, cellID string) ([]string, error) {
	seen := make(map[string]bool)
	var ids []string
	add := func(insts []db.WorkflowInstance) {
		for _, inst := range insts {
			if !seen[inst.ID] {
				seen[inst.ID] = true
				ids = append(ids, inst.ID)
			}
		}
	}

	if taskID != "" {
		byTask, err := d.db.ListWorkflowInstancesByTask(ctx, taskID)
		if err != nil {
			return nil, err
		}
		add(byTask)
	}

	byCell, err := d.db.ListWorkflowInstancesByCell(ctx, cellID)
	if err != nil {
		return nil, err
	}
	add(byCell)

	return ids, nil
}
