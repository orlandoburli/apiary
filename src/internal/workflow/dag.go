package workflow

import (
	"context"
	"strings"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// step execution states within a DAG run.
const (
	stPending = "pending"
	stRunning = "running" // dispatched to a worker goroutine, not yet complete
	stPassed  = "passed"
	stFailed  = "failed"
	stSkipped = "skipped"
	stWaiting = "waiting" // an approval step parked awaiting a human response
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
	instID string
	wf     config.WorkflowConfig
	cell   model.Cell
	depth  int          // nesting depth (0 = top-level; >0 = sub-workflow child)
	seed   []MemoryStep // inherited memory (sub-workflow snapshot from the parent)

	byID  map[string]config.StepConfig
	order []string // step ids in declaration order (deterministic scheduling)

	state       map[string]string // step id → st* state
	activated   map[string]bool   // control-flow has reached this step
	splitTarget map[string]bool   // step id is the goto target of some split branch
	retries     map[string]int    // on_fail.goto loop counter per failing step

	stepStates  map[string]StepState  // terminal states for expression context
	contrib     map[string]MemoryStep // memory contribution per passed step
	passedOrder []string              // step ids in the order they passed

	waitingStep string    // id of the approval step currently parked, if any
	parkedAt    time.Time // when the current approval parked (for timeout)
}

// initDAG builds the in-memory state for a workflow instance's step graph. seed
// is inherited memory for a sub-workflow child; depth tracks nesting.
func (e *Engine) initDAG(instID string, wf config.WorkflowConfig, cell model.Cell, seed []MemoryStep, depth int) *dagRun {
	r := &dagRun{
		instID:      instID,
		wf:          wf,
		cell:        cell,
		depth:       depth,
		seed:        seed,
		byID:        map[string]config.StepConfig{},
		state:       map[string]string{},
		activated:   map[string]bool{},
		splitTarget: map[string]bool{},
		retries:     map[string]int{},
		stepStates:  map[string]StepState{},
		contrib:     map[string]MemoryStep{},
	}
	for _, s := range wf.Steps {
		r.byID[s.ID] = s
		r.order = append(r.order, s.ID)
		r.state[s.ID] = stPending
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
			if step.Condition != "" {
				evalCtx := EvalContext{Cell: r.cell, Memory: r.memoryValues(), Steps: r.stepStates}
				if e.conditionFalse(step.Condition, evalCtx) {
					r.markSkipped(id)
					aplog.Debug("workflow %s: step %q skipped (condition false)", r.wf.ID, id)
					continue
				}
			}

			// Split: synchronous, no I/O — apply inline on scheduler goroutine.
			if step.StepType() == config.StepTypeSplit {
				e.runSplitStep(r, step)
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
			inFlight++
			go func(step config.StepConfig, memSnap []MemoryStep, contribSnap map[string]MemoryStep) {
				sem <- struct{}{}
				defer func() { <-sem }()
				var res StepResult
				var parallelContribs []MemoryStep
				var foreachExitCode int
				switch step.StepType() {
				case config.StepTypeParallel:
					res, parallelContribs = e.runParallelStep(ctx, r.instID, step, r.cell, memSnap)
				case config.StepTypeForeach:
					var fr foreachResult
					res, fr = e.executeForeachStep(ctx, r.instID, step, r.cell, memSnap, contribSnap, r.wf.ID)
					foreachExitCode = fr.failed
				case config.StepTypeWorkflow:
					res = e.executeSubWorkflowStep(ctx, r.instID, step, r.cell, memSnap, r.depth, r.wf.ID)
				default: // StepTypeAgent
					res = e.runStep(ctx, r.instID, step, r.cell, memSnap)
				}
				resultCh <- workerResult{
					stepID:           step.ID,
					step:             step,
					memSnap:          memSnap,
					res:              res,
					parallelContribs: parallelContribs,
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

		// on_missing_output: fail — declared output schema required structured output.
		if res.Success && step.OutputSchema != nil &&
			step.OnMissingOutput == config.OnMissingOutputFail &&
			len(res.StructuredOutput) == 0 {
			res.Success = false
			aplog.Info("workflow %s: step %q failed: on_missing_output=fail and no structured output", r.wf.ID, step.ID)
		}

		// fail_when — evaluate on the scheduler goroutine after the agent runs.
		if res.Success && step.FailWhen != "" {
			transientMem := r.memoryValues()
			for field, val := range res.StructuredOutput {
				transientMem[field] = renderValue(val)
			}
			evalCtx := EvalContext{Cell: r.cell, Memory: transientMem, Steps: r.stepStates}
			if e.conditionTrue(step.FailWhen, evalCtx) {
				res.Success = false
				aplog.Info("workflow %s: step %q rejected (fail_when matched)", r.wf.ID, step.ID)
			}
		}

		ss := StepState{State: passFail(res.Success), Output: res.Output}
		if step.StepType() == config.StepTypeForeach {
			ss.ExitCode = wr.foreachExitCode
		}
		r.stepStates[step.ID] = ss

		if e.resultCommentMode(r.wf) == config.ResultCommentPerStep && e.side != nil {
			_ = e.side.PostComment(ctx, r.cell, perStepComment(step, res))
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

		// Failure: attempt on_fail.goto loop if retries remain.
		if !termFail && loopTarget == "" &&
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
	return outcomeDone
}

// enterApproval parks the run at an approval step: it posts the step message to
// the task and records the waiting step. The polling loop later resumes or
// aborts it via the engine's approval handling.
func (e *Engine) enterApproval(ctx context.Context, r *dagRun, step config.StepConfig) {
	if e.side != nil && step.Message != "" {
		_ = e.side.PostComment(ctx, r.cell, step.Message)
	}
	r.state[step.ID] = stWaiting
	r.waitingStep = step.ID
	r.parkedAt = e.now()
	aplog.Info("workflow %s: instance %s parked at approval step %q", r.wf.ID, r.instID, step.ID)
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
		if r.state[dep] != stPassed {
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
		// A dependency ended skipped/failed → this step can never run.
		for _, dep := range r.byID[id].DependsOn {
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

// markSkipped sets a step skipped and cascades to pending dependents.
func (r *dagRun) markSkipped(id string) {
	if r.state[id] == stSkipped {
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
	}
}

// runSplitStep evaluates a split's branches and activates the chosen target(s),
// skipping the rest.
func (e *Engine) runSplitStep(r *dagRun, step config.StepConfig) bool {
	ctx := EvalContext{Cell: r.cell, Memory: r.memoryValues(), Steps: r.stepStates}

	chosen := map[string]bool{}
	for _, b := range step.Branches {
		match := b.IsFallback()
		if !match {
			expr, err := ParseExpr(b.If)
			if err != nil {
				aplog.Error("workflow %s: split %q: bad condition %q: %v", r.wf.ID, step.ID, b.If, err)
				continue
			}
			ok, err := expr.Eval(ctx)
			if err != nil {
				aplog.Error("workflow %s: split %q: eval %q: %v", r.wf.ID, step.ID, b.If, err)
				continue
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
	return false
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
	vals := map[string]string{}
	for _, ms := range r.memSteps() {
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

// conditionFalse reports whether expr evaluates to false (or fails to parse/eval).
// Used for per-step condition checks: false → skip the step.
func (e *Engine) conditionFalse(expr string, ctx EvalContext) bool {
	result, err := e.evalExpr(expr, ctx)
	if err != nil {
		aplog.Error("workflow: condition eval error %q: %v (treating as false → skip)", expr, err)
		return true
	}
	return !result
}

// conditionTrue reports whether expr evaluates to true.
// Used for fail_when: true → logical rejection.
func (e *Engine) conditionTrue(expr string, ctx EvalContext) bool {
	result, err := e.evalExpr(expr, ctx)
	if err != nil {
		aplog.Error("workflow: fail_when eval error %q: %v (treating as false → not rejected)", expr, err)
		return false
	}
	return result
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
