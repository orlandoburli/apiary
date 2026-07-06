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
func (e *Engine) ResumeInstance(ctx context.Context, instID string, wf config.WorkflowConfig, task model.InternalTask, priorSteps []db.StepRun) (success bool, err error) {
	_ = e.store.UpdateWorkflowInstanceState(ctx, instID, db.InstanceStateRunning)

	bindings := e.bindingsFor(ctx, task.ID)
	r := e.initDAG(instID, wf, task, bindings, nil, 0)
	e.seedResume(ctx, r, priorSteps)

	aplog.Info("workflow %s: resuming instance %s (%d cached step(s))", wf.ID, instID, len(r.passedOrder))
	outcome := e.driveDAG(ctx, r)
	return e.settle(ctx, r, outcome), nil
}

// seedResume restores the graph state of already-passed steps so a resumed run
// continues from the first incomplete step, and marks each carried-over step run
// as skipped_cached (display only). It returns nothing; on completion
// r.passedOrder/contrib/stepStates reflect the cached steps.
func (e *Engine) seedResume(ctx context.Context, r *dagRun, priorSteps []db.StepRun) {
	for _, sr := range e.restoreCachedSteps(r, priorSteps) {
		// Record that this run was carried over a resume (display only).
		if !sr.SkippedCached {
			sr.SkippedCached = true
			_ = e.store.UpdateStepRun(ctx, &sr)
		}
	}
}

// restoreCachedSteps replays already-passed steps into the in-memory graph so a
// run continues from the first incomplete step. It is purely in-memory and
// persists nothing, so it is safe both for resume (which then marks the
// carried-over steps skipped_cached) and for rehydrating an approval-parked
// instance after a restart (which must NOT rewrite step-run display flags). It
// returns the passed step runs it restored, in input order (including duplicates
// produced by on_fail.goto retry loops), so the caller can mark them if desired.
//
// Splits carry no side effects and must re-route; they are left pending so
// driveDAG re-runs them and re-activates the chosen branch target against the
// restored memory. Failed/running/pending steps re-run.
func (e *Engine) restoreCachedSteps(r *dagRun, priorSteps []db.StepRun) []db.StepRun {
	seen := map[string]bool{}
	var restored []db.StepRun
	for i := range priorSteps {
		sr := priorSteps[i]
		if sr.State != db.StepStatePassed {
			continue // failed/running/pending steps re-run
		}
		step, ok := r.byID[sr.StepID]
		if !ok {
			continue // step no longer exists in the workflow definition
		}
		if step.StepType() == config.StepTypeSplit {
			continue
		}

		var structured map[string]any
		if sr.StructuredOutput != "" {
			_ = json.Unmarshal([]byte(sr.StructuredOutput), &structured)
		}

		r.state[step.ID] = stPassed
		r.stepStates[step.ID] = StepState{State: stPassed, Output: sr.Output}
		// Last passed run wins: an on_fail.goto / restart_from loop re-runs a step,
		// and downstream conditions must see the newest attempt's output. Keeping
		// the first run's memory here made a restored `if` read stale fields (e.g.
		// an implement re-run that emitted already_done was invisible after a
		// rehydrate, so the noop exit never fired and the issue looped).
		r.contrib[step.ID] = MemoryStep{
			StepID:      step.ID,
			WriteFields: step.MemoryWriteFields(),
			Structured:  structured,
			Summary:     sr.Summary,
		}
		if !seen[step.ID] {
			r.passedOrder = append(r.passedOrder, step.ID)
			seen[step.ID] = true
		}
		if step.OnPass != nil && step.OnPass.Next != "" {
			r.activated[step.OnPass.Next] = true
		}

		restored = append(restored, sr)
	}
	return restored
}
