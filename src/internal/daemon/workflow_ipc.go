package daemon

import (
	"context"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
)

// InstanceSummary is one row in the `apiary instances` list.
type InstanceSummary struct {
	ID       string `json:"id"`
	Workflow string `json:"workflow"`
	CellID   string `json:"cell_id"`
	Title    string `json:"title"`
	State    string `json:"state"`
	Started  string `json:"started"`  // human "18m ago"
	Duration string `json:"duration"` // human "14m 32s" or "—"
}

// StepRunView is one step row in an instance detail.
type StepRunView struct {
	StepID   string `json:"step_id"`
	AgentID  string `json:"agent_id"`
	State    string `json:"state"`
	Duration string `json:"duration"`
	Cached   bool   `json:"cached"`
}

// InstanceDetail is the payload for `apiary instances <id>`.
type InstanceDetail struct {
	InstanceSummary
	Steps []StepRunView `json:"steps"`
}

// InstancesResponse is the JSON payload returned by GET /instances.
type InstancesResponse struct {
	Instances []InstanceSummary `json:"instances"`
}

// Instances returns workflow instances for the IPC list endpoint, optionally
// filtered by state and/or workflow id.
func (d *Dispatcher) Instances(ctx context.Context, state, workflowID string, limit int) (InstancesResponse, error) {
	if d.db == nil {
		return InstancesResponse{}, nil
	}
	views, err := d.db.ListWorkflowInstanceViews(ctx, state, workflowID, limit)
	if err != nil {
		return InstancesResponse{}, err
	}
	now := time.Now()
	resp := InstancesResponse{}
	for _, v := range views {
		resp.Instances = append(resp.Instances, instanceSummary(v, now))
	}
	return resp, nil
}

// InstanceDetail returns a single instance with its step runs, or (nil, nil)
// when the instance id is unknown.
func (d *Dispatcher) InstanceDetail(ctx context.Context, id string) (*InstanceDetail, error) {
	if d.db == nil {
		return nil, nil
	}
	inst, err := d.db.GetWorkflowInstance(ctx, id)
	if err != nil || inst == nil {
		return nil, err
	}
	title, _ := d.db.GetTaskTitle(ctx, inst.CellID)
	now := time.Now()
	view := db.WorkflowInstanceView{WorkflowInstance: *inst, Title: title}
	detail := &InstanceDetail{InstanceSummary: instanceSummary(view, now)}

	steps, err := d.db.ListStepRuns(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, s := range steps {
		detail.Steps = append(detail.Steps, StepRunView{
			StepID:   s.StepID,
			AgentID:  s.AgentID,
			State:    s.State,
			Duration: stepDuration(s, now),
			Cached:   s.SkippedCached,
		})
	}
	return detail, nil
}

func instanceSummary(v db.WorkflowInstanceView, now time.Time) InstanceSummary {
	return InstanceSummary{
		ID:       v.ID,
		Workflow: v.WorkflowID,
		CellID:   v.CellID,
		Title:    v.Title,
		State:    v.State,
		Started:  humanDuration(now.Sub(v.CreatedAt)) + " ago",
		Duration: instanceDuration(v.WorkflowInstance, now),
	}
}

// instanceDuration is the elapsed wall-clock time for an instance: live for
// running/waiting instances, final for done/failed, and unknown ("—") for
// interrupted/pending where no meaningful span exists.
func instanceDuration(inst db.WorkflowInstance, now time.Time) string {
	switch inst.State {
	case db.InstanceStateRunning, db.InstanceStateApprovalWaiting:
		return humanDuration(now.Sub(inst.CreatedAt))
	case db.InstanceStateDone, db.InstanceStateFailed:
		return humanDuration(inst.UpdatedAt.Sub(inst.CreatedAt))
	default:
		return "—"
	}
}

func stepDuration(s db.StepRun, now time.Time) string {
	if s.StartedAt == nil {
		return "—"
	}
	end := now
	if s.FinishedAt != nil {
		end = *s.FinishedAt
	}
	return humanDuration(end.Sub(*s.StartedAt))
}
