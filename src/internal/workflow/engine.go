package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/memory"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

// Store persists workflow instances and step runs. *db.Client satisfies it.
type Store interface {
	CreateWorkflowInstance(ctx context.Context, inst *db.WorkflowInstance) error
	UpdateWorkflowInstanceState(ctx context.Context, id, state string) error
	CreateStepRun(ctx context.Context, sr *db.StepRun) error
	UpdateStepRun(ctx context.Context, sr *db.StepRun) error
}

// workflowSnapshotStore is implemented by persistent stores that retain the
// exact workflow definition used by each instance for reproducible replay.
type workflowSnapshotStore interface {
	PutWorkflowSnapshot(ctx context.Context, instanceID, workflowJSON string) error
}

// bindingLister is the optional capability the engine uses to resolve a task's
// source bindings (for side-effect fan-out and the execution view). *db.Client
// satisfies it; fake stores in tests that omit it simply yield no bindings.
type bindingLister interface {
	ListBindingsByTask(ctx context.Context, taskID string) ([]model.SourceBinding, error)
}

// ciPollRecorder is the optional capability the engine uses to persist each CI
// poll of a wait_for step (count, timestamp, returned status). *db.Client
// satisfies it; fake stores in tests that omit it simply record nothing.
type ciPollRecorder interface {
	RecordCIPollCheck(ctx context.Context, p *db.CIPollCheck) error
}

type executionEventRecorder interface {
	RecordExecutionEvent(context.Context, *db.ExecutionEvent) error
}

type approvalRequestStore interface {
	CreateApprovalRequest(context.Context, *db.ApprovalRequest) error
	GetApprovalByInstance(context.Context, string) (*db.ApprovalRequest, error)
	MarkApprovalTimedOut(context.Context, string) (bool, error)
	MarkApprovalReminded(context.Context, string) (bool, error)
	EscalateApproval(context.Context, string) (bool, error)
	ResolveApprovalRequest(context.Context, string, db.ApprovalResponse) (*db.ApprovalRequest, bool, error)
}

func (e *Engine) recordExecutionEvent(ctx context.Context, r *dagRun, eventType string, metadata map[string]any) {
	recorder, ok := e.store.(executionEventRecorder)
	if !ok || r == nil {
		return
	}
	_ = recorder.RecordExecutionEvent(ctx, &db.ExecutionEvent{Type: eventType, TaskID: r.task.ID, WorkflowID: r.wf.ID,
		WorkflowInstanceID: r.instID, StepID: r.waitingStep, Metadata: metadata})
}

// recordCIPoll persists one CI poll result for a wait_for step when the store
// supports it. Best-effort: a recording failure never affects the poll outcome.
func (e *Engine) recordCIPoll(ctx context.Context, instID, stepID, status, url, detail string) {
	if instID == "" {
		return
	}
	rec, ok := e.store.(ciPollRecorder)
	if !ok {
		return
	}
	if err := rec.RecordCIPollCheck(ctx, &db.CIPollCheck{
		WorkflowInstanceID: instID,
		StepID:             stepID,
		Status:             status,
		PRURL:              url,
		Detail:             detail,
	}); err != nil {
		aplog.Debug("wait_for step %q: record CI poll: %v", stepID, err)
	}
}

// TaskTracker is the optional capability the engine uses to apply the top-level
// tasks: completion hook. When an instance reaches a terminal state the engine
// decrements the task's outstanding-workflow counter; when it hits zero it flips
// the task's lifecycle state and (if a tasks: block is configured) applies the
// aggregate on_complete/on_fail hook. A nil tracker disables all of this — the
// counter is left untouched and the task keeps its running state, matching the
// pre-Phase-8 behaviour. The daemon's *db.Client-backed wiring satisfies it.
type TaskTracker interface {
	// DecrementOutstanding subtracts one from the task's outstanding-workflow
	// counter and returns the new count. Must be atomic so concurrent sibling
	// instances each see a distinct post-decrement value (exactly one sees zero).
	DecrementOutstanding(ctx context.Context, taskID string) (int, error)
	// HasFailedInstance reports whether any of the task's workflow instances
	// failed, used to pick on_complete vs on_fail once the last one settles.
	HasFailedInstance(ctx context.Context, taskID string) (bool, error)
	// SetTaskState transitions the InternalTask to a terminal lifecycle state.
	SetTaskState(ctx context.Context, taskID string, state model.TaskState) error
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
	// WorkflowEnv is the workflow-scope environment overlay (wf.Env) for the
	// instance this step belongs to. The executor merges it between agent.env and
	// step.env when building the subprocess environment.
	WorkflowEnv map[string]string
}

// StepResult is the outcome of executing one agent step.
type StepResult struct {
	Success          bool
	Output           string
	StructuredOutput map[string]any
	Summary          string
	// Pending is set only by wait_for steps to signal "no terminal result yet —
	// park the instance and re-check on a later poll cycle" (as opposed to a
	// pass or a fail). The scheduler suspends the run at the wait_for step instead
	// of marking it passed/failed. Ignored for all other step types.
	Pending bool
	// Conflict marks a failure caused specifically by a merge conflict on the PR
	// (set only by wait_for/ci steps). The scheduler routes it via the step's
	// on_conflict edge when one is declared; otherwise it is an ordinary failure.
	Conflict bool
	// PublishPayload is the APIARY_PUBLISH text the agent emitted, if any. The
	// engine writes it back to the task's source bindings as a comment. The
	// executor clears it when the step sets publish: off.
	PublishPayload string
	// SpawnRequest is the parsed APIARY_SPAWN request the agent emitted as a single
	// object, if any. The engine creates a child task and dispatches the named
	// workflow.
	SpawnRequest *model.SpawnRequest
	// SpawnRequests is the parsed APIARY_SPAWN request list when the agent emitted a
	// JSON array — one step fanning out into several children (e.g. a spec
	// decomposed into sub-issues). The engine handles it uniformly with SpawnRequest.
	SpawnRequests []model.SpawnRequest
	// MemorizeRequests are the agent's APIARY_MEMORIZE requests. The engine
	// persists each to the memory store (task notes or global entries) when a
	// store is wired and the step has not set memory.memorize: off (the executor
	// clears them in that case, mirroring publish: off).
	MemorizeRequests []model.MemorizeRequest
	// MemorizeError is set when an APIARY_MEMORIZE block was present but
	// malformed. Unlike SpawnError it is only ever surfaced as a warning — a
	// failed memorize never fails the step.
	MemorizeError error
	// Usage is the token/cost rollup for the step, summed across the step's
	// failover attempts (each attempt is also its own task_executions row). Nil
	// when the runner reported no usage. The engine persists it onto the step run.
	Usage *model.Usage
	// InputPrompt is the composed prompt of the final (winning) attempt, persisted
	// onto the step run for cost auditing and replay.
	InputPrompt string
	Err         error
}

// StepExecutor performs the actual runner invocation for a single agent step.
// The engine owns persistence, memory, and hooks; the executor owns execution.
type StepExecutor interface {
	ExecuteStep(ctx context.Context, req StepRequest) StepResult
}

// MemoryStore is the persistent agent memory the engine writes APIARY_MEMORIZE
// requests to and reads recall sections from. *memory.Store satisfies it; nil
// disables persistent memory entirely (the per-instance memory document is
// unaffected).
type MemoryStore interface {
	// UpsertGlobal writes one durable daemon-wide fact (same name = update).
	UpsertGlobal(e memory.Entry) error
	// AppendTaskNote appends one working note to a task's notes file.
	AppendTaskNote(taskID string, n memory.Note) error
	// RenderRecall renders the prompt sections for the given task lineage (self
	// first) and tiers, bounded by budget. "" when there is nothing to recall.
	RenderRecall(taskIDs []string, tiers []string, budget int) string
}

// ancestorLister is the optional capability the engine uses to resolve a task's
// ancestor chain for task-memory recall (spawned children inherit their
// ancestors' notes). *db.Client satisfies it; fake stores that omit it limit
// recall to the task's own notes.
type ancestorLister interface {
	GetTaskAncestors(ctx context.Context, id string) ([]model.InternalTask, error)
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
	// ApplyHook applies an on_complete/on_fail hook (set_state, add_labels,
	// remove_labels) to each bound source item.
	ApplyHook(ctx context.Context, task model.InternalTask, bindings []model.SourceBinding, hook config.OnComplete) error
	// MaterializeChild creates a source sub-issue for a spawned child task under the
	// parent's source item and persists the child's source binding, so the child
	// becomes a first-class pollable work item exactly once. parentBindings are the
	// spawning task's bindings; the child is anchored under the first one. It is an
	// error when the source does not support sub-issue creation; a duplicate binding
	// (a concurrent materialize) is treated as success.
	MaterializeChild(ctx context.Context, parent model.InternalTask, parentBindings []model.SourceBinding, child model.InternalTask) error
}

// ApprovalProvider delivers a provider-neutral approval request to an external
// transport. Slack, Teams, email, and custom webhooks can implement this without
// coupling transport behavior to the workflow scheduler.
type ApprovalProvider interface {
	RequestApproval(context.Context, *db.ApprovalRequest) error
}

// ApprovalLifecycleProvider optionally delivers reminders and escalations.
type ApprovalLifecycleProvider interface {
	ApprovalProvider
	RemindApproval(context.Context, *db.ApprovalRequest) error
	EscalateApproval(context.Context, *db.ApprovalRequest, []string) error
}

// Engine orchestrates a workflow instance: it persists the instance and its step
// runs, threads workflow memory between steps, and applies side effects. In
// Phase 2 it executes agent steps sequentially in declaration order (single-step
// and linear chains); the DAG executor with splits/foreach arrives in Phase 3.
// CIStatusChecker is called by wait_for steps to check the current CI status of a PR/branch.
// It returns a CIStatus or an error if the check fails (transient or permanent).
type CIStatusChecker func(ctx context.Context, sourceID, sourceItemID string) (source.CIStatus, error)

// DependencyChecker is called by wait_for steps with kind "dependency" to list
// the task's upstream blockers. linkType is the step's blocker_link_type
// (empty = the source's default blocking relation). It returns the blockers or
// an error if the lookup fails (transient or permanent).
type DependencyChecker func(ctx context.Context, sourceID, sourceItemID, linkType string) ([]source.BlockerRef, error)

type Engine struct {
	cfg               *config.Config
	store             Store
	exec              StepExecutor
	side              SideEffects
	mem               MemoryBuilder
	memStore          MemoryStore
	spawner           WorkflowSpawner
	tracker           TaskTracker
	ciChecker         CIStatusChecker
	depChecker        DependencyChecker
	approvalProviders []ApprovalProvider

	now   func() time.Time
	newID func(prefix string) string

	mu     sync.Mutex         // guards parked
	parked map[string]*dagRun // instances suspended at an approval step, by id

	// instWF maps a live instance ID to its workflow ID, for memory provenance in
	// code paths (parallel / foreach workers) that do not carry the dagRun.
	// Entries are added in initDAG and removed when the instance settles terminal.
	instWF sync.Map
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

// WithMemoryStore wires the persistent agent memory store (APIARY_MEMORIZE
// write path + recall injection). Nil disables persistent memory.
func WithMemoryStore(s MemoryStore) Option { return func(e *Engine) { e.memStore = s } }

// WithSpawner sets the APIARY_SPAWN handler used to create child tasks.
func WithSpawner(s WorkflowSpawner) Option { return func(e *Engine) { e.spawner = s } }

// WithTaskTracker sets the task completion tracker used to decrement the
// outstanding-workflow counter and apply the top-level tasks: hook.
func WithTaskTracker(t TaskTracker) Option { return func(e *Engine) { e.tracker = t } }

// WithCIStatusChecker sets the CI status checker used by wait_for steps.
func WithCIStatusChecker(checker CIStatusChecker) Option {
	return func(e *Engine) { e.ciChecker = checker }
}

// WithDependencyChecker sets the blocker lister used by wait_for steps with
// kind "dependency".
func WithDependencyChecker(checker DependencyChecker) Option {
	return func(e *Engine) { e.depChecker = checker }
}

// WithApprovalProvider registers an external approval request transport.
func WithApprovalProvider(provider ApprovalProvider) Option {
	return func(e *Engine) {
		if provider != nil {
			e.approvalProviders = append(e.approvalProviders, provider)
		}
	}
}

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

// NewInstanceID reserves a process-unique workflow instance identifier for
// callers that must return a replay ID before asynchronous execution starts.
func (e *Engine) NewInstanceID() string { return e.newID("wf") }

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
	if err := e.persistWorkflowSnapshot(ctx, instID, wf); err != nil {
		return "", false, err
	}

	// state_lock fires once at workflow start (not per step).
	if e.cfg.Settings.StateLock && e.side != nil {
		_ = e.side.StateLock(ctx, task, bindings)
	}

	r := e.initDAG(instID, wf, task, bindings, nil, 0)
	outcome := e.driveDAG(ctx, r)
	return instID, e.settle(ctx, r, outcome), nil
}

func (e *Engine) persistWorkflowSnapshot(ctx context.Context, instanceID string, wf config.WorkflowConfig) error {
	store, ok := e.store.(workflowSnapshotStore)
	if !ok {
		return nil
	}
	snapshot := wf
	// Environment overlays may contain secrets after config expansion. Snapshots
	// retain workflow structure and prompts but resolve env overlays from the
	// current configuration when replayed.
	snapshot.Env = nil
	snapshot.Steps = append([]config.StepConfig(nil), wf.Steps...)
	for i := range snapshot.Steps {
		snapshot.Steps[i].Env = nil
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal workflow snapshot: %w", err)
	}
	if err := store.PutWorkflowSnapshot(ctx, instanceID, string(b)); err != nil {
		return fmt.Errorf("persist workflow snapshot: %w", err)
	}
	return nil
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
		// A run suspends at either an approval step or a wait_for step; persist the
		// matching waiting state so the right rehydration path picks it up after a
		// restart (approval_waiting → rehydrateParkedApprovals, waiting →
		// rehydrateParkedWaits).
		waitState := db.InstanceStateApprovalWaiting
		if r.byID[r.waitingStep].StepType() == config.StepTypeWaitFor {
			waitState = db.InstanceStateWaiting
		}
		_ = e.store.UpdateWorkflowInstanceState(ctx, r.instID, waitState)
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
	e.instWF.Delete(r.instID)
	e.applyCompletion(ctx, r, failed)
	e.completeTask(ctx, r, failed)
	return !failed
}

// completeTask decrements the task's outstanding-workflow counter now that this
// instance has reached a terminal state and, when it was the last outstanding
// workflow, transitions the task's lifecycle state and applies the top-level
// tasks: on_complete/on_fail hook to every source binding. It is a no-op when no
// TaskTracker is wired (the pre-Phase-8 default) or for a binding-less transient
// task with no id. The aggregate outcome is failed if THIS instance failed or any
// sibling instance from the task's current dispatch generation did
// (HasFailedInstance also covers this instance, whose terminal state was just
// persisted above). Failures from earlier generations don't count: a re-dispatch
// or escalation reopens the task under a new generation, so its success settles
// the task as done even though older failed instances remain in the DB.
func (e *Engine) completeTask(ctx context.Context, r *dagRun, failed bool) {
	if e.tracker == nil || r.task.ID == "" {
		return
	}
	remaining, err := e.tracker.DecrementOutstanding(ctx, r.task.ID)
	if err != nil {
		aplog.Error("task %s: decrement outstanding: %v", r.task.ID, err)
		return
	}
	if remaining > 0 {
		return
	}

	anyFailed := failed
	if !anyFailed {
		if hf, err := e.tracker.HasFailedInstance(ctx, r.task.ID); err != nil {
			aplog.Error("task %s: check failed instances: %v", r.task.ID, err)
		} else {
			anyFailed = hf
		}
	}

	finalState := model.TaskStateDone
	if anyFailed {
		finalState = model.TaskStateFailed
	}
	if err := e.tracker.SetTaskState(ctx, r.task.ID, finalState); err != nil {
		aplog.Error("task %s: set state %s: %v", r.task.ID, finalState, err)
	}
	eventType := "task.completed"
	if anyFailed {
		eventType = "task.escalated"
	}
	e.recordExecutionEvent(ctx, r, eventType, map[string]any{"state": finalState})

	if e.cfg.Tasks == nil || e.side == nil {
		return
	}
	hook := e.cfg.Tasks.OnComplete
	if anyFailed {
		hook = e.cfg.Tasks.OnFail
	}
	if hook != nil {
		_ = e.side.ApplyHook(ctx, r.task, r.bindings, *hook)
	}
}

// runStep executes one agent step, persisting its step run, and returns the
// result. task and bindings are immutable snapshots threaded through so the
// publish write-back can reach the task's source bindings; they are passed by
// value (not via dagRun) so the function stays safe to call from the parallel
// and foreach worker goroutines.
func (e *Engine) runStep(ctx context.Context, instID string, step config.StepConfig, cell model.SourceItem, task model.InternalTask, bindings []model.SourceBinding, memSteps []MemoryStep, wfEnv map[string]string) StepResult {
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
		// Persistent recall ([Long-term Memory] + [Task Memory]) rides ahead of the
		// per-instance document on the same SystemPrepend channel. memory.read: false
		// suppresses everything; memory.recall filters tiers.
		if recall := e.renderRecall(ctx, task, step); recall != "" {
			memDoc = recall + "\n" + memDoc
		}
	}

	var ag config.AgentConfig
	if agent != nil {
		ag = *agent
	}
	res := e.exec.ExecuteStep(ctx, StepRequest{
		InstanceID:  instID,
		Cell:        cell,
		Step:        step,
		Agent:       ag,
		Model:       resolvedModel,
		MemoryDoc:   memDoc,
		Prompt:      step.Prompt,
		WorkflowEnv: wfEnv,
	})

	finished := e.now()
	sr.FinishedAt = &finished
	sr.Output = res.Output
	sr.Summary = res.Summary
	sr.InputPrompt = res.InputPrompt
	if res.Usage != nil {
		sr.InputTokens = res.Usage.InputTokens
		sr.OutputTokens = res.Usage.OutputTokens
		sr.TotalTokens = res.Usage.TotalTokens
		sr.CacheCreationTokens = res.Usage.CacheCreationTokens
		sr.CacheReadTokens = res.Usage.CacheReadTokens
		sr.NumTurns = res.Usage.NumTurns
		sr.NumToolCalls = res.Usage.NumToolCalls
		sr.CostUSD = res.Usage.CostUSD
	}
	if res.StructuredOutput != nil {
		if data, err := json.Marshal(res.StructuredOutput); err == nil {
			sr.StructuredOutput = string(data)
		}
	}
	// Spawn handling runs before the pass/fail decision so a spawn failure (or an
	// await on a failed child) fails the step.
	e.spawnStep(ctx, task, step, bindings, &res, sr)
	if res.Success {
		sr.State = db.StepStatePassed
	} else {
		sr.State = db.StepStateFailed
	}
	// Memorize runs before publish so knowledge is persisted even when the
	// source write-back fails; neither outcome affects the step state.
	e.memorizeStep(instID, task, step, res)
	e.publishStep(ctx, task, bindings, res, sr)
	_ = e.store.UpdateStepRun(ctx, sr)
	return res
}

// secretPattern returns the name of the first common credential pattern found
// in s, or "" when none match. Heuristic by design (see memorizeStep).
func secretPattern(s string) string {
	for _, p := range []struct{ name, marker string }{
		{"GitHub token", "ghp_"},
		{"GitHub fine-grained token", "github_pat_"},
		{"Slack token", "xoxb-"},
		{"Slack user token", "xoxp-"},
		{"AWS access key", "AKIA"},
		{"private key", "-----BEGIN"},
	} {
		if strings.Contains(s, p.marker) {
			return p.name
		}
	}
	return ""
}

// renderRecall renders the persistent-memory sections for one step's prompt:
// the global index and the task notes of the task plus its ancestors (so
// spawned children inherit working context). Best-effort — a store or lineage
// failure degrades to no recall, never blocks the step.
func (e *Engine) renderRecall(ctx context.Context, task model.InternalTask, step config.StepConfig) string {
	if e.memStore == nil {
		return ""
	}
	lineage := []string{}
	if task.ID != "" {
		lineage = append(lineage, task.ID)
		if al, ok := e.store.(ancestorLister); ok {
			// GetTaskAncestors returns root first, self last; recall wants nearest
			// context first (self, parent, grandparent, …) so reverse, skipping self.
			if ancestors, err := al.GetTaskAncestors(ctx, task.ID); err == nil {
				for i := len(ancestors) - 1; i >= 0; i-- {
					if ancestors[i].ID != task.ID {
						lineage = append(lineage, ancestors[i].ID)
					}
				}
			}
		}
	}
	return e.memStore.RenderRecall(lineage, step.MemoryRecallTiers(), e.cfg.Settings.Memory.MaxInjectChars)
}

// memorizeStep persists the step's APIARY_MEMORIZE requests: global-scope
// requests upsert durable entries, task-scope requests (the default) append
// working notes to the task. Every failure — malformed block, validation,
// store error — is a warning only; a memorize can never fail a step. The
// executor has already dropped the requests when the step set
// memory.memorize: off.
func (e *Engine) memorizeStep(instID string, task model.InternalTask, step config.StepConfig, res StepResult) {
	if res.MemorizeError != nil {
		aplog.Warn("step %q: %v (block ignored)", step.ID, res.MemorizeError)
	}
	if e.memStore == nil || len(res.MemorizeRequests) == 0 {
		if len(res.MemorizeRequests) > 0 {
			aplog.Debug("step %q: %d APIARY_MEMORIZE request(s) dropped (memory disabled)", step.ID, len(res.MemorizeRequests))
		}
		return
	}

	wfID := ""
	if v, ok := e.instWF.Load(instID); ok {
		wfID, _ = v.(string)
	}
	for _, req := range res.MemorizeRequests {
		// Best-effort guard: memory files are plain text read by every agent, so
		// flag content that looks like a credential. Warn-only — patterns are
		// heuristic and a false positive must not lose a legitimate fact.
		if pat := secretPattern(req.Content); pat != "" {
			aplog.Warn("step %q: APIARY_MEMORIZE content matches a credential pattern (%s) — memory is not a secret store", step.ID, pat)
		}
		scope := req.Scope
		if scope == "" {
			scope = model.MemorizeScopeTask
		}
		var err error
		switch scope {
		case model.MemorizeScopeGlobal:
			err = e.memStore.UpsertGlobal(memory.Entry{
				Name:        req.Name,
				Description: req.Description,
				Content:     req.Content,
				Agent:       step.Agent,
				Task:        task.ID,
				Workflow:    wfID,
			})
		case model.MemorizeScopeTask:
			if task.ID == "" {
				err = fmt.Errorf("task-scope memorize on a transient task with no id")
				break
			}
			err = e.memStore.AppendTaskNote(task.ID, memory.Note{
				Content:  req.Content,
				Agent:    step.Agent,
				Workflow: wfID,
				Step:     step.ID,
				At:       e.now(),
			})
		default:
			err = fmt.Errorf("unknown scope %q (want task|global)", scope)
		}
		if err != nil {
			aplog.Warn("step %q: APIARY_MEMORIZE: %v", step.ID, err)
		}
	}
}

// spawnStep handles one or more agent-emitted APIARY_SPAWN requests: for each it
// creates a deduped child InternalTask via the spawner and, when the step sets
// materialize: sub_issue, publishes the child to the source as a sub-issue under
// the parent's item. A request that names a workflow runs it (fire-and-forget, or
// blocking under spawn: await); a request with no workflow is materialize-only —
// the child is left for the normal poll→route loop to pick up by its labels.
//
// Spawn requests are only honored when the agent step itself succeeded. Each child
// is independent: a failure on one (spawn, materialize, or await) is recorded but
// does not stop the others, so a re-run makes maximal forward progress before the
// step is failed. Idempotency comes from the child dedup key (one child per key)
// and the materialize binding check (one sub-issue per child), so re-running the
// decomposition never fans out a duplicate set (issue #119).
func (e *Engine) spawnStep(ctx context.Context, task model.InternalTask, step config.StepConfig, bindings []model.SourceBinding, res *StepResult, sr *db.StepRun) {
	reqs := res.SpawnRequests
	if len(reqs) == 0 && res.SpawnRequest != nil {
		reqs = []model.SpawnRequest{*res.SpawnRequest}
	}
	if len(reqs) == 0 || !res.Success {
		return
	}
	if e.spawner == nil {
		res.Success = false
		res.Err = fmt.Errorf("step %q requested APIARY_SPAWN but no spawner is configured", step.ID)
		return
	}

	var spawnErrs []error
	for _, req := range reqs {
		req.ParentTaskID = task.ID
		req.Depth = task.Metadata.SpawnDepth + 1
		child, err := e.spawner.Spawn(ctx, req)
		if err != nil {
			spawnErrs = append(spawnErrs, fmt.Errorf("spawn workflow %q: %w", req.WorkflowID, err))
			continue
		}
		// Record the first spawned child on the step run (it has a single slot).
		if sr.SpawnedTaskID == "" {
			sr.SpawnedTaskID = child.ID
		}

		if step.Materialize == config.MaterializeSubIssue {
			if err := e.materializeChild(ctx, task, bindings, child); err != nil {
				spawnErrs = append(spawnErrs, err)
				continue
			}
		}

		// spawn: await blocks only on children that actually run an inline workflow;
		// a materialize-only child has no workflow to wait on.
		if step.Spawn == config.SpawnAwait && req.WorkflowID != "" {
			ok, werr := e.spawner.Await(ctx, child.ID)
			if werr != nil {
				spawnErrs = append(spawnErrs, fmt.Errorf("spawn await child %s: %w", child.ID, werr))
				continue
			}
			if !ok {
				spawnErrs = append(spawnErrs, fmt.Errorf("spawned task %s failed", child.ID))
			}
		}
	}

	if len(spawnErrs) > 0 {
		res.Success = false
		res.Err = errors.Join(spawnErrs...)
	}
}

// materializeChild publishes a spawned child as a source sub-issue under the
// parent's item, exactly once. It is idempotent and self-healing: a child that
// already has a source binding (already materialized, including by a prior run
// that crashed after the agent step) is skipped, so re-running the decomposition
// never creates a second sub-issue. A binding-less parent (e.g. a spawned parent)
// has no item to anchor under and is a no-op.
func (e *Engine) materializeChild(ctx context.Context, parent model.InternalTask, parentBindings []model.SourceBinding, child model.InternalTask) error {
	if e.side == nil {
		return fmt.Errorf("materialize child %s: no side effects configured", child.ID)
	}
	if len(parentBindings) == 0 {
		return nil
	}
	if existing := e.bindingsFor(ctx, child.ID); len(existing) > 0 {
		return nil
	}
	return e.side.MaterializeChild(ctx, parent, parentBindings, child)
}

// agentProvenanceMarker is prepended to every APIARY_PUBLISH payload before it
// is posted as a comment. It marks the content as agent-authored so tooling and
// human reviewers can distinguish it from human-written comments. The marker is
// an invisible HTML comment so it does not clutter the rendered output.
const agentProvenanceMarker = "<!-- apiary:agent-authored -->\n"

// publishStep writes an agent-emitted APIARY_PUBLISH payload back to the task's
// source bindings and records the outcome on the step run. The executor has
// already cleared the payload when the step set publish: off, so a non-empty
// payload here means write-back was requested. A task with no bindings (e.g. a
// spawned task) is silently skipped.
//
// Every posted payload is prefixed with agentProvenanceMarker so that tooling
// can reliably distinguish agent-authored comments from human-written ones.
func (e *Engine) publishStep(ctx context.Context, task model.InternalTask, bindings []model.SourceBinding, res StepResult, sr *db.StepRun) {
	if res.PublishPayload == "" {
		return
	}
	sr.PublishPayload = res.PublishPayload
	if e.side == nil || len(bindings) == 0 {
		sr.PublishState = db.PublishStateSkipped
		return
	}
	payload := agentProvenanceMarker + res.PublishPayload
	if err := e.side.PostComment(ctx, task, bindings, payload); err != nil {
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
