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

// executeSubWorkflowStep runs a sub-workflow step without touching dagRun; it is
// safe to call from a worker goroutine. memSnap is a snapshot of the parent's
// memory taken on the scheduler goroutine before dispatch.
func (e *Engine) executeSubWorkflowStep(
	ctx context.Context, parentInstID string,
	step config.StepConfig, cell model.SourceItem,
	memSnap []MemoryStep, depth int, wfID string,
) StepResult {
	if depth >= maxSubWorkflowDepth {
		aplog.Error("workflow %s: step %q: sub-workflow nesting beyond depth %d is not allowed",
			wfID, step.ID, maxSubWorkflowDepth)
		return StepResult{Success: false, Output: "sub-workflow nesting limit exceeded"}
	}

	child := e.findWorkflow(step.Workflow)
	if child == nil {
		aplog.Error("workflow %s: step %q: referenced workflow %q not found", wfID, step.ID, step.Workflow)
		return StepResult{Success: false, Output: fmt.Sprintf("workflow %q not found", step.Workflow)}
	}

	_, success := e.runChildInstance(ctx, parentInstID, *child, cell, memSnap, depth+1)
	summary := ""
	if success {
		summary = fmt.Sprintf("sub-workflow %q completed", child.ID)
	}
	return StepResult{Success: success, Summary: summary}
}

// runChildInstance creates and runs a linked child workflow instance. It does
// not apply state_lock or result_comment (those belong to the top-level
// instance) and does not apply the child's on_complete/on_fail hooks against the
// shared cell — the child is an isolated pipeline whose only outward signal is
// success/failure.
func (e *Engine) runChildInstance(ctx context.Context, parentInstID string, child config.WorkflowConfig, cell model.SourceItem, seed []MemoryStep, depth int) (string, bool) {
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

	r := e.initDAG(childID, child, cell, seed, depth)
	outcome := e.driveDAG(ctx, r)
	// A sub-workflow cannot park independently in Phase 4: an approval step inside
	// a child is treated as a failure (unsupported). Top-level approvals are the
	// supported case.
	if outcome == outcomeWaiting {
		aplog.Error("sub-workflow %s: approval steps inside a sub-workflow are not supported", child.ID)
		outcome = outcomeFailed
	}

	finalState := db.InstanceStateDone
	if outcome == outcomeFailed {
		finalState = db.InstanceStateFailed
	}
	_ = e.store.UpdateWorkflowInstanceState(ctx, childID, finalState)
	return childID, outcome == outcomeDone
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
