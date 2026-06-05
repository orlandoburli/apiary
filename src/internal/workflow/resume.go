package workflow

import (
	"context"
	"encoding/json"

	aplog "github.com/orlandoburli/apiary/internal/log"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// ResumeInstance continues an existing failed or interrupted instance by
// replaying its already-passed steps from cache and running the rest. It reuses
// the instance id, restores cached steps' memory and expression context without
// re-executing them or re-firing their side effects, and drives the DAG for the
// remaining steps.
//
// priorSteps are the instance's persisted step runs in execution order. Split
// steps are intentionally re-evaluated (they are side-effect-free and their
// branch routing must re-activate the chosen target), while agent, foreach,
// sub-workflow, and approval steps that passed are carried over untouched.
func (e *Engine) ResumeInstance(ctx context.Context, instID string, wf config.WorkflowConfig, cell model.Cell, priorSteps []db.StepRun) (success bool, err error) {
	_ = e.store.UpdateWorkflowInstanceState(ctx, instID, db.InstanceStateRunning)

	r := e.initDAG(instID, wf, cell, nil, 0)
	e.seedResume(ctx, r, priorSteps)

	aplog.Info("workflow %s: resuming instance %s (%d cached step(s))", wf.ID, instID, len(r.passedOrder))
	outcome := e.driveDAG(ctx, r)
	return e.settle(ctx, r, outcome), nil
}

// seedResume restores the graph state of already-passed steps so a resumed run
// continues from the first incomplete step. It returns nothing; on completion
// r.passedOrder/contrib/stepStates reflect the cached steps.
func (e *Engine) seedResume(ctx context.Context, r *dagRun, priorSteps []db.StepRun) {
	seen := map[string]bool{}
	for i := range priorSteps {
		sr := priorSteps[i]
		if sr.State != db.StepStatePassed {
			continue // failed/running/pending steps re-run
		}
		step, ok := r.byID[sr.StepID]
		if !ok {
			continue // step no longer exists in the workflow definition
		}
		// Splits carry no side effects and must re-route; let driveDAG re-run
		// them so the chosen branch target is re-activated against restored
		// memory. Leaving them pending is correct and free.
		if step.StepType() == config.StepTypeSplit {
			continue
		}

		var structured map[string]any
		if sr.StructuredOutput != "" {
			_ = json.Unmarshal([]byte(sr.StructuredOutput), &structured)
		}

		r.state[step.ID] = stPassed
		r.stepStates[step.ID] = StepState{State: stPassed, Output: sr.Output}
		if !seen[step.ID] {
			r.contrib[step.ID] = MemoryStep{
				StepID:      step.ID,
				WriteFields: step.MemoryWriteFields(),
				Structured:  structured,
				Summary:     sr.Summary,
			}
			r.passedOrder = append(r.passedOrder, step.ID)
			seen[step.ID] = true
		}
		if step.OnPass != nil && step.OnPass.Next != "" {
			r.activated[step.OnPass.Next] = true
		}

		// Record that this run was carried over a resume (display only).
		if !sr.SkippedCached {
			sr.SkippedCached = true
			_ = e.store.UpdateStepRun(ctx, &sr)
		}
	}
}
