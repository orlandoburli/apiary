package workflow

import (
	"context"
	"errors"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/state"
)

// ParkedWait describes an instance suspended at a wait_for step waiting for an
// external signal (e.g. CI completion).
type ParkedWait struct {
	InstanceID string
	Task       model.InternalTask
	Step       config.StepConfig
	Deadline   time.Time // absolute give-up time (zero = none)
	// AgentID is the workflow's representative agent — the slot a woken advance is
	// admitted through so a follow-on agent step respects that agent's max_workers
	// (see Dispatcher.checkWaits). Empty/unmatched ids gate as ungated.
	AgentID string
}

// ParkedWaits returns a snapshot of all instances currently suspended at a wait_for
// step. The parked set is shared with approval-waiting instances, so it filters to
// runs whose waiting step is a wait_for.
func (e *Engine) ParkedWaits() []ParkedWait {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]ParkedWait, 0, len(e.parked))
	for id, r := range e.parked {
		// Step is the wait_for step to re-check: the parked node itself, or the
		// wait_for child of a parked parallel group (#425). Approval parks share
		// the parked set and resolve to no wait step — skipped here.
		waitStep, ok := r.waitStepConfig()
		if !ok {
			continue
		}
		out = append(out, ParkedWait{
			InstanceID: id,
			Task:       r.task,
			Step:       waitStep,
			Deadline:   r.waitDeadline,
			AgentID:    representativeAgent(r.wf),
		})
	}
	return out
}

// representativeAgent returns the agent id used to gate a parked wait's advance
// through the dispatcher's per-agent concurrency semaphore — the workflow's first
// agent step, mirroring how a fresh dispatch picks its route's agent
// (router.firstAgentStep). It falls back to the workflow id (which is not a
// configured agent, so the gate treats it as ungated) when the workflow declares no
// agent step.
func representativeAgent(wf config.WorkflowConfig) string {
	for _, s := range wf.Steps {
		switch s.StepType() {
		case config.StepTypeAgent:
			if s.Agent != "" {
				return s.Agent
			}
		case config.StepTypeForeach:
			if s.Step != nil && s.Step.Agent != "" {
				return s.Step.Agent
			}
		}
	}
	return wf.ID
}

// IsParked reports whether the instance is currently in the parked set, at a
// wait_for or an approval step.
func (e *Engine) IsParked(instanceID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.parked[instanceID]
	return ok
}

// StopParked takes a stopped instance out of the engine, so no later re-check or
// approval decision can advance it, and reports whether the engine held it. Call it
// BEFORE persisting the interrupted state: settle and claimParked rely on that order.
//
// A parked run is simply dropped. A run whose wake is already under way cannot be
// dropped, so it is flagged instead: driveDAG launches nothing further and settle
// leaves the persisted state to the stop. An instance the engine does not hold (a
// fresh run still on its first drive, or an unknown id) is left alone.
//
// Either way the run will never settle, so its slot in the task's outstanding count
// is released here. The completion hooks do not fire: a stopped run neither passed
// nor failed, and an interrupted instance does not count as live.
func (e *Engine) StopParked(ctx context.Context, instanceID string) bool {
	e.mu.Lock()
	r, parked := e.parked[instanceID]
	resuming := false
	if parked {
		delete(e.parked, instanceID)
	} else if r, resuming = e.resuming[instanceID]; resuming {
		r.stopped.Store(true)
		delete(e.resuming, instanceID)
	}
	e.mu.Unlock()
	if !parked && !resuming {
		return false
	}
	if parked {
		// A run still being driven keeps this until it settles: its workers need it.
		e.instWF.Delete(instanceID)
	}
	e.releaseOutstanding(ctx, r)
	aplog.Info("workflow %s: dropped stopped instance %s (was at step %q)", r.wf.ID, instanceID, r.waitingStep)
	return true
}

// releaseOutstanding gives back a run's slot in its task's outstanding-workflow
// count without settling the task.
func (e *Engine) releaseOutstanding(ctx context.Context, r *dagRun) {
	if e.tracker == nil || r.task.ID == "" {
		return
	}
	if _, err := e.tracker.DecrementOutstanding(ctx, r.task.ID); err != nil {
		aplog.Error("task %s: decrement outstanding: %v", r.task.ID, err)
	}
}

// claimParked moves a parked run into e.resuming and persists it as running, or
// reports false when it must not resume. The whole claim holds e.mu, so a stop lands
// either before it (the run is gone) or after it (the run is in e.resuming and gets
// flagged) — never between the state check and the running write, which would
// overwrite the stop's interrupted state.
func (e *Engine) claimParked(ctx context.Context, instanceID string) (*dagRun, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.parked[instanceID]
	if !ok || !e.stillParkedLocked(ctx, r) {
		return nil, false
	}
	delete(e.parked, instanceID)
	e.resuming[instanceID] = r
	_ = e.store.UpdateWorkflowInstanceState(ctx, instanceID, db.InstanceStateRunning, "")
	return r, true
}

// stillParkedLocked re-reads a parked run's persisted state and reports whether it
// is still parked, dropping the run when it is not: the row is the record of what
// the operator asked for, and a stale in-memory entry must never outrank it. This
// also catches the stop that StopParked cannot see, one that reached a fresh run
// just before it parked. A failed read says nothing either way, so the run stays
// parked for the next cycle. The caller holds e.mu.
func (e *Engine) stillParkedLocked(ctx context.Context, r *dagRun) bool {
	reader, ok := e.store.(instanceReader)
	if !ok {
		return true
	}
	inst, err := reader.GetWorkflowInstance(ctx, r.instID)
	if err != nil {
		aplog.Warn("workflow %s: read parked instance %s: %v", r.wf.ID, r.instID, err)
		return false
	}
	if inst == nil {
		return true
	}
	st, reason := state.NormalizeWithReason(inst.State)
	if reason == "" {
		reason = state.Reason(inst.BlockedReason)
	}
	if st == state.Blocked && reason != state.ReasonInterrupted {
		return true
	}
	aplog.Info("workflow %s: dropping parked instance %s: persisted state is %s %s", r.wf.ID, r.instID, inst.State, reason)
	delete(e.parked, r.instID)
	e.instWF.Delete(r.instID)
	e.releaseOutstanding(ctx, r)
	return false
}

// CheckParkedWaits re-checks every instance suspended at a wait_for step by waking
// it: the woken run re-executes the wait_for step (a single CI query via the
// engine's CIStatusChecker) and either advances past it (CI passed), fails it (CI
// failed or the deadline elapsed, which drives on_reject/on_fail loop-back), or
// re-parks it (CI still pending).
//
// This is the simple sequential reference path. In production the dispatcher does
// NOT call it; instead Dispatcher.checkWaits drives the same primitives
// concurrently (RecheckWait + WakeWait) so a long-running follow-on agent on one
// woken instance cannot block the cheap CI re-checks of every other parked
// instance, and so each advance is admitted through the per-agent semaphore.
func (e *Engine) CheckParkedWaits(ctx context.Context) {
	for _, p := range e.ParkedWaits() {
		e.WakeWait(ctx, p.InstanceID)
	}
}

// RecheckWait performs ONE cheap CI status check for a wait_for instance WITHOUT
// advancing its workflow graph, returning whether the wait is now terminal — i.e.
// CI reached pass/fail (or the deadline elapsed) and the instance has real work to
// do, so the caller should drive it via WakeWait.
//
// It deliberately runs no agent step: a still-pending check leaves the instance
// parked and returns false. That makes it safe to run every poll cycle for every
// parked instance, concurrently and WITHOUT holding any agent-concurrency slot — so
// a busy agent can never delay another instance's CI re-check. The expensive graph
// advance (which may run a follow-on agent) is left to WakeWait, which the
// dispatcher gates through the per-agent semaphore. It is a no-op returning false
// when the instance is no longer parked at a wait_for step.
//
// The CI status the re-check observes is re-queried by the subsequent WakeWait
// (driveDAG re-arms and re-runs the wait_for step), so a terminal transition costs
// one extra, idempotent CI poll — recorded for audit like any other.
func (e *Engine) RecheckWait(ctx context.Context, instanceID string) (terminal bool) {
	e.mu.Lock()
	r, ok := e.parked[instanceID]
	if !ok || !e.stillParkedLocked(ctx, r) {
		e.mu.Unlock()
		return false
	}
	step, isWait := r.waitStepConfig()
	target := r.waitTarget()
	deadline := r.waitDeadline
	e.mu.Unlock()

	if !isWait {
		return false
	}
	res, _ := e.RunWaitStep(ctx, instanceID, step, target, deadline)
	return !res.Pending
}

// WakeWait resumes a wait-parked instance: it resets the waiting wait_for step to
// pending so driveDAG re-dispatches it (a single CI check), then drives the graph
// to its next terminal or suspended state and settles it. A pending check re-parks
// the instance (via enterWait, preserving its deadline); a pass/fail advances or
// loops the workflow through the normal result-processing path. It is a no-op if
// the instance is no longer parked — the claim (claimParked) is atomic, so two
// concurrent wakes of the same instance are safe: the loser returns early. It is
// equally a no-op for an instance stopped since it parked.
//
// The dispatcher runs this on its own goroutine, admitted through the per-agent
// semaphore, so a slow follow-on agent step here neither blocks the poll loop nor
// exceeds the agent's max_workers.
func (e *Engine) WakeWait(ctx context.Context, instanceID string) {
	r, ok := e.claimParked(ctx, instanceID)
	if !ok {
		return
	}

	step := r.waitingStep
	r.waitingStep = ""
	// For a parked parallel group the waiting child is re-derived on the next
	// pass; its finished siblings stay memoized in r.parallelDone.
	r.waitingChild = ""
	r.state[step] = stPending // re-arm the wait_for (or parallel) step for re-dispatch

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

	stepID, childID, ok := r.firstRunnableWait()
	if !ok {
		return ErrNoWaitStep
	}
	r.state[stepID] = stWaiting
	r.waitingStep = stepID
	r.waitingChild = childID
	waitStep, _ := r.waitStepConfig()
	if pc := waitStep.WaitFor; pc != nil {
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
