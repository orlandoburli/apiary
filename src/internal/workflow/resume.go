package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	aplog "github.com/orlandoburli/apiary/internal/log"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// ResumeInstance creates an immutable descendant of a failed or interrupted
// instance, restores eligible passed steps from cache, and runs the rest.
//
// priorSteps are the instance's persisted step runs in execution order. Split
// steps are intentionally re-evaluated (they are side-effect-free and their
// branch routing must re-activate the chosen target), while agent, foreach,
// sub-workflow, and approval steps that passed are carried over untouched.
func (e *Engine) ResumeInstance(ctx context.Context, source *db.WorkflowInstance, wf config.WorkflowConfig, task model.InternalTask, priorSteps []db.StepRun, fromStep, instanceID string) (string, bool, error) {
	instID := instanceID
	if instID == "" {
		instID = e.NewInstanceID()
	}
	inst := &db.WorkflowInstance{
		ID:          instID,
		WorkflowID:  source.WorkflowID,
		TaskID:      source.TaskID,
		CellID:      source.CellID,
		SourceID:    source.SourceID,
		State:       db.InstanceStateRunning,
		ResumedFrom: source.ID,
		CreatedAt:   e.now(),
	}
	if err := e.store.CreateWorkflowInstance(ctx, inst); err != nil {
		return "", false, err
	}
	if err := e.persistWorkflowSnapshot(ctx, instID, wf); err != nil {
		_ = e.store.UpdateWorkflowInstanceState(ctx, instID, db.InstanceStateFailed)
		return "", false, err
	}

	bindings := e.bindingsFor(ctx, task.ID)
	r := e.initDAG(instID, wf, task, bindings, nil, 0)
	selected, err := resumeCacheSelection(wf, priorSteps, fromStep)
	if err != nil {
		return "", false, err
	}
	if err := e.seedResume(ctx, r, selected); err != nil {
		_ = e.store.UpdateWorkflowInstanceState(ctx, instID, db.InstanceStateFailed)
		return "", false, err
	}

	aplog.Info("workflow %s: resuming instance %s from %s (%d cached step(s))", wf.ID, instID, source.ID, len(r.passedOrder))
	outcome := e.driveDAG(ctx, r)
	return instID, e.settle(ctx, r, outcome), nil
}

// seedResume restores the graph state of already-passed steps so a resumed run
// continues from the first incomplete step, and marks each carried-over step run
// as skipped_cached (display only). It returns nothing; on completion
// r.passedOrder/contrib/stepStates reflect the cached steps.
func (e *Engine) seedResume(ctx context.Context, r *dagRun, priorSteps []db.StepRun) error {
	for _, source := range e.restoreCachedSteps(r, priorSteps) {
		cached := source
		cached.ID = e.newID("sr")
		cached.WorkflowInstanceID = r.instID
		cached.SkippedCached = true
		if err := e.store.CreateStepRun(ctx, &cached); err != nil {
			return err
		}
	}
	return nil
}

// resumeCacheSelection returns passed rows eligible for reuse. When fromStep is
// set, only rows for steps declared before it are reused; the selected step and
// everything after it run again. Split steps remain in the list but are ignored
// by restoreCachedSteps so branch routing is always re-evaluated.
func resumeCacheSelection(wf config.WorkflowConfig, prior []db.StepRun, fromStep string) ([]db.StepRun, error) {
	if fromStep == "" {
		return prior, nil
	}
	limit := -1
	order := make(map[string]int, len(wf.Steps))
	for i, step := range wf.Steps {
		order[step.ID] = i
		if step.ID == fromStep {
			limit = i
		}
	}
	if limit < 0 {
		return nil, fmt.Errorf("resume step %q not found in workflow %q", fromStep, wf.ID)
	}
	out := make([]db.StepRun, 0, len(prior))
	for _, sr := range prior {
		if i, ok := order[sr.StepID]; ok && i < limit {
			out = append(out, sr)
		}
	}
	return out, nil
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
			// A parallel group's child is not a graph node of its own. Restore it
			// into the parent's memoized child results instead, so a group
			// rehydrated mid-wait re-polls only its wait_for child and never
			// re-runs the sibling that already passed (#425).
			if child, isChild := r.childByID[sr.StepID]; isChild {
				restoreParallelChild(r, child, sr)
			}
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

// restoreParallelChild memoizes one passed child of a parallel group from its
// persisted step run, so the group can be resumed without re-running it.
func restoreParallelChild(r *dagRun, child config.StepConfig, sr db.StepRun) {
	parent, ok := r.parentOfChild[child.ID]
	if !ok {
		return
	}
	var structured map[string]any
	if sr.StructuredOutput != "" {
		_ = json.Unmarshal([]byte(sr.StructuredOutput), &structured)
	}
	if r.parallelDone[parent] == nil {
		r.parallelDone[parent] = map[string]StepResult{}
	}
	r.parallelDone[parent][child.ID] = StepResult{
		Success:          true,
		Output:           sr.Output,
		Summary:          sr.Summary,
		StructuredOutput: structured,
	}
}
