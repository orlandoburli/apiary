package workflow

import (
	"context"
	"errors"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
)

// ParkedWait describes an instance suspended at a wait_for step waiting for an
// external signal (e.g. CI completion).
type ParkedWait struct {
	InstanceID string
	Task       model.InternalTask
	Step       config.StepConfig
	Deadline   time.Time // absolute give-up time (zero = none)
}

// parkedWaits returns a snapshot of all instances currently suspended at a poll
// step. The parked set is shared with approval-waiting instances, so it filters to
// runs whose waiting step is a poll.
func (e *Engine) parkedWaits() []ParkedWait {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]ParkedWait, 0, len(e.parked))
	for id, r := range e.parked {
		if r.byID[r.waitingStep].StepType() != config.StepTypeWaitFor {
			continue
		}
		out = append(out, ParkedWait{
			InstanceID: id,
			Task:       r.task,
			Step:       r.byID[r.waitingStep],
			Deadline:   r.waitDeadline,
		})
	}
	return out
}

// CheckParkedWaits re-checks every instance suspended at a wait_for step by waking it:
// the woken run re-executes the wait_for step (a single CI query via the engine's
// CIStatusChecker) and either advances past it (CI passed), fails it (CI failed or
// the deadline elapsed, which drives on_reject/on_fail loop-back), or re-parks it
// (CI still pending). Called once per dispatcher poll cycle, mirroring
// CheckParkedApprovals. The poll cadence is therefore the dispatcher's poll
// interval; a step's check_interval acts as a lower bound only.
func (e *Engine) CheckParkedWaits(ctx context.Context) {
	for _, p := range e.parkedWaits() {
		e.wakeWait(ctx, p.InstanceID)
	}
}

// wakeWait resumes a wait-parked instance: it resets the waiting wait_for step to
// pending so driveDAG re-dispatches it (a single CI check), then drives the graph
// to its next terminal or suspended state and settles it. A pending check re-parks
// the instance (via enterWait, preserving its deadline); a pass/fail advances or
// loops the workflow through the normal result-processing path. It is a no-op if
// the instance is no longer parked.
func (e *Engine) wakeWait(ctx context.Context, instanceID string) {
	e.mu.Lock()
	r, ok := e.parked[instanceID]
	if ok {
		delete(e.parked, instanceID)
	}
	e.mu.Unlock()
	if !ok {
		return
	}

	step := r.waitingStep
	r.waitingStep = ""
	r.state[step] = stPending // re-arm the wait_for step for re-dispatch

	_ = e.store.UpdateWorkflowInstanceState(ctx, instanceID, db.InstanceStateRunning)
	outcome := e.driveDAG(ctx, r)
	e.settle(ctx, r, outcome)
}

// ErrNoWaitStep is returned by RehydrateWait when the instance has no wait_for step
// waiting once its cached steps are restored — i.e. it was not actually parked at a
// poll (a stale or malformed waiting row). The caller leaves it for manual
// reconciliation rather than re-parking it.
var ErrNoWaitStep = errors.New("no wait_for step is waiting")

// RehydrateWait reconstructs an instance persisted in the waiting state and
// re-registers it in the engine's in-memory parked set, so the next
// CheckParkedWaits cycle re-checks it against the live CI status.
//
// The parked set is the only place CheckParkedWaits looks and it is empty after a
// process restart: without rehydration an instance left waiting for CI when the
// daemon stopped would never be re-checked, never settle, and its task's
// outstanding-workflow counter would never drain — stranding the task. The startup
// orphan reconcile deliberately leaves waiting rows untouched (interrupting
// them would lose the wait); this is what brings them back to life.
//
// It replays the instance's passed steps as cached (no re-execution, no re-fired
// side effects) then parks the run at its waiting wait_for step. priorSteps are the
// instance's persisted step runs in execution order. The wait deadline is set
// fresh from the wait_for step's max_duration: a wait_for step persists no step run of its
// own, so the original park time is not recoverable across a restart — the timeout
// effectively restarts, which is acceptable for a safety bound.
//
// It returns ErrNoWaitStep when no wait_for step is waiting.
func (e *Engine) RehydrateWait(ctx context.Context, instID string, wf config.WorkflowConfig, task model.InternalTask, priorSteps []db.StepRun) error {
	bindings := e.bindingsFor(ctx, task.ID)
	r := e.initDAG(instID, wf, task, bindings, nil, 0)
	e.restoreCachedSteps(r, priorSteps)

	stepID, ok := r.firstRunnableWait()
	if !ok {
		return ErrNoWaitStep
	}
	r.state[stepID] = stWaiting
	r.waitingStep = stepID
	if pc := r.byID[stepID].WaitFor; pc != nil {
		if md := pc.ParsedMaxDuration(); md > 0 {
			r.waitDeadline = e.now().Add(md)
		}
	}

	e.mu.Lock()
	e.parked[instID] = r
	e.mu.Unlock()
	aplog.Info("workflow %s: rehydrated parked wait for instance %s at step %q", wf.ID, instID, stepID)
	return nil
}
