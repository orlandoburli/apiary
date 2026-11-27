package db

import (
	"context"
	"math"
	"sort"
	"time"
)

// DurationStats is a distribution, not an average.
//
// Workflow durations are heavy-tailed: one workflow in this hive has a mean of
// 481 minutes over a p50 of 261, because a handful of runs sat parked for most
// of a day. A mean describes no run in particular; p50 and p90 describe the
// ones you will actually experience.
type DurationStats struct {
	N   int
	P50 time.Duration
	P90 time.Duration
	Max time.Duration
}

// WorkflowDurations compares the two things a run costs.
//
// Wall is the end-to-end life of the instance, waiting included. Active is the
// union of its step intervals, which is the time something was actually
// executing: approvals and CI waits are not steps that persist a run, so they
// do not appear there. The pair is the point — for a workflow parked on CI for
// hours, the two differ by an order of magnitude.
type WorkflowDurations struct {
	WorkflowID string
	Runs       int
	Wall       DurationStats
	Active     DurationStats
}

// StepDurations is one step's distribution across a workflow's instances.
type StepDurations struct {
	WorkflowID string
	StepID     string
	AgentID    string
	Runs       int
	Duration   DurationStats
}

// GetWorkflowDurations returns, per workflow, the distribution of wall and
// active durations for runs that finished in the window, plus the same per
// step.
//
// A run's active time is the union of its step intervals, not their sum: steps
// that ran in parallel would otherwise be counted twice and a fast fan-out
// would look slower than a sequential one.
func (c *Client) GetWorkflowDurations(ctx context.Context, since time.Time) ([]WorkflowDurations, []StepDurations, error) {
	// Instances that finished in the window. created_at gives the wall start.
	instRows, err := c.db.QueryContext(ctx, `
		SELECT id, workflow_id, created_at, updated_at
		FROM workflow_instances
		WHERE state IN ('done','failed') AND updated_at >= ?
		ORDER BY workflow_id, id
	`, since)
	if err != nil {
		return nil, nil, err
	}
	defer instRows.Close()

	type instance struct {
		id         string
		workflowID string
		wall       time.Duration
	}
	var instances []instance
	workflowOf := map[string]string{}
	for instRows.Next() {
		var (
			id, workflowID string
			created, upd   time.Time
		)
		if err := instRows.Scan(&id, &workflowID, &created, &upd); err != nil {
			return nil, nil, err
		}
		instances = append(instances, instance{id: id, workflowID: workflowID, wall: upd.Sub(created)})
		workflowOf[id] = workflowID
	}
	if err := instRows.Err(); err != nil {
		return nil, nil, err
	}
	if len(instances) == 0 {
		return nil, nil, nil
	}

	// Step intervals for those instances. A step that started before the window
	// still counts: the run it belongs to finished inside it.
	stepRows, err := c.db.QueryContext(ctx, `
		SELECT sr.workflow_instance_id, sr.step_id, COALESCE(sr.agent_id,''), sr.started_at, sr.finished_at
		FROM step_runs sr
		JOIN workflow_instances wi ON wi.id = sr.workflow_instance_id
		WHERE wi.state IN ('done','failed') AND wi.updated_at >= ?
		  AND sr.started_at IS NOT NULL AND sr.finished_at IS NOT NULL
	`, since)
	if err != nil {
		return nil, nil, err
	}
	defer stepRows.Close()

	intervalsByInstance := map[string][]timeInterval{}
	stepSamples := map[string][]time.Duration{}
	stepAgents := map[string]string{}
	for stepRows.Next() {
		var (
			instanceID, stepID, agentID string
			start, end                  time.Time
		)
		if err := stepRows.Scan(&instanceID, &stepID, &agentID, &start, &end); err != nil {
			return nil, nil, err
		}
		if end.Before(start) {
			continue
		}
		intervalsByInstance[instanceID] = append(intervalsByInstance[instanceID], timeInterval{start, end})

		// Keyed by workflow and step, not by instance: the distribution is
		// across every run of that workflow, which is the comparison asked for.
		key := workflowOf[instanceID] + "\x00" + stepID
		stepSamples[key] = append(stepSamples[key], end.Sub(start))
		if agentID != "" {
			stepAgents[key] = agentID
		}
	}
	if err := stepRows.Err(); err != nil {
		return nil, nil, err
	}

	wallByWorkflow := map[string][]time.Duration{}
	activeByWorkflow := map[string][]time.Duration{}
	for _, inst := range instances {
		wallByWorkflow[inst.workflowID] = append(wallByWorkflow[inst.workflowID], inst.wall)
		active := unionDuration(intervalsByInstance[inst.id])
		activeByWorkflow[inst.workflowID] = append(activeByWorkflow[inst.workflowID], active)
	}

	workflows := make([]WorkflowDurations, 0, len(wallByWorkflow))
	for workflowID, walls := range wallByWorkflow {
		workflows = append(workflows, WorkflowDurations{
			WorkflowID: workflowID,
			Runs:       len(walls),
			Wall:       summariseDurations(walls),
			Active:     summariseDurations(activeByWorkflow[workflowID]),
		})
	}
	sort.Slice(workflows, func(i, j int) bool {
		if workflows[i].Runs != workflows[j].Runs {
			return workflows[i].Runs > workflows[j].Runs
		}
		return workflows[i].WorkflowID < workflows[j].WorkflowID
	})

	steps := make([]StepDurations, 0, len(stepSamples))
	for key, samples := range stepSamples {
		parts := splitStepKey(key)
		steps = append(steps, StepDurations{
			WorkflowID: parts[0],
			StepID:     parts[1],
			AgentID:    stepAgents[key],
			Runs:       len(samples),
			Duration:   summariseDurations(samples),
		})
	}
	sort.Slice(steps, func(i, j int) bool {
		if steps[i].WorkflowID != steps[j].WorkflowID {
			return steps[i].WorkflowID < steps[j].WorkflowID
		}
		return steps[i].StepID < steps[j].StepID
	})

	return workflows, steps, nil
}

func splitStepKey(key string) [2]string {
	for i := 0; i < len(key); i++ {
		if key[i] == 0 {
			return [2]string{key[:i], key[i+1:]}
		}
	}
	return [2]string{key, ""}
}

// unionDuration merges overlapping intervals and returns their total length.
//
// Summing would count two parallel steps twice and make a fan-out look like it
// took longer than the sequential path it replaced.
// timeInterval is one step run's occupied span.
type timeInterval struct{ start, end time.Time }

func unionDuration(intervals []timeInterval) time.Duration {
	if len(intervals) == 0 {
		return 0
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].start.Before(intervals[j].start) })
	var total time.Duration
	curStart, curEnd := intervals[0].start, intervals[0].end
	for _, iv := range intervals[1:] {
		if iv.start.After(curEnd) {
			total += curEnd.Sub(curStart)
			curStart, curEnd = iv.start, iv.end
			continue
		}
		if iv.end.After(curEnd) {
			curEnd = iv.end
		}
	}
	return total + curEnd.Sub(curStart)
}

// summariseDurations returns nearest-rank percentiles: the smallest sample at or
// above the requested share. It is the convention operators read a p90 as ("90%
// of runs finished within this"), and it never invents a value between samples.
func summariseDurations(samples []time.Duration) DurationStats {
	if len(samples) == 0 {
		return DurationStats{}
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := func(q float64) time.Duration {
		idx := int(math.Ceil(q*float64(len(sorted)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return sorted[idx]
	}
	return DurationStats{
		N:   len(sorted),
		P50: rank(0.50),
		P90: rank(0.90),
		Max: sorted[len(sorted)-1],
	}
}
