package workflow

import (
	"context"
	"fmt"

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

// runParallelStep executes a StepTypeParallel node's children concurrently and
// applies the join policy. Children are NOT subject to the global semaphore —
// the parallel step itself occupies one concurrency slot; its children run
// freely within that slot. Returns the aggregate StepResult and the memory
// contributions of passed children (in declaration order) to be merged into the
// outer DAG's contrib map.
//
// runParallelStep is called from a worker goroutine and must NOT touch dagRun.
func (e *Engine) runParallelStep(
	ctx context.Context, instID string,
	step config.StepConfig, cell model.SourceItem,
	task model.InternalTask, bindings []model.SourceBinding,
	memSnap []MemoryStep, wfEnv map[string]string, publishAllowList []string,
) (StepResult, []MemoryStep) {
	children := step.SubSteps
	if len(children) == 0 {
		return StepResult{Success: true, Summary: "parallel: no children"}, nil
	}

	resultCh := make(chan parallelChildResult, len(children))

	for i, child := range children {
		go func(i int, child config.StepConfig) {
			res := e.runStep(ctx, instID, child, cell, task, bindings, memSnap, wfEnv, publishAllowList)
			resultCh <- parallelChildResult{idx: i, step: child, res: res}
		}(i, child)
	}

	// Collect results (arrival order → sort by idx for determinism).
	results := make([]parallelChildResult, len(children))
	for range children {
		cr := <-resultCh
		results[cr.idx] = cr
	}

	// Apply join policy. A join expression that cannot be parsed or evaluated
	// fails the parallel step — silently falling back to "all" would let an
	// unintended join policy decide the outcome (#180).
	passed, joinErr := applyJoinPolicy(step.Join, results, cell, memSnap)
	if joinErr != nil {
		aplog.Error("workflow: parallel %q join eval error %q: %v (failing step)", step.ID, step.Join, joinErr)
		return StepResult{Success: false, Output: fmt.Sprintf("join eval error %q: %v", step.Join, joinErr)}, nil
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
		return StepResult{Success: true, Summary: "parallel: joined"}, contribs
	}
	return StepResult{Success: false}, nil
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
