package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// Store persists workflow instances and step runs. *db.Client satisfies it.
type Store interface {
	CreateWorkflowInstance(ctx context.Context, inst *db.WorkflowInstance) error
	UpdateWorkflowInstanceState(ctx context.Context, id, state string) error
	CreateStepRun(ctx context.Context, sr *db.StepRun) error
	UpdateStepRun(ctx context.Context, sr *db.StepRun) error
}

// bindingLister is the optional capability the engine uses to resolve a task's
// source bindings (for side-effect fan-out and the execution view). *db.Client
// satisfies it; fake stores in tests that omit it simply yield no bindings.
type bindingLister interface {
	ListBindingsByTask(ctx context.Context, taskID string) ([]model.SourceBinding, error)
}

// StepRequest is the input to executing one agent step.
type StepRequest struct {
	InstanceID string
	Cell       model.SourceItem
	Step       config.StepConfig
	Agent      config.AgentConfig
	Model      string // resolved model (step override or agent default)
	MemoryDoc  string // workflow memory document to prepend (empty if memory.read is false)
	// Prompt is the step-level instruction (step.prompt) with any foreach item
	// templates already rendered. The executor folds it into the agent's prompt.
	Prompt string
}

// StepResult is the outcome of executing one agent step.
type StepResult struct {
	Success          bool
	Output           string
	StructuredOutput map[string]any
	Summary          string
	// PublishPayload is the APIARY_PUBLISH text the agent emitted, if any. The
	// engine writes it back to the task's source bindings as a comment. The
	// executor clears it when the step sets publish: off.
	PublishPayload string
	// SpawnRequest is the parsed APIARY_SPAWN request the agent emitted, if any.
	// The engine creates a child task and dispatches the named workflow.
	SpawnRequest *model.SpawnRequest
	Err          error
}

// StepExecutor performs the actual runner invocation for a single agent step.
// The engine owns persistence, memory, and hooks; the executor owns execution.
type StepExecutor interface {
	ExecuteStep(ctx context.Context, req StepRequest) StepResult
}

// SideEffects applies source-facing actions for an InternalTask. Each action
// fans out to the task's source bindings (today a task has at most one binding;
// spawned tasks have none, in which case the action is a no-op). A nil
// SideEffects disables them.
type SideEffects interface {
	// StateLock marks each bound source item in-progress at workflow start.
	StateLock(ctx context.Context, task model.InternalTask, bindings []model.SourceBinding) error
	// PostComment posts a comment (result_comment) on each bound source item.
	PostComment(ctx context.Context, task model.InternalTask, bindings []model.SourceBinding, comment string) error
	// ApplyHook applies an on_complete/on_fail hook (set_state, add_labels) to
	// each bound source item.
	ApplyHook(ctx context.Context, task model.InternalTask, bindings []model.SourceBinding, hook config.OnComplete) error
}

// Engine orchestrates a workflow instance: it persists the instance and its step
// runs, threads workflow memory between steps, and applies side effects. In
// Phase 2 it executes agent steps sequentially in declaration order (single-step
// and linear chains); the DAG executor with splits/foreach arrives in Phase 3.
type Engine struct {
	cfg     *config.Config
	store   Store
	exec    StepExecutor
	side    SideEffects
	mem     MemoryBuilder
	spawner WorkflowSpawner

	now   func() time.Time
	newID func(prefix string) string

	mu     sync.Mutex         // guards parked
	parked map[string]*dagRun // instances suspended at an approval step, by id
}

// Option customizes an Engine.
type Option func(*Engine)

// WithSideEffects sets the source-facing side-effects implementation.
func WithSideEffects(s SideEffects) Option { return func(e *Engine) { e.side = s } }

// WithClock overrides the time source (for deterministic tests).
func WithClock(now func() time.Time) Option { return func(e *Engine) { e.now = now } }

// WithIDGen overrides the ID generator (for deterministic tests).
func WithIDGen(gen func(prefix string) string) Option { return func(e *Engine) { e.newID = gen } }

// WithMemoryBuilder overrides the memory builder.
func WithMemoryBuilder(b MemoryBuilder) Option { return func(e *Engine) { e.mem = b } }

// WithSpawner sets the APIARY_SPAWN handler used to create child tasks.
func WithSpawner(s WorkflowSpawner) Option { return func(e *Engine) { e.spawner = s } }

// NewEngine builds an Engine. cfg, store, and exec are required.
func NewEngine(cfg *config.Config, store Store, exec StepExecutor, opts ...Option) *Engine {
	e := &Engine{
		cfg:    cfg,
		store:  store,
		exec:   exec,
		now:    time.Now,
		parked: map[string]*dagRun{},
	}
	for _, opt := range opts {
		opt(e)
	}
	if e.newID == nil {
		e.newID = defaultIDGen(e.now)
	}
	return e
}

// RunInstance executes a workflow for an InternalTask. It returns the created
// instance ID and whether the instance completed successfully. An instance that
// suspends at an approval step returns success=false with no error — it is parked
// in state approval_waiting until ResolveApproval (driven by the polling loop)
// resumes it. Errors creating the instance are returned; per-step failures are
// recorded and reflected in the final instance state and the success flag, not
// returned.
func (e *Engine) RunInstance(ctx context.Context, wf config.WorkflowConfig, task model.InternalTask) (instanceID string, success bool, err error) {
	bindings := e.bindingsFor(ctx, task.ID)
	cell := sourceItemView(task, bindings)

	instID := e.newID("wf")
	inst := &db.WorkflowInstance{
		ID:         instID,
		WorkflowID: wf.ID,
		TaskID:     task.ID,
		CellID:     cell.ID,
		SourceID:   cell.SourceID,
		State:      db.InstanceStateRunning,
		CreatedAt:  e.now(),
	}
	if err := e.store.CreateWorkflowInstance(ctx, inst); err != nil {
		return "", false, fmt.Errorf("create workflow instance: %w", err)
	}

	// state_lock fires once at workflow start (not per step).
	if e.cfg.Settings.StateLock && e.side != nil {
		_ = e.side.StateLock(ctx, task, bindings)
	}

	r := e.initDAG(instID, wf, task, bindings, nil, 0)
	outcome := e.driveDAG(ctx, r)
	return instID, e.settle(ctx, r, outcome), nil
}

// bindingsFor resolves a task's source bindings via the Store when it supports
// it (the production *db.Client). Returns nil for fake stores, spawned tasks
// (no bindings), or on error — all of which leave side effects as no-ops.
func (e *Engine) bindingsFor(ctx context.Context, taskID string) []model.SourceBinding {
	if taskID == "" {
		return nil
	}
	bl, ok := e.store.(bindingLister)
	if !ok {
		return nil
	}
	bindings, err := bl.ListBindingsByTask(ctx, taskID)
	if err != nil {
		return nil
	}
	return bindings
}

// sourceItemView projects an InternalTask (plus its primary source binding, if
// any) into the SourceItem shape the executor, memory builder, and expression
// engine consume. The primary binding supplies source identity (id, number, url);
// the task supplies the live content and routing attributes. For a binding-less
// task (spawned, or no-DB transient) the task's own ID and metadata stand in.
func sourceItemView(task model.InternalTask, bindings []model.SourceBinding) model.SourceItem {
	item := model.SourceItem{
		ID:          task.ID,
		SourceID:    task.Metadata.Source,
		Title:       task.Title,
		Description: task.Description,
		Labels:      task.Metadata.Labels,
		Type:        task.Metadata.Type,
		Priority:    task.Metadata.Priority,
		State:       task.Metadata.State,
	}
	if len(bindings) > 0 {
		b := bindings[0]
		item.ID = b.SourceItemID
		item.SourceID = b.SourceID
		item.Number = b.SourceItemNumber
		item.URL = b.SourceItemURL
	}
	return item
}

// settle persists the terminal state of a run and applies completion hooks, or
// parks the run when it suspended at an approval step. It returns whether the
// instance completed successfully (false for failed or waiting).
func (e *Engine) settle(ctx context.Context, r *dagRun, outcome dagOutcome) bool {
	if outcome == outcomeWaiting {
		_ = e.store.UpdateWorkflowInstanceState(ctx, r.instID, db.InstanceStateApprovalWaiting)
		e.mu.Lock()
		e.parked[r.instID] = r
		e.mu.Unlock()
		return false
	}

	failed := outcome == outcomeFailed
	finalState := db.InstanceStateDone
	if failed {
		finalState = db.InstanceStateFailed
	}
	_ = e.store.UpdateWorkflowInstanceState(ctx, r.instID, finalState)
	e.mu.Lock()
	delete(e.parked, r.instID)
	e.mu.Unlock()
	e.applyCompletion(ctx, r, failed)
	return !failed
}

// runStep executes one agent step, persisting its step run, and returns the
// result. task and bindings are immutable snapshots threaded through so the
// publish write-back can reach the task's source bindings; they are passed by
// value (not via dagRun) so the function stays safe to call from the parallel
// and foreach worker goroutines.
func (e *Engine) runStep(ctx context.Context, instID string, step config.StepConfig, cell model.SourceItem, task model.InternalTask, bindings []model.SourceBinding, memSteps []MemoryStep) StepResult {
	started := e.now()
	sr := &db.StepRun{
		ID:                 e.newID("sr"),
		WorkflowInstanceID: instID,
		StepID:             step.ID,
		AgentID:            step.Agent,
		State:              db.StepStateRunning,
		StartedAt:          &started,
	}
	_ = e.store.CreateStepRun(ctx, sr)

	agent := e.findAgent(step.Agent)
	resolvedModel := step.Model
	if resolvedModel == "" && agent != nil {
		resolvedModel = agent.Model
	}

	memDoc := ""
	if step.MemoryReadEnabled() {
		memDoc = e.mem.Build(cell, memSteps)
	}

	var ag config.AgentConfig
	if agent != nil {
		ag = *agent
	}
	res := e.exec.ExecuteStep(ctx, StepRequest{
		InstanceID: instID,
		Cell:       cell,
		Step:       step,
		Agent:      ag,
		Model:      resolvedModel,
		MemoryDoc:  memDoc,
		Prompt:     step.Prompt,
	})

	finished := e.now()
	sr.FinishedAt = &finished
	sr.Output = res.Output
	sr.Summary = res.Summary
	if res.StructuredOutput != nil {
		if data, err := json.Marshal(res.StructuredOutput); err == nil {
			sr.StructuredOutput = string(data)
		}
	}
	// Spawn handling runs before the pass/fail decision so a spawn failure (or an
	// await on a failed child) fails the step.
	e.spawnStep(ctx, task, step, &res, sr)
	if res.Success {
		sr.State = db.StepStatePassed
	} else {
		sr.State = db.StepStateFailed
	}
	e.publishStep(ctx, task, bindings, res, sr)
	_ = e.store.UpdateStepRun(ctx, sr)
	return res
}

// spawnStep handles an agent-emitted APIARY_SPAWN request: it creates a child
// InternalTask via the spawner, dispatches the named workflow, and records the
// child id on the step run. By default (spawn: auto) it is fire-and-forget; with
// spawn: await it blocks until the child reaches a terminal state and fails the
// step if the child failed. A spawn request is only honored when the agent step
// itself succeeded; a missing spawner, an unknown workflow, or a create failure
// fails the step (never a silent no-op).
func (e *Engine) spawnStep(ctx context.Context, task model.InternalTask, step config.StepConfig, res *StepResult, sr *db.StepRun) {
	if res.SpawnRequest == nil || !res.Success {
		return
	}
	if e.spawner == nil {
		res.Success = false
		res.Err = fmt.Errorf("step %q requested APIARY_SPAWN but no spawner is configured", step.ID)
		return
	}

	req := *res.SpawnRequest
	req.ParentTaskID = task.ID
	child, err := e.spawner.Spawn(ctx, req)
	if err != nil {
		res.Success = false
		res.Err = fmt.Errorf("spawn workflow %q: %w", req.WorkflowID, err)
		return
	}
	sr.SpawnedTaskID = child.ID

	if step.Spawn == config.SpawnAwait {
		ok, werr := e.spawner.Await(ctx, child.ID)
		if werr != nil {
			res.Success = false
			res.Err = fmt.Errorf("spawn await child %s: %w", child.ID, werr)
			return
		}
		if !ok {
			res.Success = false
			res.Err = fmt.Errorf("spawned task %s failed", child.ID)
		}
	}
}

// publishStep writes an agent-emitted APIARY_PUBLISH payload back to the task's
// source bindings and records the outcome on the step run. The executor has
// already cleared the payload when the step set publish: off, so a non-empty
// payload here means write-back was requested. A task with no bindings (e.g. a
// spawned task) is silently skipped.
func (e *Engine) publishStep(ctx context.Context, task model.InternalTask, bindings []model.SourceBinding, res StepResult, sr *db.StepRun) {
	if res.PublishPayload == "" {
		return
	}
	sr.PublishPayload = res.PublishPayload
	if e.side == nil || len(bindings) == 0 {
		sr.PublishState = db.PublishStateSkipped
		return
	}
	if err := e.side.PostComment(ctx, task, bindings, res.PublishPayload); err != nil {
		sr.PublishState = db.PublishStateFailed
		return
	}
	sr.PublishState = db.PublishStateSent
}

// applyCompletion applies the on_complete/on_fail hook and posts the on_complete
// result comment (the final memory document) to the task's source bindings.
func (e *Engine) applyCompletion(ctx context.Context, r *dagRun, failed bool) {
	if e.side == nil {
		return
	}
	wf := r.wf

	if !failed && e.resultCommentMode(wf) == config.ResultCommentOnComplete {
		doc := e.mem.Build(r.cell, r.memSteps())
		_ = e.side.PostComment(ctx, r.task, r.bindings, finalComment(wf, false, doc))
	}

	var hook *config.OnComplete
	if failed {
		hook = wf.OnFail
	} else {
		hook = wf.OnComplete
	}
	if hook != nil {
		_ = e.side.ApplyHook(ctx, r.task, r.bindings, *hook)
	}
}

// resultCommentMode resolves the effective result-comment mode for a workflow,
// honoring the workflow override and falling back to the global setting.
func (e *Engine) resultCommentMode(wf config.WorkflowConfig) string {
	if wf.ResultComment != "" {
		return wf.ResultComment
	}
	if e.cfg.Settings.ResultComment {
		return config.ResultCommentOnComplete
	}
	return config.ResultCommentOff
}

// concurrencyLimit returns the effective global concurrency cap from config.
// If not set, defaults to 1 (sequential — preserves existing behaviour).
func (e *Engine) concurrencyLimit() int {
	if e.cfg.Settings.Concurrency > 0 {
		return e.cfg.Settings.Concurrency
	}
	return 1
}

// findAgent returns the agent config by ID, or nil.
func (e *Engine) findAgent(id string) *config.AgentConfig {
	for i := range e.cfg.Agents {
		if e.cfg.Agents[i].ID == id {
			return &e.cfg.Agents[i]
		}
	}
	return nil
}


func perStepComment(step config.StepConfig, res StepResult) string {
	status := "✓ passed"
	if !res.Success {
		status = "✗ failed"
	}
	return fmt.Sprintf("**Step: %s (%s) — %s**\n\n%s", step.ID, step.Agent, status, res.Output)
}

func finalComment(wf config.WorkflowConfig, failed bool, memoryDoc string) string {
	status := "✓ Done"
	if failed {
		status = "✗ Failed"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Workflow: %s — %s**\n\n", wf.ID, status)
	b.WriteString(memoryDoc)
	return b.String()
}

// defaultIDGen returns an ID generator that combines the clock with an atomic
// counter so IDs are unique even within the same nanosecond.
func defaultIDGen(now func() time.Time) func(string) string {
	var ctr uint64
	return func(prefix string) string {
		n := atomic.AddUint64(&ctr, 1)
		return fmt.Sprintf("%s_%d_%d", prefix, now().UnixNano(), n)
	}
}
