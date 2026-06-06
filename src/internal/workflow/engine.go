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
	Err              error
}

// StepExecutor performs the actual runner invocation for a single agent step.
// The engine owns persistence, memory, and hooks; the executor owns execution.
type StepExecutor interface {
	ExecuteStep(ctx context.Context, req StepRequest) StepResult
}

// SideEffects applies source-facing actions. A nil SideEffects disables them.
type SideEffects interface {
	// StateLock marks the task in-progress at workflow start.
	StateLock(ctx context.Context, cell model.SourceItem) error
	// PostComment posts a comment (result_comment) on the task.
	PostComment(ctx context.Context, cell model.SourceItem, comment string) error
	// ApplyHook applies an on_complete/on_fail hook (set_state, add_labels).
	ApplyHook(ctx context.Context, cell model.SourceItem, hook config.OnComplete) error
}

// Engine orchestrates a workflow instance: it persists the instance and its step
// runs, threads workflow memory between steps, and applies side effects. In
// Phase 2 it executes agent steps sequentially in declaration order (single-step
// and linear chains); the DAG executor with splits/foreach arrives in Phase 3.
type Engine struct {
	cfg   *config.Config
	store Store
	exec  StepExecutor
	side  SideEffects
	mem   MemoryBuilder

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

// RunInstance executes a workflow for a cell. It returns the created instance ID
// and whether the instance completed successfully. An instance that suspends at
// an approval step returns success=false with no error — it is parked in state
// approval_waiting until ResolveApproval (driven by the polling loop) resumes it.
// Errors creating the instance are returned; per-step failures are recorded and
// reflected in the final instance state and the success flag, not returned.
func (e *Engine) RunInstance(ctx context.Context, wf config.WorkflowConfig, cell model.SourceItem) (instanceID string, success bool, err error) {
	instID := e.newID("wf")
	inst := &db.WorkflowInstance{
		ID:         instID,
		WorkflowID: wf.ID,
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
		_ = e.side.StateLock(ctx, cell)
	}

	r := e.initDAG(instID, wf, cell, nil, 0)
	outcome := e.driveDAG(ctx, r)
	return instID, e.settle(ctx, r, outcome), nil
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
	e.applyCompletion(ctx, r.wf, r.cell, failed, r.memSteps())
	return !failed
}

// runStep executes one agent step, persisting its step run, and returns the result.
func (e *Engine) runStep(ctx context.Context, instID string, step config.StepConfig, cell model.SourceItem, memSteps []MemoryStep) StepResult {
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
	if res.Success {
		sr.State = db.StepStatePassed
	} else {
		sr.State = db.StepStateFailed
	}
	_ = e.store.UpdateStepRun(ctx, sr)
	return res
}

// applyCompletion applies the on_complete/on_fail hook and posts the on_complete
// result comment (the final memory document).
func (e *Engine) applyCompletion(ctx context.Context, wf config.WorkflowConfig, cell model.SourceItem, failed bool, memSteps []MemoryStep) {
	if e.side == nil {
		return
	}

	if !failed && e.resultCommentMode(wf) == config.ResultCommentOnComplete {
		doc := e.mem.Build(cell, memSteps)
		_ = e.side.PostComment(ctx, cell, finalComment(wf, false, doc))
	}

	var hook *config.OnComplete
	if failed {
		hook = wf.OnFail
	} else {
		hook = wf.OnComplete
	}
	if hook != nil {
		_ = e.side.ApplyHook(ctx, cell, *hook)
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
