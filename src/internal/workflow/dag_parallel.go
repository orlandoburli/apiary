package workflow

import (
	"context"
	"fmt"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// parallelChildResult is the outcome of one child within a parallel step.
type parallelChildResult struct {
	idx  int
	step config.StepConfig
	res  StepResult
}

// parallelState is the per-child bookkeeping a parallel step hands back to the
// scheduler. It exists because a wait_for child parks: the group cannot finish
// in one pass, so the scheduler must remember which child is waiting and what
// the other children already produced, and hand both back when the wait wakes.
//
//   - waitingChild is the id of the wait_for child that returned Pending (empty
//     when the group reached a terminal join).
//   - done are the terminal results of the children that did finish, keyed by
//     child id. They are replayed as-is on the next pass, so a code review
//     sibling is never re-run just because CI is still going (#425).
type parallelState struct {
	waitingChild string
	done         map[string]StepResult
}

// runParallelStep executes a StepTypeParallel node's children concurrently and
// applies the join policy. Children are NOT subject to the global semaphore —
// the parallel step itself occupies one concurrency slot; its children run
// freely within that slot. Returns the aggregate StepResult, the memory
// contributions of passed children (in declaration order) to be merged into the
// outer DAG's contrib map, and the parallelState the scheduler needs to park
// and resume the group.
//
// Each child is dispatched by ITS OWN step type, not blindly as an agent step:
// a wait_for child runs the CI/dependency check and can leave the group pending
// (before #425 every child went through runStep, so a wait_for child ran as an
// agent step with an empty agent: and failed in milliseconds with no
// diagnostic, taking its passing siblings down with it under join: all).
//
// cached carries the results of children that already completed on an earlier
// pass (see parallelState.done); those children are not re-run.
//
// runParallelStep is called from a worker goroutine and must NOT touch dagRun.
func (e *Engine) runParallelStep(
	ctx context.Context, instID string,
	step config.StepConfig, cell model.SourceItem,
	task model.InternalTask, bindings []model.SourceBinding,
	memSnap []MemoryStep, wfID string, wfEnv map[string]string,
	cached map[string]StepResult, waitDeadline time.Time,
) (StepResult, []MemoryStep, parallelState) {
	children := step.SubSteps
	if len(children) == 0 {
		return StepResult{Success: true, Summary: "parallel: no children"}, nil, parallelState{}
	}

	resultCh := make(chan parallelChildResult, len(children))

	pending := 0
	results := make([]parallelChildResult, len(children))
	for i, child := range children {
		// Replay a child that already finished on an earlier pass of this group
		// (i.e. before a sibling wait_for parked the instance).
		if res, ok := cached[child.ID]; ok {
			results[i] = parallelChildResult{idx: i, step: child, res: res}
			continue
		}
		pending++
		go func(i int, child config.StepConfig) {
			res := e.runParallelChild(ctx, instID, child, cell, task, bindings, memSnap, wfEnv, waitDeadline)
			// Children never pass through the scheduler loop, so the
			// on_missing_output guard has to be applied here (#421). A pending
			// wait_for child is not successful yet, so the guard skips it.
			res = e.applyMissingOutput(ctx, runIDs{taskID: task.ID, wfID: wfID, instID: instID}, child, res)
			resultCh <- parallelChildResult{idx: i, step: child, res: res}
		}(i, child)
	}

	// Collect results (arrival order → sort by idx for determinism).
	for range pending {
		cr := <-resultCh
		results[cr.idx] = cr
	}

	// A wait_for child with no answer yet parks the whole group — unless the
	// join is already decided by the children that did finish (join: all with a
	// failure, join: any with a pass), in which case waiting hours for CI would
	// only delay a foregone outcome.
	state := parallelState{done: map[string]StepResult{}}
	for _, cr := range results {
		if cr.res.Pending {
			state.waitingChild = cr.step.ID
			continue
		}
		state.done[cr.step.ID] = cr.res
	}
	if state.waitingChild != "" {
		if decided, ok := joinDecided(step.Join, results); ok {
			aplog.Info("workflow: parallel %q: join already decided (%s) — not waiting on %q",
				step.ID, passFail(decided), state.waitingChild)
		} else {
			return StepResult{Pending: true}, nil, state
		}
	}

	// Apply join policy. A join expression that cannot be parsed or evaluated
	// fails the parallel step — silently falling back to "all" would let an
	// unintended join policy decide the outcome (#180).
	passed, joinErr := applyJoinPolicy(step.Join, results, cell, memSnap)
	if joinErr != nil {
		aplog.Error("workflow: parallel %q join eval error %q: %v (failing step)", step.ID, step.Join, joinErr)
		return StepResult{Success: false, Output: fmt.Sprintf("join eval error %q: %v", step.Join, joinErr)}, nil, state
	}

	// Collect memory contributions from passed children in declaration order.
	var contribs []MemoryStep
	for _, cr := range results {
		if cr.res.Success {
			contribs = append(contribs, MemoryStep{
				StepID:      cr.step.ID,
				WriteFields: cr.step.MemoryWriteFields(),
				Structured:  cr.res.StructuredOutput,
				Summary:     cr.res.Summary,
			})
		}
	}

	if passed {
		return StepResult{Success: true, Summary: "parallel: joined"}, contribs, state
	}
	return StepResult{Success: false}, nil, state
}

// runParallelChild executes one child of a parallel group by its own step type.
// Agent children take the normal runStep path; a wait_for child performs one
// external check and may come back Pending, which parks the whole group.
//
// The step types a child may NOT have (approval, foreach, sub-workflow, a
// nested group) are rejected by config validation, so reaching the default arm
// means a config slipped through the lint: fail loudly and name the type rather
// than running it as an agent step.
func (e *Engine) runParallelChild(
	ctx context.Context, instID string,
	child config.StepConfig, cell model.SourceItem,
	task model.InternalTask, bindings []model.SourceBinding,
	memSnap []MemoryStep, wfEnv map[string]string, waitDeadline time.Time,
) StepResult {
	switch child.StepType() {
	case config.StepTypeAgent:
		return e.runStep(ctx, instID, child, cell, task, bindings, memSnap, wfEnv)
	case config.StepTypeWaitFor:
		res, err := e.RunWaitStep(ctx, instID, child, WaitTarget{TaskID: task.ID, SourceID: cell.SourceID, SourceItemID: cell.ID}, waitDeadline)
		if err != nil {
			aplog.Error("workflow: parallel child %q: wait_for step failed: %v", child.ID, err)
			return StepResult{Success: false, Output: err.Error(), Err: err}
		}
		if res.Err != nil {
			aplog.Error("workflow: parallel child %q: wait_for step failed: %v", child.ID, res.Err)
		}
		return res
	default:
		err := fmt.Errorf("step type %q is not supported inside a parallel group", child.StepType())
		aplog.Error("workflow: parallel child %q: %v", child.ID, err)
		return StepResult{Success: false, Output: err.Error(), Err: err}
	}
}

// joinDecided reports whether the join outcome is already settled by the
// children that finished, ignoring any child still pending. It lets a group
// whose fate is sealed stop waiting on a long CI poll: under join: all one
// failure decides it, under join: any one pass does. A join expression is
// never treated as decided — it may read any child's output, so it is only
// evaluated once every child is terminal.
func joinDecided(join string, results []parallelChildResult) (passed bool, ok bool) {
	switch join {
	case config.JoinAny:
		for _, cr := range results {
			if cr.res.Success {
				return true, true
			}
		}
	case "", config.JoinAll:
		for _, cr := range results {
			if !cr.res.Pending && !cr.res.Success {
				return false, true
			}
		}
	}
	return false, false
}

// applyJoinPolicy evaluates the join policy over the child results.
// "all" (the default) requires all children to pass; "any" requires at least
// one to pass. Any other value is a condition expression (optionally
// ${{ }}-wrapped) evaluated with the standard expression language: the
// children's outcomes are exposed as steps.<child-id>.* alongside the usual
// cell.* and memory.* accessors. An expression that fails to parse or
// evaluate returns an error — the caller fails the parallel step (#180).
func applyJoinPolicy(join string, results []parallelChildResult, cell model.SourceItem, memSnap []MemoryStep) (bool, error) {
	switch join {
	case config.JoinAny:
		for _, cr := range results {
			if cr.res.Success {
				return true, nil
			}
		}
		return false, nil
	case "", config.JoinAll:
		return allChildrenPassed(results), nil
	default:
		expr, err := ParseExpr(stripExprDelimiters(join))
		if err != nil {
			return false, err
		}
		steps := make(map[string]StepState, len(results))
		for _, cr := range results {
			steps[cr.step.ID] = StepState{State: passFail(cr.res.Success), Output: cr.res.Output}
		}
		return expr.Eval(EvalContext{Cell: cell, Memory: memoryValuesFrom(memSnap), Steps: steps})
	}
}

// allChildrenPassed implements the "all" join: every child must pass.
func allChildrenPassed(results []parallelChildResult) bool {
	for _, cr := range results {
		if !cr.res.Success {
			return false
		}
	}
	return true
}

// Silence unused-import warning when model is only used for the function signature.
var _ model.SourceItem
