package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Per-workflow metrics follow the same cohort rule as the Overview: an instance
// that started inside the window counts as a run, one that finished inside it
// counts as a completion, and only instances that did both contribute to the
// average duration. Otherwise a workflow blocked for three days and finished
// this morning reports a three-day "duration".
func TestWorkflowMetricsSeparateRunsFromCompletions(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "wf-metrics.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Now()
	since := now.Add(-6 * time.Hour)

	mkInstance := func(id, workflowID, state string, created, updated time.Time) {
		if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
			ID: id, WorkflowID: workflowID, CellID: id, State: state, CreatedAt: created,
		}); err != nil {
			t.Fatalf("create instance %s: %v", id, err)
		}
		if _, err := c.db.ExecContext(ctx,
			"UPDATE workflow_instances SET updated_at=? WHERE id=?", updated, id); err != nil {
			t.Fatalf("stamp instance %s: %v", id, err)
		}
	}

	// workflow A: one run started and finished in the window, one finished in
	// the window but started three days ago, one failed in the window.
	mkInstance("a1", "impl", "done", now.Add(-2*time.Hour), now.Add(-time.Hour))
	mkInstance("a2", "impl", "done", now.AddDate(0, 0, -3), now.Add(-time.Hour))
	mkInstance("a3", "impl", "failed", now.Add(-time.Hour), now.Add(-30*time.Minute))
	// workflow B: started, still running.
	mkInstance("b1", "review", "running", now.Add(-time.Hour), now)

	mkStep := func(instanceID, stepID, agent, state string, started, finished time.Time) {
		if err := c.CreateStepRun(ctx, &StepRun{
			ID: instanceID + ":" + stepID, WorkflowInstanceID: instanceID,
			StepID: stepID, AgentID: agent, State: state,
		}); err != nil {
			t.Fatalf("create step run %s/%s: %v", instanceID, stepID, err)
		}
		if _, err := c.db.ExecContext(ctx,
			"UPDATE step_runs SET started_at=?, finished_at=? WHERE id=?",
			started, finished, instanceID+":"+stepID); err != nil {
			t.Fatalf("stamp step run: %v", err)
		}
	}
	mkStep("a1", "implement", "engineer", "done", now.Add(-2*time.Hour), now.Add(-90*time.Minute))
	mkStep("a1", "review", "reviewer", "failed", now.Add(-90*time.Minute), now.Add(-time.Hour))
	// a2 started before the window: it must not count as a step run today.
	mkStep("a2", "implement", "engineer", "done", now.AddDate(0, 0, -3), now.Add(-2*time.Hour))

	workflows, steps, err := c.GetWorkflowMetrics(ctx, since)
	if err != nil {
		t.Fatalf("GetWorkflowMetrics: %v", err)
	}

	byID := map[string]WorkflowMetrics{}
	for _, w := range workflows {
		byID[w.WorkflowID] = w
	}
	impl, ok := byID["impl"]
	if !ok {
		t.Fatalf("impl missing from %+v", workflows)
	}
	if impl.Runs != 2 {
		t.Errorf("impl runs = %d, want 2 (a1 and a3 started in the window)", impl.Runs)
	}
	if impl.Completed != 2 || impl.Failed != 1 {
		t.Errorf("impl = %d completed / %d failed, want 2/1", impl.Completed, impl.Failed)
	}
	// a1 (1h) and a3 (30m) both started and finished inside the window; a2's
	// three-day life is excluded because it started before it.
	if got := time.Duration(impl.AvgDurationMs) * time.Millisecond; got < 44*time.Minute || got > 46*time.Minute {
		t.Errorf("impl avg duration = %v, want ~45m (only the coherent cohort)", got)
	}
	if review, ok := byID["review"]; !ok || review.Runs != 1 || review.Completed != 0 {
		t.Errorf("review = %+v, want one run and no completions", review)
	}

	stepByKey := map[string]StepMetrics{}
	for _, s := range steps {
		stepByKey[s.WorkflowID+"|"+s.StepID] = s
	}
	implStep, ok := stepByKey["impl|implement"]
	if !ok {
		t.Fatalf("impl/implement missing from %+v", steps)
	}
	if implStep.Runs != 1 || implStep.Completed != 1 || implStep.AgentID != "engineer" {
		t.Errorf("impl/implement = %+v, want one successful run by engineer", implStep)
	}
	if got := time.Duration(implStep.AvgDurationMs) * time.Millisecond; got < 29*time.Minute || got > 31*time.Minute {
		t.Errorf("impl/implement avg = %v, want ~30m", got)
	}
	if reviewStep := stepByKey["impl|review"]; reviewStep.Failed != 1 {
		t.Errorf("impl/review = %+v, want one failure", reviewStep)
	}
}
