package workflow

import (
	"context"

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
	step config.StepConfig, cell model.Cell,
	memSnap []MemoryStep,
) (StepResult, []MemoryStep) {
	children := step.SubSteps
	if len(children) == 0 {
		return StepResult{Success: true, Summary: "parallel: no children"}, nil
	}

	resultCh := make(chan parallelChildResult, len(children))

	for i, child := range children {
		go func(i int, child config.StepConfig) {
			res := e.runStep(ctx, instID, child, cell, memSnap)
			resultCh <- parallelChildResult{idx: i, step: child, res: res}
		}(i, child)
	}

	// Collect results (arrival order → sort by idx for determinism).
	results := make([]parallelChildResult, len(children))
	for range children {
		cr := <-resultCh
		results[cr.idx] = cr
	}

	// Apply join policy.
	join := step.Join
	if join == "" {
		join = "all"
	}
	passed := applyJoinPolicy(join, results)

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
// "all" requires all children to pass; "any" requires at least one to pass.
// An expression join (${{ … }}) is not yet supported and defaults to "all".
func applyJoinPolicy(join string, results []parallelChildResult) bool {
	switch join {
	case "any":
		for _, cr := range results {
			if cr.res.Success {
				return true
			}
		}
		return false
	default: // "all" or expression (expression support deferred)
		for _, cr := range results {
			if !cr.res.Success {
				return false
			}
		}
		return true
	}
}

// Silence unused-import warning when model is only used for the function signature.
var _ model.Cell
