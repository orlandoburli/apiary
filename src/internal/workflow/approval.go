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
}

// ParkedApprovals returns a snapshot of all instances currently awaiting approval.
// The parked set is shared with poll-waiting instances, so it filters to runs whose
// waiting step is an approval — a poll park is resolved by CheckParkedPolls, not by
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
		})
	}
	return out
}

// ResolveApproval resumes (or aborts) a parked instance and drives it to its next
// terminal or suspended state. Returns whether the instance then completed
// successfully. It errors if the instance is not currently awaiting approval.
func (e *Engine) ResolveApproval(ctx context.Context, instanceID string, decision ApprovalDecision) (bool, error) {
	e.mu.Lock()
	r, ok := e.parked[instanceID]
	if ok {
		delete(e.parked, instanceID)
	}
	e.mu.Unlock()
	if !ok {
		return false, fmt.Errorf("instance %q is not awaiting approval", instanceID)
	}

	r.resolveApproval(decision)
	_ = e.store.UpdateWorkflowInstanceState(ctx, instanceID, db.InstanceStateRunning)
	outcome := e.driveDAG(ctx, r)
	return e.settle(ctx, r, outcome), nil
}

// CheckParkedApprovals evaluates every parked instance against its live source
// item(s) and resumes, aborts, or times it out as conditions dictate. The live
// item is fetched per source binding (poll is keyed by source id + source item
// id), so the TaskPoller adapter is resolved from the task's bindings rather than
// a frozen SourceItem. The first binding whose live item satisfies a resume/abort
// condition decides the instance; when poll errors for a binding it is skipped.
// A binding-less (spawned) task cannot be resolved via a source and stays parked
// until it times out.
func (e *Engine) CheckParkedApprovals(ctx context.Context, poll func(sourceID, sourceItemID string) (model.SourceItem, error)) {
	for _, p := range e.ParkedApprovals() {
		if to := p.Step.ParsedTimeout(); to > 0 && e.now().Sub(p.ParkedAt) >= to {
			aplog.Info("workflow: approval on instance %s timed out after %s — aborting", p.InstanceID, to)
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
