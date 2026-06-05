package daemon

import (
	"context"
	"fmt"
	"os"

	"github.com/orlandoburli/apiary/internal/config"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/router"
	"github.com/orlandoburli/apiary/internal/source"
	"github.com/orlandoburli/apiary/internal/workflow"
)

// dispatchWorkflow runs a matched cell through the workflow engine instead of
// the legacy single-shot path. In Phase 2 the route is synthesized into a
// single-step workflow; multi-step workflow definitions arrive in Phase 3.
//
// It is gated behind settings.experimental.workflow_mode and only reached when a
// run-history DB is available (the engine persists instances and step runs).
func (d *Dispatcher) dispatchWorkflow(ctx context.Context, cell model.Cell, adapter source.Adapter, match router.Match) model.RunResult {
	if d.db == nil {
		aplog.Error("cell %s: workflow_mode requires a run-history database", cell.ID)
		return model.RunResult{Success: false}
	}

	wf := workflow.SynthesizeWorkflow(match.Route)

	side := &wfSideEffects{adapter: adapter}
	exec := &wfStepExecutor{d: d}
	eng := workflow.NewEngine(d.cfg, d.db, exec, workflow.WithSideEffects(side))

	instID, success, err := eng.RunInstance(ctx, wf, cell)
	if err != nil {
		aplog.Error("cell %s: workflow run failed: %v", cell.ID, err)
		return model.RunResult{Success: false, WorkerID: match.Route.Agent}
	}
	aplog.Info("cell %s: workflow instance %s finished success=%v", cell.ID, instID, success)
	return model.RunResult{Success: success, WorkerID: match.Route.Agent}
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

	runCtx, cancel := context.WithTimeout(ctx, rr.Timeout)
	defer cancel()

	res, err := ra.Run(runCtx, rr)
	if err != nil && res.Error == nil {
		res.Error = err
	}
	return workflow.StepResult{
		Success:          res.Success,
		Output:           res.Output,
		StructuredOutput: res.StructuredOutput,
		Summary:          res.Summary,
		Err:              res.Error,
	}
}

// wfSideEffects applies source-facing actions for a workflow instance against
// the cell's source adapter.
type wfSideEffects struct {
	adapter source.Adapter
}

func (s *wfSideEffects) StateLock(ctx context.Context, cell model.Cell) error {
	return s.adapter.Acknowledge(ctx, cell, model.AckActionInProgress)
}

func (s *wfSideEffects) PostComment(ctx context.Context, cell model.Cell, comment string) error {
	return s.adapter.WriteResult(ctx, cell, model.RunResult{Success: true, Output: comment})
}

func (s *wfSideEffects) ApplyHook(ctx context.Context, cell model.Cell, hook config.OnComplete) error {
	if len(hook.AddLabels) > 0 {
		if la, ok := s.adapter.(source.LabelAdder); ok {
			if err := la.AddLabels(ctx, cell, hook.AddLabels); err != nil {
				aplog.Error("cell %s: add labels %v: %v", cell.ID, hook.AddLabels, err)
			}
		}
	}
	if hook.SetState != "" {
		if ss, ok := s.adapter.(source.StateSetter); ok {
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
