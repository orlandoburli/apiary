package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
)

// ApprovalDecision is the result of evaluating an approval step's conditions
// against the live task.
type ApprovalDecision int

const (
	// ApprovalWait means neither resume nor abort conditions matched yet.
	ApprovalWait ApprovalDecision = iota
	// ApprovalResume means a resume_on condition matched — continue the workflow.
	ApprovalResume
	// ApprovalAbort means an abort_on condition matched — fail the workflow.
	ApprovalAbort
)

// EvaluateApproval decides whether an approval step should resume, abort, or keep
// waiting, given the live task. resume_on takes precedence over abort_on when
// both would match (an explicit approval wins).
func EvaluateApproval(step config.StepConfig, cell model.SourceItem) ApprovalDecision {
	// Explicit approvers require an authenticated dashboard/webhook response;
	// legacy source comments have no author identity and cannot satisfy policy.
	if len(step.Approvers) > 0 {
		return ApprovalWait
	}
	if step.ResumeOn != nil && matchApprovalTrigger(*step.ResumeOn, cell) {
		return ApprovalResume
	}
	if step.AbortOn != nil && matchApprovalTrigger(*step.AbortOn, cell) {
		return ApprovalAbort
	}
	return ApprovalWait
}

// matchApprovalTrigger reports whether any populated field of the trigger matches
// the cell (OR semantics across fields).
func matchApprovalTrigger(t config.ApprovalTrigger, cell model.SourceItem) bool {
	if t.CommentContains != "" && commentContains(cell, t.CommentContains) {
		return true
	}
	if t.LabelAdded != "" && labelPresent(cell, t.LabelAdded) {
		return true
	}
	if t.StateChanged != "" && strings.EqualFold(cell.State, t.StateChanged) {
		return true
	}
	return false
}

// commentContains reports whether any comment body contains the substring
// (case-insensitive).
func commentContains(cell model.SourceItem, sub string) bool {
	needle := strings.ToLower(sub)
	for _, c := range cell.Comments {
		if strings.Contains(strings.ToLower(c.Body), needle) {
			return true
		}
	}
	return false
}

// labelPresent reports whether the cell carries the label (case-insensitive).
func labelPresent(cell model.SourceItem, label string) bool {
	for _, l := range cell.Labels {
		if strings.EqualFold(l, label) {
			return true
		}
	}
	return false
}

// ParkedApproval describes an instance suspended at an approval step.
type ParkedApproval struct {
	InstanceID string
	Task       model.InternalTask
	Bindings   []model.SourceBinding
	Step       config.StepConfig // the waiting approval step (resume_on/abort_on/timeout)
	ParkedAt   time.Time
	// AgentID is the workflow's representative agent — the slot a resumed advance is
	// admitted through so a follow-on agent step respects that agent's max_workers
	// (see Dispatcher.checkApprovals). Empty/unmatched ids gate as ungated.
	AgentID string
}

// ParkedApprovals returns a snapshot of all instances currently awaiting approval.
// The parked set is shared with wait-waiting instances, so it filters to runs whose
// waiting step is an approval — a wait park is resolved by CheckParkedWaits, not by
// the approval checker.
func (e *Engine) ParkedApprovals() []ParkedApproval {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]ParkedApproval, 0, len(e.parked))
	for id, r := range e.parked {
		if r.byID[r.waitingStep].StepType() != config.StepTypeApproval {
			continue
		}
		out = append(out, ParkedApproval{
			InstanceID: id,
			Task:       r.task,
			Bindings:   r.bindings,
			Step:       r.byID[r.waitingStep],
			ParkedAt:   r.parkedAt,
			AgentID:    representativeAgent(r.wf),
		})
	}
	return out
}

// ResolveApproval resumes (or aborts) a parked instance and drives it to its next
// terminal or suspended state. Returns whether the instance then completed
// successfully. It errors if the instance is not currently awaiting approval.
func (e *Engine) ResolveApproval(ctx context.Context, instanceID string, decision ApprovalDecision) (bool, error) {
	name := "approve"
	if decision == ApprovalAbort {
		name = "reject"
	}
	response := db.ApprovalResponse{Decision: name, Actor: "source-trigger", Channel: "source", IdempotencyKey: "source:" + instanceID + ":" + name}
	if store, ok := e.store.(approvalRequestStore); ok {
		if req, _ := store.GetApprovalByInstance(ctx, instanceID); req != nil {
			stored, won, err := store.ResolveApprovalRequest(ctx, req.ID, response)
			if err != nil {
				return false, err
			}
			if !won && stored != nil {
				if stored.Status == db.ApprovalPending || stored.Status == db.ApprovalEscalated {
					return false, nil
				}
				response.Decision = "reject"
				if stored.Status == db.ApprovalApproved {
					response.Decision = "approve"
				}
				response.Actor, response.Channel, response.Feedback, response.Values = stored.RespondedBy, stored.ResponseChannel, stored.Feedback, stored.Values
			}
		}
	}
	return e.ResolveApprovalResponse(ctx, instanceID, response)
}

// ResolveApprovalResponse applies a persisted multi-channel response and exposes
// its feedback and values to subsequent steps as approval.<field> memory values.
func (e *Engine) ResolveApprovalResponse(ctx context.Context, instanceID string, response db.ApprovalResponse) (bool, error) {
	decision := ApprovalResume
	if strings.EqualFold(response.Decision, "reject") || strings.EqualFold(response.Decision, "rejected") {
		decision = ApprovalAbort
	}
	e.mu.Lock()
	r, ok := e.parked[instanceID]
	if ok {
		delete(e.parked, instanceID)
	}
	e.mu.Unlock()
	if !ok {
		return false, fmt.Errorf("instance %q is not awaiting approval", instanceID)
	}
	eventType := "approval.granted"
	decisionName := "resume"
	if decision == ApprovalAbort {
		eventType, decisionName = "approval.rejected", "abort"
	}
	metadata := map[string]any{"decision": decisionName, "actor": response.Actor, "channel": response.Channel, "feedback": response.Feedback, "values": response.Values}
	e.recordExecutionEvent(ctx, r, eventType, metadata)
	stepID := r.waitingStep
	structured := map[string]any{"approval_decision": decisionName, "approval_feedback": response.Feedback}
	write := []string{"approval_decision", "approval_feedback"}
	for key, value := range response.Values {
		structured[key] = value
		write = append(write, key)
	}
	r.contrib[stepID] = MemoryStep{StepID: stepID, WriteFields: write, Structured: structured, Summary: response.Feedback}

	r.resolveApproval(decision)
	_ = e.store.UpdateWorkflowInstanceState(ctx, instanceID, db.InstanceStateRunning)
	outcome := e.driveDAG(ctx, r)
	return e.settle(ctx, r, outcome), nil
}

// RecheckApproval performs ONE cheap evaluation of a parked approval instance
// WITHOUT advancing its workflow graph, returning the decision (wait/resume/abort)
// so the caller can decide whether to drive the expensive ResolveApproval advance.
//
// It mirrors RecheckWait: the timeout is checked first (an elapsed timeout aborts),
// then the live source item is polled per binding (poll is keyed by source id +
// source item id) and evaluated via EvaluateApproval — the first binding whose item
// satisfies a resume/abort condition decides, a poll error skips that binding. It
// runs no agent step, so it is safe to run every poll cycle for every parked
// instance, concurrently and WITHOUT holding any agent-concurrency slot — a busy
// agent can never delay another instance's approval re-check. The expensive graph
// advance is left to ResolveApproval, which the dispatcher gates through the
// per-agent semaphore.
//
// It returns ApprovalWait (a no-op) when the instance is no longer parked at an
// approval step. A binding-less (spawned) task can only ever reach ApprovalAbort
// via timeout, mirroring CheckParkedApprovals.
func (e *Engine) RecheckApproval(ctx context.Context, instanceID string, poll func(sourceID, sourceItemID string) (model.SourceItem, error)) ApprovalDecision {
	e.mu.Lock()
	r, ok := e.parked[instanceID]
	if !ok {
		e.mu.Unlock()
		return ApprovalWait
	}
	step := r.byID[r.waitingStep]
	bindings := r.bindings
	parkedAt := r.parkedAt
	e.mu.Unlock()

	if step.StepType() != config.StepTypeApproval {
		return ApprovalWait
	}
	if raw := step.RemindAfter; raw != "" {
		if after, err := time.ParseDuration(raw); err == nil && e.now().Sub(parkedAt) >= after {
			if store, ok := e.store.(approvalRequestStore); ok {
				if req, _ := store.GetApprovalByInstance(ctx, instanceID); req != nil {
					won, _ := store.MarkApprovalReminded(ctx, req.ID)
					if won {
						e.recordExecutionEvent(ctx, r, "approval.reminder", map[string]any{"request_id": req.ID, "approvers": step.Approvers})
						for _, provider := range e.approvalProviders {
							if lifecycle, ok := provider.(ApprovalLifecycleProvider); ok {
								_ = lifecycle.RemindApproval(ctx, req)
							}
						}
					}
				}
			}
		}
	}
	if raw := step.EscalateAfter; raw != "" {
		if after, err := time.ParseDuration(raw); err == nil && e.now().Sub(parkedAt) >= after {
			if store, ok := e.store.(approvalRequestStore); ok {
				if req, _ := store.GetApprovalByInstance(ctx, instanceID); req != nil {
					won, _ := store.EscalateApproval(ctx, req.ID)
					if won {
						e.recordExecutionEvent(ctx, r, "approval.escalated", map[string]any{"request_id": req.ID, "escalate_to": step.EscalateTo})
						for _, provider := range e.approvalProviders {
							if lifecycle, ok := provider.(ApprovalLifecycleProvider); ok {
								_ = lifecycle.EscalateApproval(ctx, req, step.EscalateTo)
							}
						}
					}
				}
			}
		}
	}
	if to := step.ParsedTimeout(); to > 0 && e.now().Sub(parkedAt) >= to {
		aplog.Info("workflow: approval on instance %s timed out after %s — aborting", instanceID, to)
		if store, ok := e.store.(approvalRequestStore); ok {
			if req, _ := store.GetApprovalByInstance(ctx, instanceID); req != nil {
				won, _ := store.MarkApprovalTimedOut(ctx, req.ID)
				if won {
					e.recordExecutionEvent(ctx, r, "approval.timed_out", map[string]any{"request_id": req.ID, "timeout": to.String()})
				}
			}
		}
		return ApprovalAbort
	}
	for _, b := range bindings {
		item, err := poll(b.SourceID, b.SourceItemID)
		if err != nil {
			aplog.Debug("workflow: poll item %s/%s for approval check failed: %v", b.SourceID, b.SourceItemID, err)
			continue
		}
		if decision := EvaluateApproval(step, item); decision != ApprovalWait {
			return decision
		}
	}
	return ApprovalWait
}

// CheckParkedApprovals evaluates every parked instance against its live source
// item(s) and resumes, aborts, or times it out as conditions dictate. The live
// item is fetched per source binding (poll is keyed by source id + source item
// id), so the TaskPoller adapter is resolved from the task's bindings rather than
// a frozen SourceItem. The first binding whose live item satisfies a resume/abort
// condition decides the instance; when poll errors for a binding it is skipped.
// A binding-less (spawned) task cannot be resolved via a source and stays parked
// until it times out.
//
// This is the simple sequential reference path used by the engine tests. In
// production the dispatcher does NOT call it; instead Dispatcher.checkApprovals
// drives the same primitives concurrently (RecheckApproval + ResolveApproval) so a
// long-running follow-on agent on one resumed instance cannot block the cheap
// re-checks of every other parked approval, and so each advance is admitted through
// the per-agent semaphore.
func (e *Engine) CheckParkedApprovals(ctx context.Context, poll func(sourceID, sourceItemID string) (model.SourceItem, error)) {
	for _, p := range e.ParkedApprovals() {
		if to := p.Step.ParsedTimeout(); to > 0 && e.now().Sub(p.ParkedAt) >= to {
			aplog.Info("workflow: approval on instance %s timed out after %s — aborting", p.InstanceID, to)
			_ = e.RecheckApproval(ctx, p.InstanceID, poll) // persists timeout + audit event
			_, _ = e.ResolveApproval(ctx, p.InstanceID, ApprovalAbort)
			continue
		}
		for _, b := range p.Bindings {
			item, err := poll(b.SourceID, b.SourceItemID)
			if err != nil {
				aplog.Debug("workflow: poll item %s/%s for approval check failed: %v", b.SourceID, b.SourceItemID, err)
				continue
			}
			decision := EvaluateApproval(p.Step, item)
			if decision == ApprovalResume {
				aplog.Info("workflow: approval on instance %s resumed", p.InstanceID)
				_, _ = e.ResolveApproval(ctx, p.InstanceID, ApprovalResume)
				break
			}
			if decision == ApprovalAbort {
				aplog.Info("workflow: approval on instance %s aborted by condition", p.InstanceID)
				_, _ = e.ResolveApproval(ctx, p.InstanceID, ApprovalAbort)
				break
			}
		}
	}
}

// ErrNoApprovalStep is returned by RehydrateApproval when the instance has no
// approval step waiting once its cached steps are restored — i.e. it was not
// actually parked at an approval (a stale or malformed approval_waiting row). The
// caller should leave it for manual reconciliation rather than re-parking it.
var ErrNoApprovalStep = errors.New("no approval step is waiting")

// RehydrateApproval reconstructs an instance persisted in the approval_waiting
// state and re-registers it in the engine's in-memory parked set, so the next
// CheckParkedApprovals poll can re-evaluate it against the live source item.
//
// The parked set is the only place CheckParkedApprovals looks and it is empty
// after a process restart: without rehydration an instance left waiting for
// approval when the daemon stopped would never be re-evaluated, never settle, and
// its task's outstanding-workflow counter would never drain — stranding the task
// in 'registered' forever. The startup orphan reconcile deliberately leaves
// approval_waiting rows untouched (interrupting them would lose the wait); this is
// what brings them back to life.
//
// It replays the instance's passed steps as cached — no re-execution, no re-fired
// side effects, and crucially no re-posted approval message — then parks the run
// at its waiting approval step. priorSteps are the instance's persisted step runs
// in execution order. parkedAt is when the instance originally suspended (the
// persisted instance's updated_at); preserving it means an approval timeout counts
// from the original park time and survives the restart rather than resetting on
// every boot.
//
// It returns ErrNoApprovalStep when no approval step is waiting.
func (e *Engine) RehydrateApproval(ctx context.Context, instID string, wf config.WorkflowConfig, task model.InternalTask, priorSteps []db.StepRun, parkedAt time.Time) error {
	bindings := e.bindingsFor(ctx, task.ID)
	r := e.initDAG(instID, wf, task, bindings, nil, 0)
	e.restoreCachedSteps(r, priorSteps)

	stepID, ok := r.firstRunnableApproval()
	if !ok {
		return ErrNoApprovalStep
	}
	r.state[stepID] = stWaiting
	r.waitingStep = stepID
	r.parkedAt = parkedAt

	e.mu.Lock()
	e.parked[instID] = r
	e.mu.Unlock()
	aplog.Info("workflow %s: rehydrated parked approval for instance %s at step %q", wf.ID, instID, stepID)
	return nil
}
