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
		d.engine = workflow.NewEngine(d.cfg, d.db, &wfStepExecutor{d: d},
			workflow.WithSideEffects(&wfSideEffects{d: d}))
	})
	return d.engine
}

// dispatchWorkflow runs a matched cell through the workflow engine. The route is
// matched to a defined workflow when one shares the route's id; otherwise the
// route is synthesized into a single-step workflow.
//
// This is the dispatch path; it requires a run-history DB (the engine persists
// instances and step runs).
func (d *Dispatcher) dispatchWorkflow(ctx context.Context, cell model.Cell, adapter source.Adapter, match router.Match) model.RunResult {
	if d.db == nil {
		aplog.Error("cell %s: workflow dispatch requires a run-history database", cell.ID)
		return model.RunResult{Success: false}
	}

	wf := d.resolveWorkflow(match)
	instID, success, err := d.workflowEngine().RunInstance(ctx, wf, cell)
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
	d.engine.CheckParkedApprovals(ctx, func(sourceID, cellID string) (model.Cell, error) {
		adapter, ok := d.sources[sourceID]
		if !ok {
			return model.Cell{}, fmt.Errorf("source %q not found", sourceID)
		}
		poller, ok := adapter.(source.TaskPoller)
		if !ok {
			return model.Cell{}, fmt.Errorf("source %q does not support per-task polling (approvals)", sourceID)
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

	return workflow.StepResult{
		Success:          res.Success,
		Output:           res.Output,
		StructuredOutput: res.StructuredOutput,
		Summary:          res.Summary,
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

// wfSideEffects applies source-facing actions for a workflow instance. It
// resolves the adapter per cell (via the cell's SourceID) so a single long-lived
// engine can serve cells from any configured source.
type wfSideEffects struct {
	d *Dispatcher
}

// adapterFor returns the source adapter that produced the cell, or nil.
func (s *wfSideEffects) adapterFor(cell model.Cell) source.Adapter {
	return s.d.sources[cell.SourceID]
}

func (s *wfSideEffects) StateLock(ctx context.Context, cell model.Cell) error {
	adapter := s.adapterFor(cell)
	if adapter == nil {
		return fmt.Errorf("no adapter for source %q", cell.SourceID)
	}
	return adapter.Acknowledge(ctx, cell, model.AckActionInProgress)
}

func (s *wfSideEffects) PostComment(ctx context.Context, cell model.Cell, comment string) error {
	adapter := s.adapterFor(cell)
	if adapter == nil {
		return fmt.Errorf("no adapter for source %q", cell.SourceID)
	}
	return adapter.WriteResult(ctx, cell, model.RunResult{Success: true, Output: comment})
}

func (s *wfSideEffects) ApplyHook(ctx context.Context, cell model.Cell, hook config.OnComplete) error {
	adapter := s.adapterFor(cell)
	if adapter == nil {
		return fmt.Errorf("no adapter for source %q", cell.SourceID)
	}
	if len(hook.AddLabels) > 0 {
		if la, ok := adapter.(source.LabelAdder); ok {
			if err := la.AddLabels(ctx, cell, hook.AddLabels); err != nil {
				aplog.Error("cell %s: add labels %v: %v", cell.ID, hook.AddLabels, err)
			}
		}
	}
	if hook.SetState != "" {
		if ss, ok := adapter.(source.StateSetter); ok {
			if err := ss.SetState(ctx, cell, hook.SetState); err != nil {
				aplog.Error("cell %s: set_state %q: %v", cell.ID, hook.SetState, err)
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
