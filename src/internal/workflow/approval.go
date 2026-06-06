package workflow

import (
	"context"
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
func (e *Engine) ParkedApprovals() []ParkedApproval {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]ParkedApproval, 0, len(e.parked))
	for id, r := range e.parked {
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
