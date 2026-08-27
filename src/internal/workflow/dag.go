package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// step execution states within a DAG run.
const (
	stPending     = "pending"
	stRunning     = "running" // dispatched to a worker goroutine, not yet complete
	stPassed      = "passed"
	stFailed      = "failed"
	stSkipped     = "skipped"      // cascade-skipped because a dep failed/was cascade-skipped
	stCondSkipped = "cond_skipped" // skipped because the step's own condition was false
	stWaiting     = "waiting"      // an approval step parked awaiting a human response
)

// dagOutcome is the terminal (or suspended) result of driving a DAG run.
type dagOutcome int

const (
	outcomeDone dagOutcome = iota
	outcomeFailed
	outcomeWaiting // suspended at an approval step
)

// dagRun holds the mutable state of one workflow instance's step graph as the
// scheduler walks it.
type dagRun struct {
	instID   string
	wf       config.WorkflowConfig
	task     model.InternalTask    // the unit of work this instance runs against
	bindings []model.SourceBinding // task's source bindings (side-effect targets)
	cell     model.SourceItem      // execution view derived from task + primary binding
	depth    int                   // nesting depth (0 = top-level; >0 = sub-workflow child)
	seed     []MemoryStep          // inherited memory (sub-workflow snapshot from the parent)
	// event is the PR event payload for an event-triggered instance, exposed to
	// condition expressions as event.*. Nil for item-triggered (and rehydrated)
	// instances.
	event map[string]string

	byID  map[string]config.StepConfig
	order []string // step ids in declaration order (deterministic scheduling)
	// childByID / parentOfChild index the children of parallel nodes, which are
	// not graph nodes of their own (they live in the parent's SubSteps).
	childByID     map[string]config.StepConfig
	parentOfChild map[string]string // child step id → parallel parent id

	state           map[string]string // step id → st* state
	activated       map[string]bool   // control-flow has reached this step
	splitTarget     map[string]bool   // step id is the goto target of some split branch
	retries         map[string]int    // on_fail.goto loop counter per failing step
	conflictRetries map[string]int    // on_conflict.goto loop counter per conflicting step

	stepStates  map[string]StepState  // terminal states for expression context
	contrib     map[string]MemoryStep // memory contribution per passed step
	passedOrder []string              // step ids in the order they passed

	waitingStep string    // id of the approval/wait_for step currently parked, if any
	parkedAt    time.Time // when the current approval parked (for timeout)
	// waitingChild is set when waitingStep is a parallel node parked on a
	// wait_for CHILD: it holds that child's id, so the re-check knows which
	// wait_for config to poll. Empty when the parked step is a wait_for itself.
	waitingChild string
	// parallelDone memoizes the terminal results of a parked parallel group's
	// finished children (parent step id → child id → result), so waking the
	// group to re-poll its wait_for child does not re-run the siblings that
	// already passed. Cleared once the group reaches a terminal join (#425).
	parallelDone map[string]map[string]StepResult

	// waitDeadline is the absolute time a parked wait_for step gives up waiting for
	// CI (set when the wait first parks, preserved across re-checks, cleared once
	// the wait produces a terminal pass/fail). Zero means "no deadline yet".
	waitDeadline time.Time
}

// initDAG builds the in-memory state for a workflow instance's step graph. The
// execution view (cell) is projected from the task + its primary binding. seed
// is inherited memory for a sub-workflow child; depth tracks nesting.
func (e *Engine) initDAG(instID string, wf config.WorkflowConfig, task model.InternalTask, bindings []model.SourceBinding, seed []MemoryStep, depth int) *dagRun {
	// Memory provenance: parallel/foreach worker paths only carry the instance
	// ID, so memorizeStep resolves the workflow ID through this map. Removed on
	// terminal settle; parked instances keep their mapping for the resume path.
	e.instWF.Store(instID, wf.ID)
	r := &dagRun{
		instID:          instID,
		wf:              wf,
		task:            task,
		bindings:        bindings,
		cell:            sourceItemView(task, bindings),
		depth:           depth,
		seed:            seed,
		byID:            map[string]config.StepConfig{},
		state:           map[string]string{},
		activated:       map[string]bool{},
		splitTarget:     map[string]bool{},
		retries:         map[string]int{},
		conflictRetries: map[string]int{},
		stepStates:      map[string]StepState{},
		contrib:         map[string]MemoryStep{},
		parallelDone:    map[string]map[string]StepResult{},
		childByID:       map[string]config.StepConfig{},
		parentOfChild:   map[string]string{},
	}
	for _, s := range wf.Steps {
		r.byID[s.ID] = s
		r.order = append(r.order, s.ID)
		r.state[s.ID] = stPending
		// Index a parallel node's children so the park/resume path can resolve a
		// waiting child's config and restore its cached result by id alone.
		if s.StepType() == config.StepTypeParallel {
			for _, child := range s.SubSteps {
				r.childByID[child.ID] = child
				r.parentOfChild[child.ID] = s.ID
			}
		}
	}
	// Identify split targets: they are activated only when their split chooses
	// them, never at workflow start.
	for _, s := range wf.Steps {
		if s.StepType() == config.StepTypeSplit {
			for _, b := range s.Branches {
				if b.Goto != "" {
					r.splitTarget[b.Goto] = true
				}
			}
		}
	}
	// Activate every non-split-target step up front; depends_on still gates them.
	for _, id := range r.order {
		if !r.splitTarget[id] {
			r.activated[id] = true
		}
	}
	return r
}

// workerResult carries the execution outcome of a single step back to the
// scheduler goroutine. Only the scheduler goroutine reads/writes dagRun state.
type workerResult struct {
	stepID  string
	step    config.StepConfig
	memSnap []MemoryStep // memory snapshot taken when the step was dispatched
	res     StepResult
	// parallelContribs is set for StepTypeParallel steps: the ordered list of
	// memory contributions from its passed children (declaration order).
	parallelContribs []MemoryStep
	// parallel is set for StepTypeParallel steps: which child (if any) is parked
	// on a pending wait_for, plus the results of the children that finished.
	parallel parallelState
	// foreachExitCode is the failed-item count for StepTypeForeach steps.
	// Used to populate StepState.ExitCode (allows `steps.<id>.exit_code` in exprs).
	foreachExitCode int
}

// driveDAG runs the scheduler until the graph completes, fails, or suspends at
// an approval step. It dispatches all runnable steps concurrently (bounded by
// the global semaphore), processes results on the scheduler goroutine, and
// handles split routing, on_fail.goto loops, and skip propagation.
// It may be re-entered after an approval resolves.
func (e *Engine) driveDAG(ctx context.Context, r *dagRun) dagOutcome {
	sem := make(chan struct{}, e.concurrencyLimit())
	resultCh := make(chan workerResult, len(r.order)+1)
	inFlight := 0

	// termFail is set when a step fails irrecoverably; we drain in-flight before
	// returning outcomeFailed (so workers don't write to a closed channel).
	termFail := false
	// loopTarget is set when a step triggers on_fail.goto; we drain in-flight
	// before calling resetLoop.
	loopTarget := ""

	dispatch := func() {
		for _, id := range r.pickAllRunnable() {
			step := r.byID[id]

			// Per-step condition: evaluate on scheduler goroutine (reads dagRun).
			// An expression that cannot be parsed or evaluated fails the step
			// (routed through resultCh so on_fail applies) — silently treating it
			// as false would skip the branch with no error signal (#180).
			if step.Condition != "" {
				evalCtx := EvalContext{Cell: r.cell, Memory: r.memoryValues(), Steps: r.stepStates, Event: r.event}
				condOK, condErr := e.evalExpr(step.Condition, evalCtx)
				if condErr != nil {
					aplog.Error("workflow %s: step %q condition eval error %q: %v (failing step)", r.wf.ID, id, step.Condition, condErr)
					r.state[id] = stRunning
					inFlight++
					resultCh <- workerResult{stepID: id, step: step, res: StepResult{
						Success: false,
						Output:  fmt.Sprintf("condition eval error %q: %v", step.Condition, condErr),
					}}
					continue
				}
				if !condOK {
					r.markCondSkipped(id)
					aplog.Debug("workflow %s: step %q skipped (condition false)", r.wf.ID, id)
					continue
				}
			}

			// Split: synchronous, no I/O — apply inline on scheduler goroutine.
			// A branch expression that cannot be evaluated fails the split step
			// (routed through resultCh so on_fail applies) — see #180.
			if step.StepType() == config.StepTypeSplit {
				if err := e.runSplitStep(r, step); err != nil {
					aplog.Error("workflow %s: split %q condition eval error: %v (failing step)", r.wf.ID, id, err)
					r.state[id] = stRunning
					inFlight++
					resultCh <- workerResult{stepID: id, step: step, res: StepResult{
						Success: false,
						Output:  fmt.Sprintf("split condition eval error: %v", err),
					}}
				}
				continue
			}

			// Approval: must drain in-flight first; handled after drain below.
			if step.StepType() == config.StepTypeApproval {
				// Mark as running so pickAllRunnable won't re-select it, but don't
				// launch a worker — the approval is handled after drain.
				r.state[id] = stRunning
				inFlight++ // counts as "in-flight" so we drain before acting
				resultCh <- workerResult{stepID: id, step: step, res: StepResult{Success: true}}
				// The result will be processed below; it signals the approval path.
				continue
			}

			// Concurrency gate: don't over-dispatch; wait for in-flight to drain.
			// This also ensures declaration-order execution when concurrency=1.
			if inFlight >= e.concurrencyLimit() {
				break
			}

			// I/O-bound steps: dispatch to worker goroutine.
			r.state[id] = stRunning
			snap := r.memSteps()
			contribSnap := r.contribSnapshot()
			waitDeadline := r.waitDeadline // captured for the wait worker (value copy, safe)
			inFlight++
			parallelCache := r.parallelSnapshot(id) // captured for the parallel worker
			go func(step config.StepConfig, memSnap []MemoryStep, contribSnap map[string]MemoryStep) {
				sem <- struct{}{}
				defer func() { <-sem }()
				var res StepResult
				var parallelContribs []MemoryStep
				var parallel parallelState
				var foreachExitCode int
				switch step.StepType() {
				case config.StepTypeParallel:
					res, parallelContribs, parallel = e.runParallelStep(ctx, r.instID, step, r.cell, r.task, r.bindings, memSnap, r.wf.ID, scopeOf(r.wf), parallelCache, waitDeadline)
				case config.StepTypeForeach:
					var fr foreachResult
					if step.Concurrency > 1 {
						// Release our global slot so item goroutines can use all
						// available slots. Re-acquire before the deferred release fires.
						<-sem
						res, fr = e.executeForeachStep(ctx, r.instID, step, r.cell, r.task, r.bindings, memSnap, contribSnap, r.wf.ID, sem, scopeOf(r.wf))
						sem <- struct{}{}
					} else {
						res, fr = e.executeForeachStep(ctx, r.instID, step, r.cell, r.task, r.bindings, memSnap, contribSnap, r.wf.ID, nil, scopeOf(r.wf))
					}
					foreachExitCode = fr.failed
				case config.StepTypeWorkflow:
					res = e.executeSubWorkflowStep(ctx, r.instID, step, r.task, r.bindings, memSnap, contribSnap, r.depth, r.wf.ID)
				case config.StepTypeWaitFor:
					res, _ = e.RunWaitStep(ctx, r.instID, step, r.cell.SourceID, r.cell.ID, waitDeadline)
				default: // StepTypeAgent
					res = e.runStep(ctx, r.instID, step, r.cell, r.task, r.bindings, memSnap, scopeOf(r.wf))
				}
				resultCh <- workerResult{
					stepID:           step.ID,
					step:             step,
					memSnap:          memSnap,
					res:              res,
					parallelContribs: parallelContribs,
					parallel:         parallel,
					foreachExitCode:  foreachExitCode,
				}
			}(step, snap, contribSnap)
		}
	}

	for {
		// Dispatch only when not draining for a loop-back or terminal failure.
		if !termFail && loopTarget == "" {
			dispatch()
		}

		// Nothing in-flight: resolve pending control-flow or break.
		if inFlight == 0 {
			if termFail {
				return outcomeFailed
			}
			if loopTarget != "" {
				r.resetLoop(loopTarget)
				loopTarget = ""
				continue
			}
			if r.skipUnreachable() {
				continue
			}
			// A synchronous step (split) may have activated new steps this cycle.
			// Re-check before declaring quiescence.
			if len(r.pickAllRunnable()) > 0 {
				continue
			}
			break
		}

		// Wait for one worker to finish.
		wr := <-resultCh
		inFlight--
		step := wr.step

		// Approval path: all in-flight drained by the time we get here since
		// dispatch sends approvals immediately to resultCh.
		if step.StepType() == config.StepTypeApproval {
			// Wait for all other in-flight workers to finish first.
			for inFlight > 0 {
				<-resultCh
				inFlight--
			}
			e.enterApproval(ctx, r, step)
			return outcomeWaiting
		}

		res := wr.res
		if ctx.Err() != nil {
			res.Success = false
			res.Output = "workflow canceled: " + ctx.Err().Error()
			res.Err = ctx.Err()
		}

		// Wait_for step with no terminal result yet: suspend the instance at this step
		// (releasing the worker) and resume it on a later poll cycle, exactly like
		// an approval park. Drain any siblings first (wait_for workflows are sequential
		// in practice, so this is normally a no-op). The deadline persists across
		// re-checks via enterWait so the timeout is measured from the first park.
		// Parallel group whose wait_for child has no answer yet: park the GROUP,
		// remembering the results of the children that finished so the wake
		// re-polls only the wait and never re-runs a passed sibling (#425).
		if step.StepType() == config.StepTypeParallel && res.Pending {
			for inFlight > 0 {
				<-resultCh
				inFlight--
			}
			r.parallelDone[step.ID] = wr.parallel.done
			r.waitingChild = wr.parallel.waitingChild
			e.enterWait(r, step)
			return outcomeWaiting
		}
		// Terminal parallel join: drop the memoized child results so a later
		// loop-back (on_fail.goto) re-runs the whole group from scratch.
		if step.StepType() == config.StepTypeParallel {
			if _, wasParked := r.parallelDone[step.ID]; wasParked {
				// Clear the waiting window too, so a loop-back into this group
				// starts a fresh one (mirrors the plain wait_for path below).
				r.waitDeadline = time.Time{}
				delete(r.parallelDone, step.ID)
			}
			r.waitingChild = ""
		}

		if step.StepType() == config.StepTypeWaitFor && res.Pending {
			for inFlight > 0 {
				<-resultCh
				inFlight--
			}
			e.enterWait(r, step)
			return outcomeWaiting
		}
		// Terminal poll result (pass/fail): clear the deadline so a future loop-back
		// to this poll (on_reject.restart_from) starts a fresh waiting window.
		if step.StepType() == config.StepTypeWaitFor {
			r.waitDeadline = time.Time{}
		}

		// Missing structured output for a step that declared an output schema.
		res = e.applyMissingOutput(ctx, runIDs{taskID: r.task.ID, wfID: r.wf.ID, instID: r.instID}, step, res)

		// fail_when (authored as reject_when) — evaluate on the scheduler
		// goroutine after the agent runs.
		if res.Success && step.FailWhen != "" {
			res = e.applyFailWhen(ctx, r, step, res, wr.parallelContribs)
		}

		ss := StepState{State: passFail(res.Success), Output: res.Output}
		if step.StepType() == config.StepTypeForeach {
			ss.ExitCode = wr.foreachExitCode
		}
		r.stepStates[step.ID] = ss

		if e.resultCommentMode(r.wf) == config.ResultCommentPerStep && e.side != nil {
			_ = e.side.PostComment(ctx, r.task, r.bindings, perStepComment(step, res))
		}

		if res.Success {
			r.state[step.ID] = stPassed
			if step.StepType() == config.StepTypeParallel {
				// Merge children's contributions into the outer DAG memory.
				for _, c := range wr.parallelContribs {
					r.contrib[c.StepID] = c
				}
				// The parallel step itself has no direct memory contribution;
				// its children's contributions are now visible via r.contrib.
			} else {
				r.contrib[step.ID] = MemoryStep{
					StepID:      step.ID,
					WriteFields: step.MemoryWriteFields(),
					Structured:  res.StructuredOutput,
					Summary:     res.Summary,
				}
			}
			r.passedOrder = append(r.passedOrder, step.ID)
			if step.OnPass != nil && step.OnPass.Next != "" {
				r.activated[step.OnPass.Next] = true
			}
			continue
		}

		// Merge-conflict failure with a dedicated on_conflict route: that edge
		// governs the loop-back exclusively, with its own retry budget separate from
		// on_fail (a rebase retry must not consume the CI-failure retry budget, and
		// vice-versa). Once on_conflict's budget is exhausted the failure is terminal
		// — it does NOT fall through to on_fail. A conflict on a step with no
		// on_conflict declared skips this block and is handled by on_fail below.
		conflictGoverned := res.Conflict && step.OnConflict != nil && step.OnConflict.Goto != ""
		if !termFail && loopTarget == "" && conflictGoverned &&
			r.conflictRetries[step.ID] < step.OnConflict.MaxRetries {
			r.conflictRetries[step.ID]++
			aplog.Info("workflow %s: step %q hit a merge conflict, looping back to %q (retry %d/%d)",
				r.wf.ID, step.ID, step.OnConflict.Goto, r.conflictRetries[step.ID], step.OnConflict.MaxRetries)
			r.state[step.ID] = stPending
			loopTarget = step.OnConflict.Goto
			continue
		}

		// Failure: attempt on_fail.goto loop if retries remain. Skipped when a
		// conflict is governed by on_conflict (its budget is exhausted → terminal).
		if !termFail && loopTarget == "" && !conflictGoverned &&
			step.OnFail != nil && step.OnFail.Goto != "" &&
			r.retries[step.ID] < step.OnFail.MaxRetries {
			r.retries[step.ID]++
			aplog.Info("workflow %s: step %q failed, looping back to %q (retry %d/%d)",
				r.wf.ID, step.ID, step.OnFail.Goto, r.retries[step.ID], step.OnFail.MaxRetries)
			// Mark the step as pending so it can re-run after the loop reset.
			r.state[step.ID] = stPending
			loopTarget = step.OnFail.Goto
			// Drain remaining in-flight before resetting.
			continue
		}

		// Irrecoverable failure.
		aplog.Info("workflow %s: step %q failed permanently", r.wf.ID, step.ID)
		if step.OnFail != nil && step.OnFail.Goto != "" {
			aplog.Info("workflow %s: step %q exhausted %d retries", r.wf.ID, step.ID, step.OnFail.MaxRetries)
		}
		r.state[step.ID] = stFailed
		termFail = true
		loopTarget = "" // cancel any pending loop
		// Drain remaining in-flight before returning.
	}

	// The instance fails if any step ended failed.
	for _, id := range r.order {
		if r.state[id] == stFailed {
			return outcomeFailed
		}
	}

	// Stranded steps: the scheduler went quiescent while a declared step is still
	// pending. Such a step neither ran, nor had its own condition evaluate false,
	// nor was cascade-skipped by a failed/skipped dependency — it simply became
	// unreachable (typically an explicit depends_on on a condition-skipped step).
	// Reporting success here silently drops declared work, which is how a pair of
	// quality gates disappeared from a pipeline with no signal at all (#379).
	// Fail loudly instead: a declared step that never ran is not a success.
	if stranded := r.strandedSteps(); len(stranded) > 0 {
		aplog.Error("workflow %s: instance %s finished with steps that never ran and were never skipped: %s — failing the instance rather than reporting success (check depends_on against condition-skipped steps)",
			r.wf.ID, r.instID, strings.Join(stranded, ", "))
		return outcomeFailed
	}
	return outcomeDone
}

// strandedSteps returns the ids of steps still pending once the scheduler has
// gone quiescent, in declaration order. A pending step at quiescence can never
// run: nothing is in flight to unblock it and no further control flow is
// possible. See driveDAG for why this is a failure and not a success.
func (r *dagRun) strandedSteps() []string {
	var ids []string
	for _, id := range r.order {
		if r.state[id] == stPending {
			ids = append(ids, id)
		}
	}
	return ids
}

// enterApproval parks the run at an approval step: it posts the step message to
// the task and records the waiting step. The polling loop later resumes or
// aborts it via the engine's approval handling.
func (e *Engine) enterApproval(ctx context.Context, r *dagRun, step config.StepConfig) {
	if e.side != nil && step.Message != "" {
		_ = e.side.PostComment(ctx, r.task, r.bindings, step.Message)
	}
	r.state[step.ID] = stWaiting
	r.waitingStep = step.ID
	r.parkedAt = e.now()
	requestID := r.instID + ":" + step.ID
	fields := make([]map[string]any, 0, len(step.ApprovalFields))
	for _, f := range step.ApprovalFields {
		fields = append(fields, map[string]any{"name": f.Name, "label": f.Label, "type": f.Type, "required": f.Required, "options": f.Options})
	}
	var expires *time.Time
	if timeout := step.ParsedTimeout(); timeout > 0 {
		deadline := r.parkedAt.Add(timeout)
		expires = &deadline
	}
	request := &db.ApprovalRequest{ID: requestID, WorkflowInstanceID: r.instID, TaskID: r.task.ID, WorkflowID: r.wf.ID, StepID: step.ID, Message: step.Message, Approvers: step.Approvers, Delegates: step.Delegates, RequiredApprovals: step.RequiredApprovals, Fields: fields, CreatedAt: r.parkedAt, ExpiresAt: expires}
	if store, ok := e.store.(approvalRequestStore); ok {
		_ = store.CreateApprovalRequest(ctx, request)
	}
	for _, provider := range e.approvalProviders {
		if err := provider.RequestApproval(ctx, request); err != nil {
			aplog.Warn("workflow: approval provider for %s: %v", requestID, err)
		}
	}
	e.recordExecutionEvent(ctx, r, "approval.requested", map[string]any{"request_id": requestID, "message": step.Message, "approvers": step.Approvers, "fields": fields})
	aplog.Info("workflow %s: instance %s parked at approval step %q", r.wf.ID, r.instID, step.ID)
}

// enterWait parks the run at a wait_for step: it records the waiting step and, on the
// first park, sets the absolute deadline after which the wait gives up (from the
// step's max_duration). Re-parks of the same poll (CI still pending) preserve the
// original deadline so the timeout is measured from the first wait, not reset each
// cycle. Unlike an approval, a wait park posts no message — it waits silently.
func (e *Engine) enterWait(r *dagRun, step config.StepConfig) {
	r.state[step.ID] = stWaiting
	r.waitingStep = step.ID
	// The wait config lives on the parked step itself, or — for a parallel group
	// parked on a wait_for child — on that child.
	waitStep, _ := r.waitStepConfig()
	if r.waitDeadline.IsZero() && waitStep.WaitFor != nil {
		if md := waitStep.WaitFor.ParsedMaxDuration(); md > 0 {
			r.waitDeadline = e.now().Add(md)
		}
	}
	if r.waitingChild != "" {
		aplog.Info("workflow %s: instance %s parked at wait_for child %q of parallel step %q",
			r.wf.ID, r.instID, r.waitingChild, step.ID)
		return
	}
	aplog.Info("workflow %s: instance %s parked at wait_for step %q", r.wf.ID, r.instID, step.ID)
}

// waitStepConfig returns the step whose wait_for config governs the current
// park: the parked step itself when it is a wait_for node, or the parked child
// when the instance is parked at a parallel group (#425). Reports false when
// the run is not parked on a wait at all.
func (r *dagRun) waitStepConfig() (config.StepConfig, bool) {
	if r.waitingStep == "" {
		return config.StepConfig{}, false
	}
	if r.waitingChild != "" {
		child, ok := r.childByID[r.waitingChild]
		return child, ok
	}
	step := r.byID[r.waitingStep]
	return step, step.StepType() == config.StepTypeWaitFor
}

// parallelSnapshot copies the memoized child results of a parked parallel group
// so the worker goroutine reads them without touching dagRun.
func (r *dagRun) parallelSnapshot(stepID string) map[string]StepResult {
	done := r.parallelDone[stepID]
	if len(done) == 0 {
		return nil
	}
	out := make(map[string]StepResult, len(done))
	for k, v := range done {
		out[k] = v
	}
	return out
}

// resolveApproval applies an approval decision to the parked step so the run can
// continue: a resume marks it passed, an abort marks it failed.
func (r *dagRun) resolveApproval(decision ApprovalDecision) {
	step := r.waitingStep
	r.waitingStep = ""
	if decision == ApprovalResume {
		r.state[step] = stPassed
		r.stepStates[step] = StepState{State: stPassed}
	} else {
		r.state[step] = stFailed
	}
}

// firstRunnableApproval returns the id of the first approval step that is
// currently runnable (activated, not yet resolved, dependencies satisfied), in
// declaration order. It identifies which approval step a rehydrated instance is
// parked at: an approval step never persists a step run of its own, so after the
// cached passed steps are restored the waiting approval is simply the next
// runnable approval. Returns false when none is runnable.
func (r *dagRun) firstRunnableApproval() (string, bool) {
	for _, id := range r.pickAllRunnable() {
		if r.byID[id].StepType() == config.StepTypeApproval {
			return id, true
		}
	}
	return "", false
}

// firstRunnableWait returns the id of the first wait_for step that is currently
// runnable, in declaration order. It identifies which wait_for step a rehydrated
// instance is parked at: wait_for steps persist no step run of their own, so after the
// cached passed steps are restored the waiting poll is simply the next runnable
// poll. Returns false when none is runnable.
// It also matches a parallel group holding a wait_for child, returning that
// child's id alongside the group's — an instance can park on a wait nested one
// level inside a group (#425).
func (r *dagRun) firstRunnableWait() (stepID, childID string, ok bool) {
	for _, id := range r.pickAllRunnable() {
		step := r.byID[id]
		switch step.StepType() {
		case config.StepTypeWaitFor:
			return id, "", true
		case config.StepTypeParallel:
			for _, child := range step.SubSteps {
				if child.StepType() == config.StepTypeWaitFor {
					return id, child.ID, true
				}
			}
		}
	}
	return "", "", false
}

// pickAllRunnable returns the IDs of ALL currently runnable steps (activated,
// pending, all dependencies passed), in declaration order.
func (r *dagRun) pickAllRunnable() []string {
	var ids []string
	for _, id := range r.order {
		if r.state[id] != stPending || !r.activated[id] {
			continue
		}
		if r.depsPassed(id) {
			ids = append(ids, id)
		}
	}
	return ids
}

func (r *dagRun) depsPassed(id string) bool {
	for _, dep := range r.byID[id].DependsOn {
		// Explicit deps must be fully passed; condition-skip does cascade here.
		if r.state[dep] != stPassed {
			return false
		}
	}
	for _, dep := range r.byID[id].SeqDependsOn {
		// Implicit sequential deps: condition-skipped counts as satisfied so the
		// successor can still run even when its predecessor's if: was false.
		st := r.state[dep]
		if st != stPassed && st != stCondSkipped {
			return false
		}
	}
	return true
}

// skipUnreachable marks pending steps that can never run as skipped, returning
// whether it made progress. A step is unreachable when a dependency is terminal
// non-passed (skipped/failed), or it is an un-activated split target whose
// feeding splits have all finished.
func (r *dagRun) skipUnreachable() bool {
	progress := false
	for _, id := range r.order {
		if r.state[id] != stPending {
			continue
		}
		// Explicit dep ended skipped/failed → this step can never run.
		// A condition-skipped explicit dep cascades too: depsPassed requires an
		// explicit dependency to have *passed*, so the dependent is unreachable.
		// Recording it as skipped (instead of leaving it pending forever) is what
		// the cascade always intended, and it is announced in the log — a step
		// that quietly disappears from a pipeline is exactly what #379 cost.
		for _, dep := range r.byID[id].DependsOn {
			if st := r.state[dep]; st == stSkipped || st == stFailed || st == stCondSkipped {
				if st == stCondSkipped {
					aplog.Warn("workflow %s: step %q skipped — its explicit depends_on %q was condition-skipped (use an if: on this step, or rely on implicit sequencing, if it should still run)",
						r.wf.ID, id, dep)
				}
				r.markSkipped(id)
				progress = true
				break
			}
		}
		if r.state[id] != stPending {
			continue
		}
		// Seq dep failed/skipped cascades too, but stCondSkipped does not.
		for _, dep := range r.byID[id].SeqDependsOn {
			if r.state[dep] == stSkipped || r.state[dep] == stFailed {
				r.markSkipped(id)
				progress = true
				break
			}
		}
		if r.state[id] != stPending {
			continue
		}
		// An un-activated split target whose every feeding split is terminal
		// will never be chosen.
		if r.splitTarget[id] && !r.activated[id] && r.feedingSplitsDone(id) {
			r.markSkipped(id)
			progress = true
		}
	}
	return progress
}

// feedingSplitsDone reports whether every split that can goto id has finished.
func (r *dagRun) feedingSplitsDone(id string) bool {
	for _, s := range r.wf.Steps {
		if s.StepType() != config.StepTypeSplit {
			continue
		}
		for _, b := range s.Branches {
			if b.Goto == id {
				st := r.state[s.ID]
				if st == stPending {
					return false
				}
			}
		}
	}
	return true
}

// markSkipped sets a step cascade-skipped (because a dep failed or was cascade-skipped)
// and propagates to pending dependents. Does not override stCondSkipped.
func (r *dagRun) markSkipped(id string) {
	if r.state[id] == stSkipped || r.state[id] == stCondSkipped {
		return
	}
	r.state[id] = stSkipped
	for _, other := range r.order {
		if r.state[other] != stPending {
			continue
		}
		for _, dep := range r.byID[other].DependsOn {
			if dep == id {
				r.markSkipped(other)
				break
			}
		}
		if r.state[other] != stPending {
			continue
		}
		// SeqDependsOn failures also cascade (only stCondSkipped is exempt).
		for _, dep := range r.byID[other].SeqDependsOn {
			if dep == id {
				r.markSkipped(other)
				break
			}
		}
	}
}

// markCondSkipped marks exactly this step as condition-skipped (no cascade).
// Successors that depend only on condition-skipped steps can still run.
func (r *dagRun) markCondSkipped(id string) {
	r.state[id] = stCondSkipped
	r.stepStates[id] = StepState{State: stCondSkipped}
}

// runSplitStep evaluates a split's branches and activates the chosen target(s),
// skipping the rest. A branch expression that cannot be parsed or evaluated
// returns an error before any state is mutated — the caller fails the split
// step rather than silently routing as if the branch didn't match (#180).
func (e *Engine) runSplitStep(r *dagRun, step config.StepConfig) error {
	ctx := EvalContext{Cell: r.cell, Memory: r.memoryValues(), Steps: r.stepStates, Event: r.event}

	chosen := map[string]bool{}
	for _, b := range step.Branches {
		match := b.IsFallback()
		if !match {
			expr, err := ParseExpr(b.If)
			if err != nil {
				return fmt.Errorf("branch %q: %w", b.If, err)
			}
			ok, err := expr.Eval(ctx)
			if err != nil {
				return fmt.Errorf("branch %q: %w", b.If, err)
			}
			match = ok
		}
		if match {
			chosen[b.Goto] = true
			if !step.Multi {
				break // first match wins
			}
		}
	}

	r.state[step.ID] = stPassed
	r.stepStates[step.ID] = StepState{State: stPassed}

	// Activate chosen targets; skip the rest of this split's branch targets.
	for _, b := range step.Branches {
		if b.Goto == "" {
			continue
		}
		if chosen[b.Goto] {
			r.activated[b.Goto] = true
		}
	}
	for _, b := range step.Branches {
		if b.Goto != "" && !chosen[b.Goto] && !r.activated[b.Goto] && r.feedingSplitsDone(b.Goto) {
			r.markSkipped(b.Goto)
		}
	}
	return nil
}

// resetLoop resets the goto target and all its transitive dependents back to
// pending so the branch re-runs, clearing their memory contributions.
func (r *dagRun) resetLoop(target string) {
	reset := map[string]bool{}
	var mark func(id string)
	mark = func(id string) {
		if reset[id] {
			return
		}
		reset[id] = true
		for _, other := range r.order {
			for _, dep := range r.byID[other].DependsOn {
				if dep == id {
					mark(other)
				}
			}
			for _, dep := range r.byID[other].SeqDependsOn {
				if dep == id {
					mark(other)
				}
			}
		}
	}
	mark(target)

	for id := range reset {
		r.state[id] = stPending
		delete(r.stepStates, id)
		delete(r.contrib, id)
	}
	// Rebuild passedOrder in declaration order excluding reset steps.
	r.passedOrder = r.passedOrder[:0]
	for _, id := range r.order {
		if _, ok := r.contrib[id]; ok {
			r.passedOrder = append(r.passedOrder, id)
		}
	}
}

// memSteps returns the ordered memory contributions: inherited seed (for a
// sub-workflow) first, then this run's passed steps in declaration order.
// Declaration order is deterministic regardless of goroutine completion order.
func (r *dagRun) memSteps() []MemoryStep {
	out := make([]MemoryStep, 0, len(r.seed)+len(r.contrib))
	out = append(out, r.seed...)
	for _, id := range r.order {
		if c, ok := r.contrib[id]; ok {
			out = append(out, c)
		}
	}
	return out
}

// contribSnapshot returns a shallow copy of the contrib map for safe use from
// worker goroutines (reads the snapshot, not the live map).
func (r *dagRun) contribSnapshot() map[string]MemoryStep {
	snap := make(map[string]MemoryStep, len(r.contrib))
	for k, v := range r.contrib {
		snap[k] = v
	}
	return snap
}

// memoryValues returns the flattened Step Data map for expression evaluation
// (memory.<key>), honoring last-write-wins in passed order.
func (r *dagRun) memoryValues() map[string]string {
	return memoryValuesFrom(r.memSteps())
}

// memoryValuesFrom flattens ordered memory contributions into the Step Data
// map used by expression evaluation (memory.<key>), last-write-wins.
func memoryValuesFrom(steps []MemoryStep) map[string]string {
	vals := map[string]string{}
	for _, ms := range steps {
		for _, field := range ms.WriteFields {
			if v, ok := ms.Structured[field]; ok {
				vals[field] = renderValue(v)
			}
		}
	}
	return vals
}

func passFail(success bool) string {
	if success {
		return stPassed
	}
	return stFailed
}

// applyFailWhen evaluates a step's fail_when (reject_when) gate and returns the
// possibly-failed result.
//
// Three ways the gate can fail the step:
//
//  1. The expression cannot be parsed or evaluated — silently treating it as
//     "not rejected" would pass a result the gate was supposed to inspect (#180).
//  2. The gate reads memory keys THIS step declared in `memory.write` but never
//     emitted (typically: the agent omitted its APIARY_OUTPUT line). The gate is
//     unevaluable, so it fails closed — a rejected review must never be recorded
//     as passed just because the verdict went missing (#390).
//  3. The gate matched: the agent rejected the work.
func (e *Engine) applyFailWhen(ctx context.Context, r *dagRun, step config.StepConfig, res StepResult, parallelContribs []MemoryStep) StepResult {
	transientMem := r.memoryValues()
	for field, val := range res.StructuredOutput {
		transientMem[field] = renderValue(val)
	}
	// A parallel step's own StructuredOutput is empty — its children's fresh
	// contributions are only merged into r.contrib after this check, so
	// overlay them here (declaration order, last-write-wins) or a gate like
	// `memory.qa_verdict == "rejected"` would read the stale/empty value.
	if step.StepType() == config.StepTypeParallel {
		for _, c := range parallelContribs {
			for field, val := range c.Structured {
				transientMem[field] = renderValue(val)
			}
		}
	}
	evalCtx := EvalContext{Cell: r.cell, Memory: transientMem, Steps: r.stepStates, Event: r.event}

	gate, parseErr := ParseExpr(stripExprDelimiters(step.FailWhen))
	if parseErr != nil {
		res.Success = false
		res.Output = fmt.Sprintf("fail_when eval error %q: %v", step.FailWhen, parseErr)
		aplog.Error("workflow %s: step %q fail_when eval error %q: %v (failing step)", r.wf.ID, step.ID, step.FailWhen, parseErr)
		e.markStepRunFailed(ctx, res.StepRunID, res.Output)
		return res
	}

	// Fail closed on an unevaluable gate, unless the step opted out with
	// on_missing_output: ignore.
	if step.OnMissingOutput != config.OnMissingOutputIgnore {
		if unset := unevaluableGateKeys(gate, step, res, parallelContribs); len(unset) > 0 {
			res.Success = false
			res.Output = fmt.Sprintf("gate %q cannot be evaluated: step declared memory.write key(s) %s but emitted no value for them "+
				"(missing or incomplete APIARY_OUTPUT) — failing closed", step.FailWhen, strings.Join(unset, ", "))
			aplog.Error("workflow %s: step %q gate %q is unevaluable — no value for memory key(s) %s that the step declares in memory.write; failing the step (fail closed, #390)",
				r.wf.ID, step.ID, step.FailWhen, strings.Join(unset, ", "))
			e.recordStepExecutionEvent(ctx, r, step.ID, "step.gate_unevaluable", map[string]any{
				"gate":       step.FailWhen,
				"unset_keys": unset,
			})
			e.markStepRunFailed(ctx, res.StepRunID, res.Output)
			return res
		}
	}

	rejected, evalErr := gate.Eval(evalCtx)
	switch {
	case evalErr != nil:
		res.Success = false
		res.Output = fmt.Sprintf("fail_when eval error %q: %v", step.FailWhen, evalErr)
		aplog.Error("workflow %s: step %q fail_when eval error %q: %v (failing step)", r.wf.ID, step.ID, step.FailWhen, evalErr)
		e.markStepRunFailed(ctx, res.StepRunID, res.Output)
	case rejected:
		res.Success = false
		aplog.Info("workflow %s: step %q rejected (fail_when matched)", r.wf.ID, step.ID)
		e.markStepRunFailed(ctx, res.StepRunID, res.Output)
	}
	return res
}

// unevaluableGateKeys returns the sorted memory keys the gate reads that this
// step promised to write (`memory.write`) but did not emit in its structured
// output. A non-empty result means the gate is reading values the step was
// responsible for producing and did not — the gate cannot decide anything.
//
// Only keys the step itself owns are considered: a gate reading a key written
// by some earlier step is that step's responsibility, and keys that were never
// declared anywhere are an authoring error caught by `apiary validate`.
func unevaluableGateKeys(gate *Expr, step config.StepConfig, res StepResult, parallelContribs []MemoryStep) []string {
	declared := map[string]struct{}{}
	produced := map[string]struct{}{}
	add := func(fields []string, structured map[string]any) {
		for _, f := range fields {
			declared[f] = struct{}{}
		}
		for k := range structured {
			produced[k] = struct{}{}
		}
	}
	if step.StepType() == config.StepTypeParallel {
		// The parallel step emits nothing itself; its children own the keys.
		for _, c := range parallelContribs {
			add(c.WriteFields, c.Structured)
		}
	} else {
		add(step.MemoryWriteFields(), res.StructuredOutput)
	}

	var unset []string
	for _, key := range gate.MemoryRefs() {
		if _, isDeclared := declared[key]; !isDeclared {
			continue
		}
		if _, ok := produced[key]; !ok {
			unset = append(unset, key)
		}
	}
	return unset
}

// applyMissingOutput enforces on_missing_output for a step that declared an
// output schema and finished without emitting APIARY_OUTPUT. It takes the ids
// explicitly instead of a *dagRun so it can also run on a worker goroutine, for
// the children of a parallel or foreach group — those never reach the scheduler
// loop and used to skip the guard entirely: the child was recorded as passed
// with a NULL structured output and nothing was logged (#421).
//
// A no-op for results that are already failed, carry structured output, declare
// no schema, or opt out with on_missing_output: ignore.
func (e *Engine) applyMissingOutput(ctx context.Context, ids runIDs, step config.StepConfig, res StepResult) StepResult {
	if !res.Success || step.OutputSchema == nil || len(res.StructuredOutput) > 0 ||
		step.OnMissingOutput == config.OnMissingOutputIgnore {
		return res
	}
	aplog.Error("workflow %s: step %q declared output_schema but emitted no APIARY_OUTPUT — conditions reading its fields will see empty values", ids.wfID, step.ID)
	// Beyond the log line: record it on the instance so it is visible in
	// the dashboard / event stream, not only in the daemon log (#390).
	e.recordStepExecutionEventFor(ctx, ids, step.ID, "step.missing_output", map[string]any{
		"policy":       missingOutputPolicy(step),
		"memory_write": step.MemoryWriteFields(),
	})
	// on_missing_output: fail — declared output schema required structured output.
	if step.OnMissingOutput == config.OnMissingOutputFail {
		res.Success = false
		res.Output = fmt.Sprintf("on_missing_output=fail: step %q declared an output schema but emitted no APIARY_OUTPUT", step.ID)
		aplog.Info("workflow %s: step %q failed: on_missing_output=fail and no structured output", ids.wfID, step.ID)
		e.markStepRunFailed(ctx, res.StepRunID, res.Output)
	}
	return res
}

// missingOutputPolicy returns the effective on_missing_output policy of a step
// (the empty authored value means the `warn` default).
func missingOutputPolicy(step config.StepConfig) string {
	if step.OnMissingOutput == "" {
		return config.OnMissingOutputWarn
	}
	return step.OnMissingOutput
}

// evalExpr parses and evaluates a condition expression. Strips optional ${{ }}
// wrappers produced by the v2 authoring layer before parsing.
func (e *Engine) evalExpr(src string, ctx EvalContext) (bool, error) {
	src = stripExprDelimiters(src)
	parsed, err := ParseExpr(src)
	if err != nil {
		return false, err
	}
	return parsed.Eval(ctx)
}

// stripExprDelimiters removes the optional "${{ … }}" wrapper from a v2
// expression string, returning the bare expression body.
func stripExprDelimiters(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "${{") && strings.HasSuffix(s, "}}") {
		s = strings.TrimSpace(s[3 : len(s)-2])
	}
	return s
}
