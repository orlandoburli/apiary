package daemon

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/router"
	"github.com/orlandoburli/apiary/internal/source"
	"github.com/orlandoburli/apiary/internal/workflow"
)

// workflowEngine returns the dispatcher's long-lived workflow engine, building it
// on first use. A single engine instance is essential so that workflows parked at
// approval steps survive across dispatch and poll cycles.
func (d *Dispatcher) workflowEngine() *workflow.Engine {
	d.engineOnce.Do(func() {
		opts := []workflow.Option{workflow.WithSideEffects(&wfSideEffects{d: d})}
		if d.db != nil {
			opts = append(opts,
				workflow.WithSpawner(d.newSpawner()),
				workflow.WithTaskTracker(dbTaskTracker{db: d.db}),
			)
		}
		d.engine = workflow.NewEngine(d.cfg, d.db, &wfStepExecutor{d: d}, opts...)
	})
	return d.engine
}

// newSpawner builds the WorkflowSpawner backing the APIARY_SPAWN marker: it
// resolves a named workflow from config, persists the child InternalTask, and
// runs the workflow through this dispatcher's engine. A nil DB disables spawning
// (handled by the engine as a step error if a marker is emitted).
func (d *Dispatcher) newSpawner() workflow.WorkflowSpawner {
	resolve := func(id string) (config.WorkflowConfig, bool) {
		for i := range d.cfg.Workflows {
			if d.cfg.Workflows[i].ID == id {
				return d.cfg.Workflows[i], true
			}
		}
		return config.WorkflowConfig{}, false
	}
	run := func(ctx context.Context, wf config.WorkflowConfig, task model.InternalTask) (bool, error) {
		_, success, err := d.workflowEngine().RunInstance(ctx, wf, task)
		return success, err
	}
	return workflow.NewDefaultSpawner(resolve, d.db.InternalTasks(), run, nil, nil)
}

// dbTaskTracker adapts *db.Client to workflow.TaskTracker, backing the top-level
// tasks: completion hook. The outstanding-counter and task-state methods live on
// the InternalTaskStore; HasFailedInstance queries the workflow_instances table.
type dbTaskTracker struct{ db *db.Client }

func (t dbTaskTracker) DecrementOutstanding(ctx context.Context, taskID string) (int, error) {
	return t.db.InternalTasks().DecrementOutstanding(ctx, taskID)
}

func (t dbTaskTracker) HasFailedInstance(ctx context.Context, taskID string) (bool, error) {
	return t.db.HasFailedInstance(ctx, taskID)
}

func (t dbTaskTracker) SetTaskState(ctx context.Context, taskID string, state model.TaskState) error {
	return t.db.InternalTasks().UpdateTaskState(ctx, taskID, state)
}

// dispatchWorkflow runs a matched task through the workflow engine. The route is
// matched to a defined workflow when one shares the route's id; otherwise the
// route is synthesized into a single-step workflow. cell is retained for logging
// (the engine derives its own execution view from the task + its bindings).
//
// This is the dispatch path; it requires a run-history DB (the engine persists
// instances and step runs).
func (d *Dispatcher) dispatchWorkflow(ctx context.Context, cell model.SourceItem, task model.InternalTask, match router.Match) model.RunResult {
	if d.db == nil {
		aplog.Error("cell %s: workflow dispatch requires a run-history database", cell.ID)
		return model.RunResult{Success: false}
	}

	wf := d.resolveWorkflow(match)
	instID, success, err := d.workflowEngine().RunInstance(ctx, wf, task)
	if err != nil {
		aplog.Error("cell %s: workflow run failed: %v", cell.ID, err)
		return model.RunResult{Success: false, WorkerID: match.Route.Agent}
	}
	aplog.Info("cell %s: workflow instance %s started (success=%v; may be awaiting approval)", cell.ID, instID, success)
	return model.RunResult{Success: success, WorkerID: match.Route.Agent}
}

// resolveWorkflow returns the workflow to run for a matched route: a workflow
// whose id equals the route id (a full multi-step definition), or a synthesized
// single-step workflow for a plain route.
func (d *Dispatcher) resolveWorkflow(match router.Match) config.WorkflowConfig {
	for i := range d.cfg.Workflows {
		if d.cfg.Workflows[i].ID == match.Route.ID {
			return d.cfg.Workflows[i]
		}
	}
	return config.WorkflowConfig{}
}

// checkApprovals drives the engine's parked-approval evaluation using each
// source's TaskPoller. Called once per poll cycle. Sources that do not implement
// TaskPoller cannot resolve approvals (the instance stays parked).
func (d *Dispatcher) checkApprovals(ctx context.Context) {
	if d.db == nil || d.engine == nil {
		return
	}
	d.engine.CheckParkedApprovals(ctx, func(sourceID, cellID string) (model.SourceItem, error) {
		adapter, ok := d.sources[sourceID]
		if !ok {
			return model.SourceItem{}, fmt.Errorf("source %q not found", sourceID)
		}
		poller, ok := adapter.(source.TaskPoller)
		if !ok {
			return model.SourceItem{}, fmt.Errorf("source %q does not support per-task polling (approvals)", sourceID)
		}
		return poller.PollTask(ctx, cellID)
	})
}

// wfStepExecutor adapts the dispatcher's runner machinery to the workflow
// engine's StepExecutor interface: it builds a RunRequest for one agent step and
// invokes the agent's runner.
type wfStepExecutor struct {
	d *Dispatcher
}

func (x *wfStepExecutor) ExecuteStep(ctx context.Context, req workflow.StepRequest) workflow.StepResult {
	pseudoWorkerID := fmt.Sprintf("agent-%s", req.Step.Agent)
	ra, ok := x.d.runners[pseudoWorkerID]
	if !ok {
		return workflow.StepResult{Success: false, Err: fmt.Errorf("runner for agent %q not found", req.Step.Agent)}
	}

	// Per-agent source token for write operations performed by the runner.
	if req.Agent.SourceToken != "" {
		ctx = context.WithValue(ctx, source.SourceTokenCtxKey, req.Agent.SourceToken)
	}

	rr := model.RunRequest{
		Cell:               req.Cell,
		WorkerID:           req.Step.Agent,
		Model:              req.Model,
		MaxTurns:           15,
		SystemPrepend:      req.MemoryDoc,
		SystemAppend:       composeSystemAppend(req.Prompt, readSoulFile(req.Agent, req.Cell.ID)),
		SummaryPrompt:      req.Step.SummaryPrompt,
		StepID:             req.Step.ID,
		WorkflowInstanceID: req.InstanceID,
		WorkingDir:         "/",
		Env:                gitIdentityEnv(req.Agent),
		Timeout:            x.d.cfg.Settings.TaskTimeoutDuration(),
	}

	if x.d.logger != nil {
		cellID := req.Cell.ID
		rr.LogSink = func(e model.LogEntry) {
			switch e.Level {
			case "error":
				x.d.logger.TaskError(ctx, cellID, e.Message)
			case "info":
				x.d.logger.TaskInfo(ctx, cellID, e.Message)
			default:
				x.d.logger.TaskDebug(ctx, cellID, e.Message)
			}
			aplog.Info("[%s] %s", cellID, e.Message)
		}
	}

	// Record a task_executions row for this step so the dashboard (Tasks
	// history, Usage/cost, agent stats, live "running" status) observes
	// workflow steps the same way it observed legacy single-shot runs. Each
	// step is one execution; usage and PID/heartbeat are wired through.
	exec := x.beginExecution(ctx, req)
	if exec != nil {
		execID := exec.ID
		rr.SetPID = func(pid int) { _ = x.d.db.SetPID(ctx, execID, pid) }
		rr.Heartbeat = func() { _ = x.d.db.SendHeartbeat(ctx, execID) }
	}

	runCtx, cancel := context.WithTimeout(ctx, rr.Timeout)
	// Register the cancel func so `apiary restart` / ForceRestart can interrupt
	// an in-flight step. A single key per cell mirrors legacy behaviour.
	x.d.runCancel.Store(req.Cell.ID, cancel)
	defer func() {
		cancel()
		x.d.runCancel.Delete(req.Cell.ID)
	}()

	res, err := ra.Run(runCtx, rr)
	if err != nil && res.Error == nil {
		res.Error = err
	}

	x.finishExecution(ctx, exec, res)

	// publish: off suppresses write-back before the engine ever sees the payload,
	// even when the agent emitted an APIARY_PUBLISH block.
	publishPayload := res.PublishPayload
	if req.Step.Publish == config.PublishOff {
		publishPayload = ""
	}

	// A malformed APIARY_SPAWN block is a step-level error so the workflow fails
	// with a descriptive message rather than silently dropping the request.
	if res.SpawnError != nil {
		return workflow.StepResult{Success: false, Output: res.Output, Err: res.SpawnError}
	}

	return workflow.StepResult{
		Success:          res.Success,
		Output:           res.Output,
		StructuredOutput: res.StructuredOutput,
		Summary:          res.Summary,
		PublishPayload:   publishPayload,
		SpawnRequest:     res.SpawnRequest,
		Err:              res.Error,
	}
}

// beginExecution creates the task_executions row for a step run, or returns nil
// when no run-history DB is configured. The attempt number continues the cell's
// execution history so retries/steps accumulate rather than reset.
func (x *wfStepExecutor) beginExecution(ctx context.Context, req workflow.StepRequest) *db.Execution {
	if x.d.db == nil {
		return nil
	}
	attempt := 1
	if last, _ := x.d.db.GetLastExecution(ctx, req.Cell.ID); last != nil {
		attempt = last.Attempt + 1
	}
	runnerType := x.d.agentRunner[req.Step.Agent]
	exec, err := x.d.db.CreateExecution(ctx, req.Cell.ID, req.Step.Agent, req.Cell.Title,
		req.Cell.Number, req.Cell.URL, req.Model, runnerType, attempt)
	if err != nil {
		aplog.Error("cell %s: create execution record: %v", req.Cell.ID, err)
		return nil
	}
	if req.InstanceID != "" {
		_ = x.d.db.SetStepLink(ctx, exec.ID, req.InstanceID, req.Step.ID)
	}
	return exec
}

// finishExecution flips a step's execution row to its terminal state and records
// duration and usage. A nil exec (no DB) is a no-op.
func (x *wfStepExecutor) finishExecution(ctx context.Context, exec *db.Execution, res model.RunResult) {
	if exec == nil || x.d.db == nil {
		return
	}
	now := time.Now()
	exec.CompletedAt = &now
	exec.DurationMs = res.Duration.Milliseconds()
	exec.Status = "success"
	if !res.Success {
		exec.Status = "failed"
		if res.Error != nil {
			exec.ErrorMsg = res.Error.Error()
		}
	}
	if res.Usage != nil {
		exec.InputTokens = res.Usage.InputTokens
		exec.OutputTokens = res.Usage.OutputTokens
		exec.TotalTokens = res.Usage.TotalTokens
		exec.NumTurns = res.Usage.NumTurns
		exec.NumToolCalls = res.Usage.NumToolCalls
		exec.CostUSD = res.Usage.CostUSD
	}
	if err := x.d.db.UpdateExecution(ctx, exec); err != nil {
		aplog.Error("cell %s: update execution record: %v", exec.TaskID, err)
	}
}

// wfSideEffects applies source-facing actions for a workflow instance. It fans
// each action out across the task's source bindings, resolving the adapter per
// binding (via binding.SourceID) so a single long-lived engine can serve tasks
// bound to any configured source. A task with no bindings (spawned) is a no-op.
type wfSideEffects struct {
	d *Dispatcher
}

// sourceItemFromBinding reconstructs the SourceItem an adapter call needs from a
// binding (source identity) plus the live task (content + routing attributes).
func sourceItemFromBinding(task model.InternalTask, b model.SourceBinding) model.SourceItem {
	return model.SourceItem{
		ID:          b.SourceItemID,
		SourceID:    b.SourceID,
		Number:      b.SourceItemNumber,
		URL:         b.SourceItemURL,
		Title:       task.Title,
		Description: task.Description,
		Labels:      task.Metadata.Labels,
		Type:        task.Metadata.Type,
		Priority:    task.Metadata.Priority,
		State:       task.Metadata.State,
	}
}

func (s *wfSideEffects) StateLock(ctx context.Context, task model.InternalTask, bindings []model.SourceBinding) error {
	for _, b := range bindings {
		adapter := s.d.sources[b.SourceID]
		if adapter == nil {
			aplog.Error("task %s: no adapter for source %q (state_lock)", task.ID, b.SourceID)
			continue
		}
		item := sourceItemFromBinding(task, b)
		if err := adapter.Acknowledge(ctx, item, model.AckActionInProgress); err != nil {
			aplog.Error("item %s: state_lock: %v", item.ID, err)
		}
	}
	return nil
}

func (s *wfSideEffects) PostComment(ctx context.Context, task model.InternalTask, bindings []model.SourceBinding, comment string) error {
	for _, b := range bindings {
		adapter := s.d.sources[b.SourceID]
		if adapter == nil {
			aplog.Error("task %s: no adapter for source %q (post_comment)", task.ID, b.SourceID)
			continue
		}
		item := sourceItemFromBinding(task, b)
		if err := adapter.WriteResult(ctx, item, model.RunResult{Success: true, Output: comment}); err != nil {
			aplog.Error("item %s: post_comment: %v", item.ID, err)
		}
	}
	return nil
}

func (s *wfSideEffects) ApplyHook(ctx context.Context, task model.InternalTask, bindings []model.SourceBinding, hook config.OnComplete) error {
	for _, b := range bindings {
		adapter := s.d.sources[b.SourceID]
		if adapter == nil {
			aplog.Error("task %s: no adapter for source %q (apply_hook)", task.ID, b.SourceID)
			continue
		}
		item := sourceItemFromBinding(task, b)
		if len(hook.AddLabels) > 0 {
			if la, ok := adapter.(source.LabelAdder); ok {
				if err := la.AddLabels(ctx, item, hook.AddLabels); err != nil {
					aplog.Error("item %s: add labels %v: %v", item.ID, hook.AddLabels, err)
				}
			}
		}
		if hook.SetState != "" {
			if ss, ok := adapter.(source.StateSetter); ok {
				if err := ss.SetState(ctx, item, hook.SetState); err != nil {
					aplog.Error("item %s: set_state %q: %v", item.ID, hook.SetState, err)
				}
			}
		}
	}
	return nil
}

// composeSystemAppend combines a step-level prompt with the agent's soul file.
// The step prompt comes first so per-step (and foreach per-item) instructions
// lead, followed by the agent's standing guidance.
func composeSystemAppend(stepPrompt, soul string) string {
	switch {
	case stepPrompt == "":
		return soul
	case soul == "":
		return stepPrompt
	default:
		return stepPrompt + "\n\n" + soul
	}
}

// readSoulFile loads an agent's soul file, returning "" (and logging) on error.
func readSoulFile(agent config.AgentConfig, cellID string) string {
	if agent.SoulFile == "" {
		return ""
	}
	data, err := os.ReadFile(agent.SoulFile)
	if err != nil {
		aplog.Error("cell %s: reading soul file %q: %v", cellID, agent.SoulFile, err)
		return ""
	}
	return string(data)
}

// gitIdentityEnv returns the git author/committer identity env vars derived from
// the agent's source identity, so commits use the agent's account.
func gitIdentityEnv(agent config.AgentConfig) map[string]string {
	env := map[string]string{}
	if agent.SourceName != "" {
		env["GIT_AUTHOR_NAME"] = agent.SourceName
		env["GIT_COMMITTER_NAME"] = agent.SourceName
	}
	if agent.SourceEmail != "" {
		env["GIT_AUTHOR_EMAIL"] = agent.SourceEmail
		env["GIT_COMMITTER_EMAIL"] = agent.SourceEmail
	}
	return env
}

// compile-time interface checks.
var (
	_ workflow.StepExecutor = (*wfStepExecutor)(nil)
	_ workflow.SideEffects  = (*wfSideEffects)(nil)
)
