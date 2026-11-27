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

// ActivityDurations is the same measurement across every workflow, for the
// Overview.
//
// It cannot be assembled from the per-workflow percentiles: a p90 of p90s is
// not the overall p90. The samples are collected first and summarised once.
type ActivityDurations struct {
	Runs      DurationStats // finished agent executions in the window
	Instances DurationStats // wall, every finished instance
	Active    DurationStats // union of step intervals, every finished instance
}

// activity is the raw sample set both public views are built from.
type activity struct {
	instances []instanceSample
	steps     []stepSample
	runs      []time.Duration
}

type instanceSample struct {
	workflowID string
	wall       time.Duration
	active     time.Duration
}

type stepSample struct {
	workflowID string
	stepID     string
	agentID    string
	duration   time.Duration
}

// GetWorkflowDurations returns, per workflow, the distribution of wall and
// active durations for runs that finished in the window, plus the same per
// step.
//
// A run's active time is the union of its step intervals, not their sum: steps
// that ran in parallel would otherwise be counted twice and a fast fan-out
// would look slower than a sequential one.
func (c *Client) GetWorkflowDurations(ctx context.Context, since time.Time) ([]WorkflowDurations, []StepDurations, error) {
	a, err := c.collectActivity(ctx, since)
	if err != nil {
		return nil, nil, err
	}

	wallByWorkflow := map[string][]time.Duration{}
	activeByWorkflow := map[string][]time.Duration{}
	for _, inst := range a.instances {
		wallByWorkflow[inst.workflowID] = append(wallByWorkflow[inst.workflowID], inst.wall)
		activeByWorkflow[inst.workflowID] = append(activeByWorkflow[inst.workflowID], inst.active)
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

	stepSamples := map[string][]time.Duration{}
	stepAgent := map[string]string{}
	for _, s := range a.steps {
		key := s.workflowID + "\x00" + s.stepID
		stepSamples[key] = append(stepSamples[key], s.duration)
		if s.agentID != "" {
			stepAgent[key] = s.agentID
		}
	}
	steps := make([]StepDurations, 0, len(stepSamples))
	for key, samples := range stepSamples {
		parts := splitStepKey(key)
		steps = append(steps, StepDurations{
			WorkflowID: parts[0],
			StepID:     parts[1],
			AgentID:    stepAgent[key],
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

// GetActivityDurations returns the distributions across every workflow, which
// is what the Overview leads with.
func (c *Client) GetActivityDurations(ctx context.Context, since time.Time) (ActivityDurations, error) {
	a, err := c.collectActivity(ctx, since)
	if err != nil {
		return ActivityDurations{}, err
	}
	walls := make([]time.Duration, 0, len(a.instances))
	active := make([]time.Duration, 0, len(a.instances))
	for _, inst := range a.instances {
		walls = append(walls, inst.wall)
		active = append(active, inst.active)
	}
	return ActivityDurations{
		Runs:      summariseDurations(a.runs),
		Instances: summariseDurations(walls),
		Active:    summariseDurations(active),
	}, nil
}

// collectActivity reads the raw durations both public views are built from.
func (c *Client) collectActivity(ctx context.Context, since time.Time) (activity, error) {
	var out activity

	// Instances that finished in the window. created_at gives the wall start.
	instRows, err := c.db.QueryContext(ctx, `
		SELECT id, workflow_id, created_at, updated_at
		FROM workflow_instances
		WHERE state IN ('done','failed') AND updated_at >= ?
		ORDER BY workflow_id, id
	`, since)
	if err != nil {
		return out, err
	}
	defer instRows.Close()

	workflowOf := map[string]string{}
	wallOf := map[string]time.Duration{}
	for instRows.Next() {
		var (
			id, workflowID string
			created, upd   time.Time
		)
		if err := instRows.Scan(&id, &workflowID, &created, &upd); err != nil {
			return out, err
		}
		workflowOf[id] = workflowID
		wallOf[id] = upd.Sub(created)
	}
	if err := instRows.Err(); err != nil {
		return out, err
	}
	if len(wallOf) == 0 {
		return out, nil
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
		return out, err
	}
	defer stepRows.Close()

	intervalsByInstance := map[string][]timeInterval{}
	for stepRows.Next() {
		var (
			instanceID, stepID, agentID string
			start, end                  time.Time
		)
		if err := stepRows.Scan(&instanceID, &stepID, &agentID, &start, &end); err != nil {
			return out, err
		}
		if end.Before(start) {
			continue
		}
		if _, known := wallOf[instanceID]; !known {
			continue
		}
		intervalsByInstance[instanceID] = append(intervalsByInstance[instanceID], timeInterval{start, end})
		out.steps = append(out.steps, stepSample{
			workflowID: workflowOf[instanceID],
			stepID:     stepID,
			agentID:    agentID,
			duration:   end.Sub(start),
		})
	}
	if err := stepRows.Err(); err != nil {
		return out, err
	}

	for id, wall := range wallOf {
		out.instances = append(out.instances, instanceSample{
			workflowID: workflowOf[id],
			wall:       wall,
			active:     unionDuration(intervalsByInstance[id]),
		})
	}

	// Agent executions are their own distribution: a run is one process, and
	// its duration says how long a single agent took, independent of how many
	// steps a task had.
	runRows, err := c.db.QueryContext(ctx, `
		SELECT duration_ms FROM task_executions
		WHERE status IN ('success','failed') AND created_at >= ? AND duration_ms IS NOT NULL
	`, since)
	if err != nil {
		return out, err
	}
	defer runRows.Close()
	for runRows.Next() {
		var ms int64
		if err := runRows.Scan(&ms); err != nil {
			return out, err
		}
		if ms > 0 {
			out.runs = append(out.runs, time.Duration(ms)*time.Millisecond)
		}
	}
	return out, runRows.Err()
}

func splitStepKey(key string) [2]string {
	for i := 0; i < len(key); i++ {
		if key[i] == 0 {
			return [2]string{key[:i], key[i+1:]}
		}
	}
	return [2]string{key, ""}
}

// timeInterval is one step run's occupied span.
type timeInterval struct{ start, end time.Time }

// unionDuration merges overlapping intervals and returns their total length.
//
// Summing would count two parallel steps twice and make a fan-out look like it
// took longer than the sequential path it replaced.
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
