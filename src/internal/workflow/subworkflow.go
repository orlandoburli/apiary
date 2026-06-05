package workflow

import (
	"context"
	"fmt"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
)

// maxSubWorkflowDepth bounds sub-workflow nesting at runtime. Config validation
// already forbids nesting (a referenced workflow may not contain workflow
// steps), so depth 1 is the only legal level; this is a backstop.
const maxSubWorkflowDepth = 1

// runSubWorkflowStep executes a `type: workflow` step by running the referenced
// workflow as a linked child instance, seeded with the parent's current memory.
// The parent step passes iff the child instance completes successfully. The
// child's memory is NOT merged back into the parent (sub-workflows are isolated).
func (e *Engine) runSubWorkflowStep(ctx context.Context, r *dagRun, step config.StepConfig) bool {
	if r.depth >= maxSubWorkflowDepth {
		aplog.Error("workflow %s: step %q: sub-workflow nesting beyond depth %d is not allowed",
			r.wf.ID, step.ID, maxSubWorkflowDepth)
		r.failStep(step.ID)
		return true
	}

	child := e.findWorkflow(step.Workflow)
	if child == nil {
		aplog.Error("workflow %s: step %q: referenced workflow %q not found", r.wf.ID, step.ID, step.Workflow)
		r.failStep(step.ID)
		return true
	}

	seed := r.memSteps() // snapshot of the parent's memory at this point
	_, success := e.runChildInstance(ctx, r.instID, *child, r.cell, seed, r.depth+1)

	r.state[step.ID] = passFail(success)
	r.stepStates[step.ID] = StepState{State: passFail(success)}
	if success {
		r.contrib[step.ID] = MemoryStep{
			StepID:  step.ID,
			Summary: fmt.Sprintf("sub-workflow %q completed", child.ID),
		}
		r.passedOrder = append(r.passedOrder, step.ID)
		return false
	}
	return true
}

// runChildInstance creates and runs a linked child workflow instance. It does
// not apply state_lock or result_comment (those belong to the top-level
// instance) and does not apply the child's on_complete/on_fail hooks against the
// shared cell — the child is an isolated pipeline whose only outward signal is
// success/failure.
func (e *Engine) runChildInstance(ctx context.Context, parentInstID string, child config.WorkflowConfig, cell model.Cell, seed []MemoryStep, depth int) (string, bool) {
	childID := e.newID("wf")
	inst := &db.WorkflowInstance{
		ID:               childID,
		WorkflowID:       child.ID,
		CellID:           cell.ID,
		SourceID:         cell.SourceID,
		State:            db.InstanceStateRunning,
		ParentInstanceID: parentInstID,
		CreatedAt:        e.now(),
	}
	if err := e.store.CreateWorkflowInstance(ctx, inst); err != nil {
		aplog.Error("sub-workflow %s: create child instance: %v", child.ID, err)
		return "", false
	}

	failed, _ := e.runDAG(ctx, childID, child, cell, seed, depth)

	finalState := db.InstanceStateDone
	if failed {
		finalState = db.InstanceStateFailed
	}
	_ = e.store.UpdateWorkflowInstanceState(ctx, childID, finalState)
	return childID, !failed
}

// findWorkflow looks up a workflow definition by ID in the config.
func (e *Engine) findWorkflow(id string) *config.WorkflowConfig {
	for i := range e.cfg.Workflows {
		if e.cfg.Workflows[i].ID == id {
			return &e.cfg.Workflows[i]
		}
	}
	return nil
}
