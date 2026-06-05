package workflow

import (
	"context"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// step execution states within a DAG run.
const (
	stPending = "pending"
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

// driveDAG runs the scheduler until the graph completes, fails, or suspends at
// an approval step. It honors depends_on ordering, split routing, on_fail.goto
// loops, and skip propagation, and may be re-entered after an approval resolves.
func (e *Engine) driveDAG(ctx context.Context, r *dagRun) dagOutcome {
	for {
		next := r.pickRunnable()
		if next == "" {
			if r.skipUnreachable() {
				continue
			}
			break // nothing else can run
		}
		step := r.byID[next]
		if step.StepType() == config.StepTypeApproval {
			e.enterApproval(ctx, r, step)
			return outcomeWaiting
		}
		if failed := e.runDAGStep(ctx, r, next); failed {
			return outcomeFailed
		}
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

// pickRunnable returns the next runnable step id (activated, pending, all
// dependencies passed), in declaration order, or "" when none is ready.
func (r *dagRun) pickRunnable() string {
	for _, id := range r.order {
		if r.state[id] != stPending || !r.activated[id] {
			continue
		}
		if r.depsPassed(id) {
			return id
		}
	}
	return ""
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

// runDAGStep executes one step and updates graph state. Returns true if the
// instance must fail (an agent step failed with no retry left).
func (e *Engine) runDAGStep(ctx context.Context, r *dagRun, id string) bool {
	step := r.byID[id]
	switch step.StepType() {
	case config.StepTypeSplit:
		return e.runSplitStep(r, step)
	case config.StepTypeAgent:
		return e.runAgentDAGStep(ctx, r, step)
	case config.StepTypeForeach:
		return e.runForeachStep(ctx, r, step)
	case config.StepTypeWorkflow:
		return e.runSubWorkflowStep(ctx, r, step)
	default:
		// approval steps are not executed by this scheduler yet; treat as a
		// no-op pass so they don't block the graph.
		aplog.Debug("workflow %s: step %q type %q not yet executable, passing through", r.wf.ID, step.ID, step.StepType())
		r.state[id] = stPassed
		return false
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

// runAgentDAGStep runs an agent step, threading memory, and handles on_pass.next
// activation and on_fail.goto loops.
func (e *Engine) runAgentDAGStep(ctx context.Context, r *dagRun, step config.StepConfig) bool {
	res := e.runStep(ctx, r.instID, step, r.cell, r.memSteps())

	r.stepStates[step.ID] = StepState{
		State:  passFail(res.Success),
		Output: res.Output,
	}

	if e.resultCommentMode(r.wf) == config.ResultCommentPerStep && e.side != nil {
		_ = e.side.PostComment(ctx, r.cell, perStepComment(step, res))
	}

	if res.Success {
		r.state[step.ID] = stPassed
		r.contrib[step.ID] = MemoryStep{
			StepID:      step.ID,
			WriteFields: step.MemoryWriteFields(),
			Structured:  res.StructuredOutput,
			Summary:     res.Summary,
		}
		r.passedOrder = append(r.passedOrder, step.ID)
		if step.OnPass != nil && step.OnPass.Next != "" {
			r.activated[step.OnPass.Next] = true
		}
		return false
	}

	// Failure: attempt an on_fail.goto loop if retries remain.
	if step.OnFail != nil && step.OnFail.Goto != "" {
		if r.retries[step.ID] < step.OnFail.MaxRetries {
			r.retries[step.ID]++
			aplog.Info("workflow %s: step %q failed, looping back to %q (retry %d/%d)",
				r.wf.ID, step.ID, step.OnFail.Goto, r.retries[step.ID], step.OnFail.MaxRetries)
			r.resetLoop(step.OnFail.Goto)
			return false
		}
		aplog.Info("workflow %s: step %q exhausted %d retries", r.wf.ID, step.ID, step.OnFail.MaxRetries)
	}

	r.state[step.ID] = stFailed
	return true
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
	// Rebuild passedOrder excluding reset steps.
	kept := r.passedOrder[:0]
	for _, id := range r.passedOrder {
		if !reset[id] {
			kept = append(kept, id)
		}
	}
	r.passedOrder = kept
}

// memSteps returns the ordered memory contributions: inherited seed (for a
// sub-workflow) first, then this run's passed steps.
func (r *dagRun) memSteps() []MemoryStep {
	out := make([]MemoryStep, 0, len(r.seed)+len(r.passedOrder))
	out = append(out, r.seed...)
	for _, id := range r.passedOrder {
		if c, ok := r.contrib[id]; ok {
			out = append(out, c)
		}
	}
	return out
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
