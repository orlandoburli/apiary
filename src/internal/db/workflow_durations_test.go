package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// The two durations a run has must not collapse into one. Wall includes every
// wait; active is the union of the step intervals. In this hive a typical
// implementation run is 54 minutes active and 261 minutes wall, so reporting
// one as "the" duration misrepresents the workflow by 5x.
func TestWorkflowDurationsReportWallAndActiveSeparately(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "durations.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Now()
	since := now.Add(-24 * time.Hour)

	mkInstance := func(id, workflowID string, created, finished time.Time) {
		if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
			ID: id, WorkflowID: workflowID, CellID: id, State: "done", CreatedAt: created,
		}); err != nil {
			t.Fatalf("create instance %s: %v", id, err)
		}
		if _, err := c.db.ExecContext(ctx,
			"UPDATE workflow_instances SET updated_at=? WHERE id=?", finished, id); err != nil {
			t.Fatalf("stamp instance %s: %v", id, err)
		}
	}
	mkStep := func(instanceID, stepID string, started, finished time.Time) {
		if err := c.CreateStepRun(ctx, &StepRun{
			ID: instanceID + ":" + stepID, WorkflowInstanceID: instanceID,
			StepID: stepID, AgentID: "engineer", State: "done",
		}); err != nil {
			t.Fatalf("create step %s/%s: %v", instanceID, stepID, err)
		}
		if _, err := c.db.ExecContext(ctx,
			"UPDATE step_runs SET started_at=?, finished_at=? WHERE id=?",
			started, finished, instanceID+":"+stepID); err != nil {
			t.Fatalf("stamp step: %v", err)
		}
	}

	// Run 1: 4h wall, two overlapping steps (1h and 1.5h) whose union is 2h.
	mkInstance("r1", "impl", now.Add(-5*time.Hour), now.Add(-time.Hour))
	mkStep("r1", "implement", now.Add(-5*time.Hour), now.Add(-4*time.Hour))
	mkStep("r1", "review", now.Add(-4*time.Hour-30*time.Minute), now.Add(-3*time.Hour))

	// Run 2: 1h wall, one 1h step.
	mkInstance("r2", "impl", now.Add(-3*time.Hour), now.Add(-2*time.Hour))
	mkStep("r2", "implement", now.Add(-3*time.Hour), now.Add(-2*time.Hour))

	// A run that started before the window but finished inside it: it counts,
	// because that is when it finished.
	mkInstance("r3", "impl", now.Add(-30*time.Hour), now.Add(-time.Hour))
	mkStep("r3", "implement", now.Add(-30*time.Hour), now.Add(-29*time.Hour))

	workflows, steps, err := c.GetWorkflowDurations(ctx, since)
	if err != nil {
		t.Fatalf("GetWorkflowDurations: %v", err)
	}
	if len(workflows) != 1 {
		t.Fatalf("got %d workflows, want 1: %+v", len(workflows), workflows)
	}
	impl := workflows[0]
	if impl.WorkflowID != "impl" || impl.Runs != 3 {
		t.Fatalf("workflow = %+v, want impl with 3 runs", impl)
	}

	// Wall samples: 4h, 1h, 29h. Nearest-rank p50 = 4h, p90 = 29h, max 29h.
	if impl.Wall.P50 != 4*time.Hour || impl.Wall.P90 != 29*time.Hour || impl.Wall.Max != 29*time.Hour {
		t.Errorf("wall = %+v, want p50 4h / p90 29h / max 29h", impl.Wall)
	}
	// Active samples: 2h (union, not 2.5h sum), 1h, 1h.
	if impl.Active.P50 != time.Hour || impl.Active.Max != 2*time.Hour {
		t.Errorf("active = %+v, want p50 1h / max 2h — the union, not the 2.5h sum", impl.Active)
	}
	if impl.Active.Max == 150*time.Minute {
		t.Error("active used the sum of overlapping steps; it must use the union")
	}

	// Per step: implement ran three times (1h, 1h, 1h), review once (1.5h).
	byKey := map[string]StepDurations{}
	for _, s := range steps {
		byKey[s.WorkflowID+"|"+s.StepID] = s
	}
	implement, ok := byKey["impl|implement"]
	if !ok || implement.Runs != 3 {
		t.Fatalf("implement = %+v, want 3 runs", implement)
	}
	if implement.Duration.P50 != time.Hour || implement.Duration.Max != time.Hour {
		t.Errorf("implement duration = %+v, want 1h across the board", implement.Duration)
	}
	if review := byKey["impl|review"]; review.Duration.Max != 90*time.Minute {
		t.Errorf("review = %+v, want a 1.5h max", review)
	}
}

// Percentiles are nearest-rank: p90 is the smallest sample at or above 90% of
// runs, never an interpolated value that no run actually took.
func TestSummariseDurationsUsesNearestRank(t *testing.T) {
	samples := []time.Duration{
		10 * time.Minute, 20 * time.Minute, 30 * time.Minute,
		40 * time.Minute, 50 * time.Minute, 60 * time.Minute,
		70 * time.Minute, 80 * time.Minute, 90 * time.Minute,
		100 * time.Minute,
	}
	got := summariseDurations(samples)
	if got.N != 10 || got.P50 != 50*time.Minute || got.P90 != 90*time.Minute || got.Max != 100*time.Minute {
		t.Errorf("stats = %+v, want n=10 p50=50m p90=90m max=100m", got)
	}
}

// The Overview needs one distribution across every workflow. It cannot be
// built from the per-workflow p90s — a p90 of p90s is not the overall p90 — so
// the samples are aggregated before summarising.
func TestActivityDurationsAggregateAcrossWorkflows(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "activity.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Now()
	since := now.Add(-24 * time.Hour)

	mkInstance := func(id, workflowID string, created, finished time.Time) {
		if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
			ID: id, WorkflowID: workflowID, CellID: id, State: "done", CreatedAt: created,
		}); err != nil {
			t.Fatalf("create instance %s: %v", id, err)
		}
		if _, err := c.db.ExecContext(ctx,
			"UPDATE workflow_instances SET updated_at=? WHERE id=?", finished, id); err != nil {
			t.Fatalf("stamp instance %s: %v", id, err)
		}
	}
	mkStep := func(instanceID, stepID string, started, finished time.Time) {
		if err := c.CreateStepRun(ctx, &StepRun{
			ID: instanceID + ":" + stepID, WorkflowInstanceID: instanceID,
			StepID: stepID, AgentID: "engineer", State: "done",
		}); err != nil {
			t.Fatalf("create step: %v", err)
		}
		if _, err := c.db.ExecContext(ctx,
			"UPDATE step_runs SET started_at=?, finished_at=? WHERE id=?",
			started, finished, instanceID+":"+stepID); err != nil {
			t.Fatalf("stamp step: %v", err)
		}
	}
	mkRun := func(taskID, status string, duration time.Duration, created time.Time) {
		exec, err := c.CreateExecution(ctx, taskID, "engineer", "t", "#1", "", "m", "cli", 1)
		if err != nil {
			t.Fatalf("create execution: %v", err)
		}
		if _, err := c.db.ExecContext(ctx,
			"UPDATE task_executions SET status=?, duration_ms=?, created_at=? WHERE id=?",
			status, duration.Milliseconds(), created, exec.ID); err != nil {
			t.Fatalf("stamp execution: %v", err)
		}
	}

	// Two workflows, so an overall figure that merely echoed one of them would
	// be visible: 4h/2h and 1h/1h.
	mkInstance("a1", "impl", now.Add(-5*time.Hour), now.Add(-time.Hour))
	mkStep("a1", "implement", now.Add(-5*time.Hour), now.Add(-4*time.Hour))
	mkStep("a1", "review", now.Add(-4*time.Hour-30*time.Minute), now.Add(-3*time.Hour))
	mkInstance("b1", "review", now.Add(-3*time.Hour), now.Add(-2*time.Hour))
	mkStep("b1", "approve", now.Add(-3*time.Hour), now.Add(-2*time.Hour))

	mkRun("a1", "success", 30*time.Minute, now.Add(-4*time.Hour))
	mkRun("b1", "failed", 90*time.Minute, now.Add(-3*time.Hour))
	mkRun("b1", "running", time.Hour, now.Add(-2*time.Hour)) // unfinished: no duration

	got, err := c.GetActivityDurations(ctx, since)
	if err != nil {
		t.Fatalf("GetActivityDurations: %v", err)
	}
	if got.Instances.N != 2 || got.Instances.Max != 4*time.Hour || got.Instances.P50 != time.Hour {
		t.Errorf("instances = %+v, want n=2 max=4h p50=1h (nearest rank)", got.Instances)
	}
	if got.Active.N != 2 || got.Active.Max != 2*time.Hour {
		t.Errorf("active = %+v, want n=2 max=2h (the union, not the 2.5h sum)", got.Active)
	}
	if got.Runs.N != 2 || got.Runs.P50 != 30*time.Minute || got.Runs.Max != 90*time.Minute {
		t.Errorf("runs = %+v, want n=2 p50=30m max=90m — the running run has no duration", got.Runs)
	}
}
