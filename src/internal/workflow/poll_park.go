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

// ParkedPoll describes an instance suspended at a poll step waiting for an
// external signal (e.g. CI completion).
type ParkedPoll struct {
	InstanceID string
	Task       model.InternalTask
	Step       config.StepConfig
	Deadline   time.Time // absolute give-up time (zero = none)
}

// parkedPolls returns a snapshot of all instances currently suspended at a poll
// step. The parked set is shared with approval-waiting instances, so it filters to
// runs whose waiting step is a poll.
func (e *Engine) parkedPolls() []ParkedPoll {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]ParkedPoll, 0, len(e.parked))
	for id, r := range e.parked {
		if r.byID[r.waitingStep].StepType() != config.StepTypePoll {
			continue
		}
		out = append(out, ParkedPoll{
			InstanceID: id,
			Task:       r.task,
			Step:       r.byID[r.waitingStep],
			Deadline:   r.pollDeadline,
		})
	}
	return out
}

// CheckParkedPolls re-checks every instance suspended at a poll step by waking it:
// the woken run re-executes the poll step (a single CI query via the engine's
// CIStatusChecker) and either advances past it (CI passed), fails it (CI failed or
// the deadline elapsed, which drives on_reject/on_fail loop-back), or re-parks it
// (CI still pending). Called once per dispatcher poll cycle, mirroring
// CheckParkedApprovals. The poll cadence is therefore the dispatcher's poll
// interval; a step's check_interval acts as a lower bound only.
func (e *Engine) CheckParkedPolls(ctx context.Context) {
	for _, p := range e.parkedPolls() {
		e.wakePoll(ctx, p.InstanceID)
	}
}

// wakePoll resumes a poll-parked instance: it resets the waiting poll step to
// pending so driveDAG re-dispatches it (a single CI check), then drives the graph
// to its next terminal or suspended state and settles it. A pending check re-parks
// the instance (via enterPoll, preserving its deadline); a pass/fail advances or
// loops the workflow through the normal result-processing path. It is a no-op if
// the instance is no longer parked.
func (e *Engine) wakePoll(ctx context.Context, instanceID string) {
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
	r.state[step] = stPending // re-arm the poll step for re-dispatch

	_ = e.store.UpdateWorkflowInstanceState(ctx, instanceID, db.InstanceStateRunning)
	outcome := e.driveDAG(ctx, r)
	e.settle(ctx, r, outcome)
}

// ErrNoPollStep is returned by RehydratePoll when the instance has no poll step
// waiting once its cached steps are restored — i.e. it was not actually parked at a
// poll (a stale or malformed poll_waiting row). The caller leaves it for manual
// reconciliation rather than re-parking it.
var ErrNoPollStep = errors.New("no poll step is waiting")

// RehydratePoll reconstructs an instance persisted in the poll_waiting state and
// re-registers it in the engine's in-memory parked set, so the next
// CheckParkedPolls cycle re-checks it against the live CI status.
//
// The parked set is the only place CheckParkedPolls looks and it is empty after a
// process restart: without rehydration an instance left waiting for CI when the
// daemon stopped would never be re-checked, never settle, and its task's
// outstanding-workflow counter would never drain — stranding the task. The startup
// orphan reconcile deliberately leaves poll_waiting rows untouched (interrupting
// them would lose the wait); this is what brings them back to life.
//
// It replays the instance's passed steps as cached (no re-execution, no re-fired
// side effects) then parks the run at its waiting poll step. priorSteps are the
// instance's persisted step runs in execution order. The wait deadline is set
// fresh from the poll step's max_duration: a poll step persists no step run of its
// own, so the original park time is not recoverable across a restart — the timeout
// effectively restarts, which is acceptable for a safety bound.
//
// It returns ErrNoPollStep when no poll step is waiting.
func (e *Engine) RehydratePoll(ctx context.Context, instID string, wf config.WorkflowConfig, task model.InternalTask, priorSteps []db.StepRun) error {
	bindings := e.bindingsFor(ctx, task.ID)
	r := e.initDAG(instID, wf, task, bindings, nil, 0)
	e.restoreCachedSteps(r, priorSteps)

	stepID, ok := r.firstRunnablePoll()
	if !ok {
		return ErrNoPollStep
	}
	r.state[stepID] = stWaiting
	r.waitingStep = stepID
	if pc := r.byID[stepID].PollConfig; pc != nil {
		if md := pc.ParsedMaxDuration(); md > 0 {
			r.pollDeadline = e.now().Add(md)
		}
	}

	e.mu.Lock()
	e.parked[instID] = r
	e.mu.Unlock()
	aplog.Info("workflow %s: rehydrated parked poll for instance %s at step %q", wf.ID, instID, stepID)
	return nil
}
