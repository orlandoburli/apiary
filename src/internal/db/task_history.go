package db

import (
	"context"
	"time"
)

// TaskHistorySegment is one workflow instance's slice of an InternalTask's life:
// the instance, its step runs, and the raw task logs scoped to that instance's
// time window. Segments are returned oldest-first so a task that handed off from
// one workflow to another (e.g. investigator → implementation) reads top-to-bottom
// as a chronological story.
type TaskHistorySegment struct {
	Instance WorkflowInstance
	Steps    []StepRun
	Logs     []TaskLogLine
}

// GetTaskWorkflowHistory returns the full per-instance history for an InternalTask,
// oldest instance first.
//
// task_logs are keyed by cell id — shared by every instance of a task, since they
// all bind to the same source item — and carry no instance id, so each instance's
// logs are isolated by time-windowing the shared stream: instance i owns
// [created_at(i), created_at(i+1)), and the most recent instance is open-ended so
// its still-arriving live logs keep flowing in. This is accurate while hand-offs are
// sequential (HasActiveInstanceForRoute blocks a duplicate (task, workflow) while one
// runs/waits); concurrent fan-out instances on one task would interleave and
// mis-attribute — revisit with a task_logs.instance_id column if that becomes common.
// A log written exactly at the next instance's created_at lands in both adjacent
// windows, but timestamps come from time.Now() so an exact tie is vanishingly rare.
func (c *Client) GetTaskWorkflowHistory(ctx context.Context, internalTaskID string) ([]TaskHistorySegment, error) {
	insts, err := c.ListWorkflowInstancesByTask(ctx, internalTaskID)
	if err != nil {
		return nil, err
	}
	// ListWorkflowInstancesByTask is newest-first; reverse to oldest-first.
	for i, j := 0, len(insts)-1; i < j; i, j = i+1, j-1 {
		insts[i], insts[j] = insts[j], insts[i]
	}

	segments := make([]TaskHistorySegment, 0, len(insts))
	for i := range insts {
		inst := insts[i]
		steps, err := c.ListStepRuns(ctx, inst.ID)
		if err != nil {
			return nil, err
		}
		// Window: [this.created_at, next.created_at). The last (most recent)
		// instance is open-ended (to == nil) so live logs are still captured.
		from := inst.CreatedAt
		var to *time.Time
		if i+1 < len(insts) {
			next := insts[i+1].CreatedAt
			to = &next
		}
		logs, err := c.GetTaskLogsInRange(ctx, inst.CellID, &from, to)
		if err != nil {
			return nil, err
		}
		segments = append(segments, TaskHistorySegment{
			Instance: inst,
			Steps:    steps,
			Logs:     logs,
		})
	}
	return segments, nil
}
