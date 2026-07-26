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
	"github.com/orlandoburli/apiary/internal/runner/execution"
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
		if d.memStore != nil {
			opts = append(opts, workflow.WithMemoryStore(d.memStore))
		}
		// Wire up CI status polling for wait_for steps.
		opts = append(opts, workflow.WithCIStatusChecker(func(ctx context.Context, sourceID, sourceItemID string) (source.CIStatus, error) {
			adapter, ok := d.sources[sourceID]
			if !ok {
				return source.CIStatus{}, fmt.Errorf("source %q not found", sourceID)
			}
			poller, ok := adapter.(source.CIStatusPoller)
			if !ok {
				return source.CIStatus{}, fmt.Errorf("source %q does not support CI status polling", sourceID)
			}
			return poller.PollCIStatus(ctx, sourceItemID)
		}))
		// Wire up blocker listing for wait_for steps with kind: dependency.
		opts = append(opts, workflow.WithDependencyChecker(func(ctx context.Context, sourceID, sourceItemID, linkType string) ([]source.BlockerRef, error) {
			adapter, ok := d.sources[sourceID]
			if !ok {
				return nil, fmt.Errorf("source %q not found", sourceID)
			}
			lister, ok := adapter.(source.BlockerLister)
			if !ok {
				return nil, fmt.Errorf("source %q does not support blocker listing (wait_for kind: dependency)", sourceID)
			}
			return lister.ListBlockers(ctx, sourceItemID, linkType)
		}))
		var store workflow.Store = d.db
		if d.db != nil {
			store = pluginExportStore{Client: d.db, dispatcher: d}
		}
		d.engine = workflow.NewEngine(d.cfg, store, &wfStepExecutor{d: d}, opts...)
	})
	return d.engine
}

// rehydrateParkedApprovals reconstructs workflow instances persisted in the
// approval_waiting state and re-registers them in the engine's in-memory parked
// set, so the polling loop's approval check (checkApprovals → CheckParkedApprovals)
// re-evaluates them after a daemon restart.
//
// The parked set lives only in memory and is empty on a fresh process, so without
// this an instance left waiting for approval when the daemon stopped would never
// be re-evaluated: it would stay approval_waiting forever, its task's
// outstanding-workflow counter would never drain, and the task would stay stuck in
// 'registered'. ReconcileOrphanWorkflowInstances deliberately leaves
// approval_waiting rows untouched (interrupting them would lose the wait); this
// rehydration is what brings them back to life. Called once at startup, after the
// orphan reconcile and before the poll loops start, so it builds the engine eagerly
// (via workflowEngine) — that also makes the very first poll's approval check live.
func (d *Dispatcher) rehydrateParkedApprovals(ctx context.Context) {
	if d.db == nil {
		return
	}
	instances, err := d.db.ListWorkflowInstancesByState(ctx, db.InstanceStateApprovalWaiting)
	if err != nil {
		aplog.Warn("rehydrate parked approvals: list instances: %v", err)
		return
	}
	if len(instances) == 0 {
		return
	}

	engine := d.workflowEngine()
	rehydrated := 0
	for i := range instances {
		inst := instances[i]
		wf, ok := d.workflowByID(inst.WorkflowID)
		if !ok {
			aplog.Warn("rehydrate parked approval %s: workflow %q no longer defined — leaving parked", inst.ID, inst.WorkflowID)
			continue
		}
		steps, err := d.db.ListStepRuns(ctx, inst.ID)
		if err != nil {
			aplog.Warn("rehydrate parked approval %s: list step runs: %v", inst.ID, err)
			continue
		}
		task := d.taskForInstance(ctx, &inst)
		if err := engine.RehydrateApproval(ctx, inst.ID, wf, task, steps, inst.UpdatedAt); err != nil {
			aplog.Warn("rehydrate parked approval %s: %v", inst.ID, err)
			continue
		}
		rehydrated++
	}
	if rehydrated > 0 {
		aplog.Info("rehydrated %d parked approval instance(s) from a previous run", rehydrated)
	}
}

// rehydrateParkedWaits reconstructs workflow instances persisted in the
// waiting state and re-registers them in the engine's in-memory parked set,
// so the polling loop's poll check (checkWaits → CheckParkedWaits) re-checks them
// after a daemon restart. It mirrors rehydrateParkedApprovals: the parked set
// lives only in memory and is empty on a fresh process, and
// ReconcileOrphanWorkflowInstances deliberately leaves waiting rows untouched
// (interrupting them would lose the wait). Called once at startup, after the orphan
// reconcile and before the poll loops start.
func (d *Dispatcher) rehydrateParkedWaits(ctx context.Context) {
	if d.db == nil {
		return
	}
	instances, err := d.db.ListWorkflowInstancesByState(ctx, db.InstanceStateWaiting)
	if err != nil {
		aplog.Warn("rehydrate parked waits: list instances: %v", err)
		return
	}
	if len(instances) == 0 {
		return
	}

	engine := d.workflowEngine()
	rehydrated := 0
	for i := range instances {
		inst := instances[i]
		wf, ok := d.workflowByID(inst.WorkflowID)
		if !ok {
			aplog.Warn("rehydrate parked wait %s: workflow %q no longer defined — leaving parked", inst.ID, inst.WorkflowID)
			continue
		}
		steps, err := d.db.ListStepRuns(ctx, inst.ID)
		if err != nil {
			aplog.Warn("rehydrate parked wait %s: list step runs: %v", inst.ID, err)
			continue
		}
		task := d.taskForInstance(ctx, &inst)
		if err := engine.RehydrateWait(ctx, inst.ID, wf, task, steps); err != nil {
			aplog.Warn("rehydrate parked wait %s: %v", inst.ID, err)
			continue
		}
		rehydrated++
	}
	if rehydrated > 0 {
		aplog.Info("rehydrated %d parked wait instance(s) from a previous run", rehydrated)
	}
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
		aplog.Error("cell %s: workflow dispatch requires a run-history database", cell.LogLabel())
		return model.RunResult{Success: false}
	}

	wf := d.resolveWorkflow(match)
	wf = withTrustGate(wf, cell, d.cfg.Settings.TrustGate)
	instID, success, err := d.workflowEngine().RunInstance(ctx, wf, task)
	if err != nil {
		aplog.Error("cell %s: workflow run failed: %v", cell.LogLabel(), err)
		return model.RunResult{Success: false, WorkerID: match.Route.Agent}
	}
	aplog.Info("cell %s: workflow instance %s started (success=%v; may be awaiting approval)", cell.LogLabel(), instID, success)
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

// checkApprovals drives the engine's parked-approval re-checks once per poll cycle.
// Each parked instance is handled on its own goroutine so a long-running follow-on
// agent step on one resumed instance cannot delay the cheap re-evaluation of any
// other parked approval — the head-of-line blocking that, on the single poll loop,
// mirrors the parked wait_for starvation fixed in checkWaits.
//
// Two phases per instance, mirroring checkWaits / fresh dispatch's split:
//   - RecheckApproval: a cheap timeout + per-binding TaskPoller evaluation, run
//     UNGATED, so a busy agent never blocks it. A still-waiting approval stops here
//     and stays parked.
//   - ResolveApproval: the expensive graph advance (may run a follow-on agent), run
//     only when the decision is resume/abort and admitted through the agent's
//     semaphore so it respects max_workers — exactly like fanOut and WakeWait.
//
// The approvalAdvancing guard keeps a slow advance started on one cycle from being
// re-checked or re-advanced by the next. Sources that do not implement TaskPoller
// cannot resolve approvals (poll errors, the instance stays parked). A nil DB/engine
// (no run-history) disables it. This does not block the poll loop: it launches the
// goroutines and returns.
func (d *Dispatcher) checkApprovals(ctx context.Context) {
	if d.db == nil || d.engine == nil {
		return
	}
	poll := func(sourceID, cellID string) (model.SourceItem, error) {
		adapter, ok := d.sources[sourceID]
		if !ok {
			return model.SourceItem{}, fmt.Errorf("source %q not found", sourceID)
		}
		poller, ok := adapter.(source.TaskPoller)
		if !ok {
			return model.SourceItem{}, fmt.Errorf("source %q does not support per-task polling (approvals)", sourceID)
		}
		return poller.PollTask(ctx, cellID)
	}

	for _, p := range d.engine.ParkedApprovals() {
		p := p
		// Skip an instance already being re-checked/advanced by an earlier cycle.
		if _, busy := d.approvalAdvancing.LoadOrStore(p.InstanceID, struct{}{}); busy {
			continue
		}
		agentCh := d.agentSem[p.AgentID]
		go func() {
			defer d.approvalAdvancing.Delete(p.InstanceID)

			// Cheap re-evaluation, ungated. Still waiting → leave it parked.
			var stored *db.ApprovalRequest
			decision := workflow.ApprovalWait
			if req, _ := d.db.GetApprovalByInstance(ctx, p.InstanceID); req != nil && (req.Status == db.ApprovalApproved || req.Status == db.ApprovalRejected) {
				stored = req
				decision = workflow.ApprovalResume
				if req.Status == db.ApprovalRejected {
					decision = workflow.ApprovalAbort
				}
			} else {
				decision = d.engine.RecheckApproval(ctx, p.InstanceID, poll)
			}
			if decision == workflow.ApprovalWait {
				return
			}

			// Resume/abort: admit the advance through the agent's semaphore (held for
			// the whole advance, just like fanOut) so a follow-on agent step honours
			// max_workers. A run waiting for a slot is not yet counted active.
			if agentCh != nil {
				select {
				case agentCh <- struct{}{}:
				case <-ctx.Done():
					return // shutting down before a slot freed; never acquired
				}
				defer func() { <-agentCh }()
			}
			if stored != nil {
				_, _ = d.engine.ResolveApprovalResponse(ctx, p.InstanceID, db.ApprovalResponse{Decision: stored.Status, Actor: stored.RespondedBy, Channel: stored.ResponseChannel, IdempotencyKey: stored.IdempotencyKey, Feedback: stored.Feedback, Values: stored.Values})
			} else {
				_, _ = d.engine.ResolveApproval(ctx, p.InstanceID, decision)
			}
		}()
	}
}

// checkWaits drives the engine's parked-wait re-checks (CI status) once per poll
// cycle. Each parked instance is handled on its own goroutine so a long-running
// follow-on agent step on one woken instance cannot delay the cheap CI re-check of
// any other parked instance — the head-of-line blocking that, on the single poll
// loop, once left a parked CI wait un-rechecked for 43 minutes while another
// instance ran a 35-minute agent.
//
// Two phases per instance, mirroring fresh dispatch's split:
//   - RecheckWait: a cheap CI status query, run UNGATED, so a busy agent never
//     blocks it. A still-pending wait stops here and stays parked.
//   - WakeWait: the expensive graph advance (may run a follow-on agent), run only
//     when CI is terminal and admitted through the agent's semaphore so it respects
//     max_workers — exactly like fanOut.
//
// The waitAdvancing guard keeps a slow advance started on one cycle from being
// re-checked or re-advanced by the next. A nil DB/engine (no run-history) disables
// it. This does not block the poll loop: it launches the goroutines and returns.
func (d *Dispatcher) checkWaits(ctx context.Context) {
	if d.db == nil || d.engine == nil {
		return
	}
	for _, w := range d.engine.ParkedWaits() {
		w := w
		// Skip an instance already being re-checked/advanced by an earlier cycle.
		if _, busy := d.waitAdvancing.LoadOrStore(w.InstanceID, struct{}{}); busy {
			continue
		}
		agentCh := d.agentSem[w.AgentID]
		go func() {
			defer d.waitAdvancing.Delete(w.InstanceID)

			// Cheap CI re-check, ungated. Still pending → leave it parked.
			if !d.engine.RecheckWait(ctx, w.InstanceID) {
				return
			}

			// Terminal: admit the advance through the agent's semaphore (held for
			// the whole advance, just like fanOut) so a follow-on agent step honours
			// max_workers. A run waiting for a slot is not yet counted active.
			if agentCh != nil {
				select {
				case agentCh <- struct{}{}:
				case <-ctx.Done():
					return // shutting down before a slot freed; never acquired
				}
				defer func() { <-agentCh }()
			}
			d.engine.WakeWait(ctx, w.InstanceID)
		}()
	}
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

	env := withMemoryDir(stepEnv(req.Agent, req.WorkflowEnv, req.Step.Env), x.d.memDir)

	rr := model.RunRequest{
		Cell:               req.Cell,
		WorkerID:           req.Step.Agent,
		Model:              req.Model,
		MaxTurns:           req.Agent.MaxTurns,
		SystemPrepend:      req.MemoryDoc,
		SystemAppend:       composeSystemAppend(req.Prompt, readSoulFile(req.Agent, req.Cell.ID)),
		OutputInstruction:  execution.OutputSchemaInstruction(req.Step.OutputSchema),
		SummaryPrompt:      req.Step.SummaryPrompt,
		StepID:             req.Step.ID,
		WorkflowInstanceID: req.InstanceID,
		WorkingDir:         "/",
		Env:                env,
		Timeout:            x.d.cfg.Settings.TaskTimeoutDuration(),
	}

	if x.d.logger != nil {
		cellID := req.Cell.ID
		// DB logs stay keyed by the cell id; the console/file mirror carries the
		// human-facing reference too (e.g. Jira's "CDT-123") so a task is
		// recognizable in the stream.
		label := req.Cell.LogLabel()
		rr.LogSink = func(e model.LogEntry) {
			switch e.Level {
			case "error":
				x.d.logger.TaskError(ctx, cellID, e.Message)
			case "info":
				x.d.logger.TaskInfo(ctx, cellID, e.Message)
			default:
				x.d.logger.TaskDebug(ctx, cellID, e.Message)
			}
			aplog.Info("[%s] %s", label, e.Message)
		}
	}

	// Markdown transcript: render the provider's stream-json events (assistant
	// messages, thinking, tool calls/results) into a per-step markdown file
	// under <logDir>/transcripts/<cellID>/. Best-effort — a transcript failure
	// never blocks the run.
	if x.d.logger != nil {
		name := fmt.Sprintf("%s-%s", req.InstanceID, req.Step.ID)
		if f, path, err := x.d.logger.CreateTranscript(req.Cell.ID, name); err == nil {
			defer f.Close()
			tr := execution.NewTranscript(f, execution.TranscriptMeta{
				Title:    fmt.Sprintf("%s — %s", req.Cell.LogLabel(), req.Cell.Title),
				Agent:    req.Step.Agent,
				Model:    req.Model,
				Step:     req.Step.ID,
				Instance: req.InstanceID,
				Started:  time.Now(),
			})
			rr.TranscriptSink = tr.Feed
			aplog.Debug("[%s] transcript: %s", req.Cell.LogLabel(), path)
		}
	}

	// Run chain: the agent's primary runner first, then its rate-limit failover
	// chain. A candidate whose runner type is currently paused by a provider rate
	// limit is skipped (unless it is the last resort). Each attempt is its own
	// task_executions row — recorded the same way legacy single-shot runs were —
	// so the dashboard (Tasks history, Usage/cost, agent stats, live "running")
	// shows the failover. usage and PID/heartbeat are wired through per attempt.
	candidates := append([]runnerCandidate{{
		adapter:    ra,
		runnerType: x.d.agentRunner[req.Step.Agent],
		model:      req.Model,
	}}, x.d.agentFallbacks[req.Step.Agent]...)

	var res model.RunResult
	// summedUsage accumulates token/cost across every attempt (primary + failovers)
	// so the step run reflects everything the step actually cost, not just the
	// winning attempt. Per-attempt detail stays in each task_executions row.
	var summedUsage model.Usage
	var anyUsage bool
	for i, c := range candidates {
		last := i == len(candidates)-1
		if !last && x.d.runnerPausedUntil(c.runnerType).After(time.Now()) {
			aplog.Info("[%s] runner %q paused by rate limit, skipping to fallback", req.Cell.LogLabel(), c.runnerType)
			x.d.recordExecutionEvent(ctx, db.ExecutionEvent{Type: "fallback.selected", WorkflowInstanceID: req.InstanceID, StepID: req.Step.ID,
				Metadata: map[string]any{"from_runner": c.runnerType, "to_runner": candidates[i+1].runnerType, "reason": "runner paused"}})
			continue
		}

		rr.Model = c.model
		exec := x.beginExecution(ctx, req, c.model, c.runnerType)
		if exec != nil {
			execID := exec.ID
			rr.SetPID = func(pid int) { _ = x.d.db.SetPID(ctx, execID, pid) }
			rr.Heartbeat = func() { _ = x.d.db.SendHeartbeat(ctx, execID) }
		}

		runCtx, cancel := context.WithTimeout(ctx, rr.Timeout)
		// Register the cancel func so `apiary restart` / ForceRestart can interrupt
		// an in-flight step. A single key per cell mirrors legacy behaviour.
		x.d.runCancel.Store(req.Cell.ID, cancel)
		out, err := c.adapter.Run(runCtx, rr)
		cancel()
		x.d.runCancel.Delete(req.Cell.ID)
		if err != nil && out.Error == nil {
			out.Error = err
		}
		x.finishExecution(ctx, exec, out)
		res = out

		if out.Usage != nil {
			anyUsage = true
			summedUsage.InputTokens += out.Usage.InputTokens
			summedUsage.OutputTokens += out.Usage.OutputTokens
			summedUsage.TotalTokens += out.Usage.TotalTokens
			summedUsage.CacheCreationTokens += out.Usage.CacheCreationTokens
			summedUsage.CacheReadTokens += out.Usage.CacheReadTokens
			summedUsage.NumTurns += out.Usage.NumTurns
			summedUsage.NumToolCalls += out.Usage.NumToolCalls
			summedUsage.CostUSD += out.Usage.CostUSD
		}

		// Classify the failure and either fail over or stop. A run that was
		// rate-limited, credit-exhausted, or aborted did no useful work — pause
		// the runner type with the appropriate cooldown and try the next fallback
		// candidate if one remains.
		failureKind := out.FailureKind
		if failureKind == model.FailureNone {
			// Backward compatibility: some runners set RateLimited without FailureKind.
			if out.RateLimited {
				failureKind = model.FailureRateLimited
			}
		}

		if failureKind != model.FailureNone && !last {
			x.d.pauseRunnerWithKind(c.runnerType, out.RateLimitResetsAt, failureKind)
			if failureKind == model.FailureRateLimited || failureKind == model.FailureCreditExhausted {
				x.d.recordExecutionEvent(ctx, db.ExecutionEvent{Type: "rate_limit.detected", WorkflowInstanceID: req.InstanceID, StepID: req.Step.ID,
					Metadata: map[string]any{"runner": c.runnerType, "kind": failureKind.String()}})
			}
			x.d.recordExecutionEvent(ctx, db.ExecutionEvent{Type: "fallback.selected", WorkflowInstanceID: req.InstanceID, StepID: req.Step.ID,
				Metadata: map[string]any{"from_runner": c.runnerType, "to_runner": candidates[i+1].runnerType, "reason": failureKind.String()}})

			label := "failed"
			switch failureKind {
			case model.FailureRateLimited:
				label = "rate-limited"
			case model.FailureCreditExhausted:
				label = "credit-exhausted"
			case model.FailureAborted:
				label = "aborted"
			}
			aplog.Info("[%s] runner %q %s, failing over to next runner", req.Cell.LogLabel(), c.runnerType, label)
			continue
		}
		break
	}

	// publish: off suppresses write-back before the engine ever sees the payload,
	// even when the agent emitted an APIARY_PUBLISH block.
	publishPayload := res.PublishPayload
	if req.Step.Publish == config.PublishOff {
		publishPayload = ""
	}

	// memory.memorize: off mirrors publish: off — requests (and any parse error)
	// are dropped before the engine ever sees them.
	memorizeRequests := res.MemorizeRequests
	memorizeError := res.MemorizeError
	if !req.Step.MemorizeEnabled() {
		memorizeRequests = nil
		memorizeError = nil
	}

	var usage *model.Usage
	if anyUsage {
		u := summedUsage
		usage = &u
	}

	// A malformed APIARY_SPAWN block is a step-level error so the workflow fails
	// with a descriptive message rather than silently dropping the request. The
	// step's memorize requests still travel — persisted knowledge should survive
	// an unrelated spawn failure.
	if res.SpawnError != nil {
		return workflow.StepResult{Success: false, Output: res.Output, Usage: usage, InputPrompt: res.InputPrompt,
			MemorizeRequests: memorizeRequests, MemorizeError: memorizeError, Err: res.SpawnError}
	}

	return workflow.StepResult{
		Success:          res.Success,
		Output:           res.Output,
		StructuredOutput: res.StructuredOutput,
		Summary:          res.Summary,
		PublishPayload:   publishPayload,
		SpawnRequest:     res.SpawnRequest,
		SpawnRequests:    res.SpawnRequests,
		MemorizeRequests: memorizeRequests,
		MemorizeError:    memorizeError,
		Usage:            usage,
		InputPrompt:      res.InputPrompt,
		Err:              res.Error,
	}
}

// beginExecution creates the task_executions row for a step run, or returns nil
// when no run-history DB is configured. The attempt number continues the cell's
// execution history so retries/steps accumulate rather than reset.
func (x *wfStepExecutor) beginExecution(ctx context.Context, req workflow.StepRequest, model, runnerType string) *db.Execution {
	if x.d.db == nil {
		return nil
	}
	attempt := 1
	if last, _ := x.d.db.GetLastExecution(ctx, req.Cell.ID); last != nil {
		attempt = last.Attempt + 1
	}
	exec, err := x.d.db.CreateExecution(ctx, req.Cell.ID, req.Step.Agent, req.Cell.Title,
		req.Cell.Number, req.Cell.URL, model, runnerType, attempt)
	if err != nil {
		aplog.Error("cell %s: create execution record: %v", req.Cell.LogLabel(), err)
		return nil
	}
	if req.InstanceID != "" {
		_ = x.d.db.SetStepLink(ctx, exec.ID, req.InstanceID, req.Step.ID)
	}
	event := db.ExecutionEvent{Type: "runner.started", WorkflowInstanceID: req.InstanceID, StepID: req.Step.ID, AttemptID: fmt.Sprint(exec.ID),
		Metadata: map[string]any{"runner": runnerType, "model": model, "attempt": attempt}}
	if inst, _ := x.d.db.GetWorkflowInstance(ctx, req.InstanceID); inst != nil {
		event.TaskID, event.WorkflowID = inst.TaskID, inst.WorkflowID
	}
	x.d.recordExecutionEvent(ctx, event)
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
		exec.CacheCreationTokens = res.Usage.CacheCreationTokens
		exec.CacheReadTokens = res.Usage.CacheReadTokens
		exec.NumTurns = res.Usage.NumTurns
		exec.NumToolCalls = res.Usage.NumToolCalls
		exec.CostUSD = res.Usage.CostUSD
	}
	exec.InputPrompt = res.InputPrompt
	exec.OutputText = res.Output
	exec.CreditExhausted = res.CreditExhausted
	if !res.CreditExhausted && res.RateLimited {
		exec.FailureKind = "rate_limited"
	} else if res.CreditExhausted {
		exec.FailureKind = "credit_exhausted"
	} else if res.FailureKind != model.FailureNone && res.FailureKind != model.FailureRateLimited {
		exec.FailureKind = res.FailureKind.String()
	}
	if err := x.d.db.UpdateExecution(ctx, exec); err != nil {
		aplog.Error("cell %s: update execution record: %v", exec.TaskID, err)
	}
	event := db.ExecutionEvent{Type: "runner.stopped", WorkflowInstanceID: exec.WorkflowInstanceID, StepID: exec.StepID, AttemptID: fmt.Sprint(exec.ID),
		Metadata: map[string]any{"runner": exec.Runner, "model": exec.Model, "status": exec.Status, "failure_kind": exec.FailureKind,
			"duration_ms": exec.DurationMs, "total_tokens": exec.TotalTokens, "cost_usd": exec.CostUSD}}
	if inst, _ := x.d.db.GetWorkflowInstance(ctx, exec.WorkflowInstanceID); inst != nil {
		event.TaskID, event.WorkflowID = inst.TaskID, inst.WorkflowID
	}
	x.d.recordExecutionEvent(ctx, event)
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
		// Removals run after additions so a label in both lists ends up removed.
		if len(hook.RemoveLabels) > 0 {
			if lr, ok := adapter.(source.LabelRemover); ok {
				if err := lr.RemoveLabels(ctx, item, hook.RemoveLabels); err != nil {
					aplog.Error("item %s: remove labels %v: %v", item.ID, hook.RemoveLabels, err)
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

	// Escalation notifications (#201): when a hook adds a watched label (e.g.
	// needs-attention), ping the configured channels — otherwise the escalation
	// is silent and the parked flow waits for someone to happen to look. Every
	// escalation path funnels through this hook applier (engine on_fail /
	// on_complete, task-level hooks, and the failure-cap park), so this is the
	// single choke point. Fired once per label per hook, not per binding.
	var notifCfg *config.NotificationsConfig
	if s.d.cfg != nil {
		notifCfg = s.d.cfg.Notifications
	}
	if matched := matchedEscalationLabels(notifCfg, hook.AddLabels); len(matched) > 0 {
		ev := escalationEvent{
			TaskID:  task.ID,
			CellID:  task.ID,
			Title:   task.Title,
			Summary: s.d.escalationSummary(ctx, task),
		}
		if len(bindings) > 0 {
			b := bindings[0]
			ev.CellID = b.SourceItemID
			ev.Number = b.SourceItemNumber
			ev.URL = b.SourceItemURL
		}
		for _, label := range matched {
			ev.Label = label
			s.d.notifyEscalation(ev)
		}
	}
	return nil
}

// MaterializeChild creates a source sub-issue for a spawned child task under the
// parent's first source item and persists the child's source binding, so the new
// item resolves back to this same child task on the next poll (the binder is keyed
// on source_id+source_item_id). The remote create happens before the local binding
// write; the engine only calls this for a child that has no binding yet, and a
// duplicate binding (a concurrent materialize) is treated as success — together
// these make the publish exactly-once in the common path.
func (s *wfSideEffects) MaterializeChild(ctx context.Context, parent model.InternalTask, parentBindings []model.SourceBinding, child model.InternalTask) error {
	if len(parentBindings) == 0 {
		return nil
	}
	// A sub-issue belongs under exactly one parent item; anchor under the first.
	b := parentBindings[0]
	adapter := s.d.sources[b.SourceID]
	if adapter == nil {
		return fmt.Errorf("materialize child %s: no adapter for source %q", child.ID, b.SourceID)
	}
	creator, ok := adapter.(source.SubIssueCreator)
	if !ok {
		return fmt.Errorf("materialize child %s: source %q does not support sub-issue creation", child.ID, b.SourceID)
	}

	parentItem := sourceItemFromBinding(parent, b)
	childItem := model.SourceItem{
		SourceID:    b.SourceID,
		Title:       child.Title,
		Description: child.Description,
		Labels:      child.Metadata.Labels,
		Type:        "issue",
	}
	created, err := creator.CreateSubIssue(ctx, parentItem, childItem)
	if err != nil {
		return fmt.Errorf("materialize child %s under %s: %w", child.ID, b.SourceItemNumber, err)
	}

	binding := model.SourceBinding{
		TaskID:           child.ID,
		SourceID:         b.SourceID,
		SourceItemID:     created.ID,
		SourceItemURL:    created.URL,
		SourceItemNumber: created.Number,
	}
	if err := s.d.db.SourceBindings().CreateBinding(ctx, &binding); err != nil {
		// A concurrent materialize may have already bound this item (the
		// source_bindings unique index rejects the second insert). Re-resolve: if a
		// binding now exists, the publish succeeded — not an error.
		if existing, ferr := s.d.db.SourceBindings().GetBindingBySourceItem(ctx, b.SourceID, created.ID); ferr == nil && existing != nil {
			return nil
		}
		return fmt.Errorf("materialize child %s: persist binding for %s: %w", child.ID, created.Number, err)
	}
	aplog.Info("materialized child %s as %s sub-issue %s under %s", child.ID, b.SourceID, created.Number, b.SourceItemNumber)
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

// agentIdentityEnv returns the environment that makes an agent subprocess act as
// its own GitHub account: the git author/committer identity derived from the
// agent's source identity (so commits are attributed correctly), plus the
// agent's source token exported as GITHUB_TOKEN / GH_TOKEN (so any `gh` commands
// the agent runs itself authenticate as the agent, not the daemon's inherited
// default account). The `gh` CLI honours both variables; we set both for safety.
//
// These overlay os.Environ() at the call site (Env on the RunRequest, applied
// after the inherited environment in the runner), so the agent's token wins over
// any GITHUB_TOKEN the daemon inherited.
func agentIdentityEnv(agent config.AgentConfig) map[string]string {
	env := map[string]string{}
	if agent.SourceName != "" {
		env["GIT_AUTHOR_NAME"] = agent.SourceName
		env["GIT_COMMITTER_NAME"] = agent.SourceName
	}
	if agent.SourceEmail != "" {
		env["GIT_AUTHOR_EMAIL"] = agent.SourceEmail
		env["GIT_COMMITTER_EMAIL"] = agent.SourceEmail
	}
	if agent.SourceToken != "" {
		env["GITHUB_TOKEN"] = agent.SourceToken
		env["GH_TOKEN"] = agent.SourceToken
	}
	return env
}

// stepEnv composes the environment for one agent step. Layers are applied lowest
// precedence first, so a later layer overrides the same key set by an earlier one:
//
//	agentIdentityEnv(agent)   identity base (git + source_token → GITHUB_TOKEN/GH_TOKEN)
//	  ← agent.Env             agent-scope explicit env
//	    ← wfEnv               workflow-scope explicit env
//	      ← stepEnv           step-scope explicit env (highest precedence)
//
// The precedence is STEP > WORKFLOW > AGENT, above the identity base. An explicit
// env value at any scope can therefore override the identity defaults (e.g. a
// step setting its own GITHUB_TOKEN), which is a deliberate escape hatch.
func stepEnv(agent config.AgentConfig, wfEnv, stepEnv map[string]string) map[string]string {
	env := agentIdentityEnv(agent)
	for k, v := range agent.Env {
		env[k] = v
	}
	for k, v := range wfEnv {
		env[k] = v
	}
	for k, v := range stepEnv {
		env[k] = v
	}
	return env
}

// withMemoryDir exposes the memory root to the agent subprocess as
// APIARY_MEMORY_DIR, so it can read full memory entries directly (recall only
// injects the index). Part of the identity base: an explicit env value already
// present (any scope) wins. A "" dir (memory disabled) leaves env untouched.
func withMemoryDir(env map[string]string, dir string) map[string]string {
	if dir == "" {
		return env
	}
	if _, ok := env["APIARY_MEMORY_DIR"]; !ok {
		env["APIARY_MEMORY_DIR"] = dir
	}
	return env
}

// compile-time interface checks.
var (
	_ workflow.StepExecutor = (*wfStepExecutor)(nil)
	_ workflow.SideEffects  = (*wfSideEffects)(nil)
)
