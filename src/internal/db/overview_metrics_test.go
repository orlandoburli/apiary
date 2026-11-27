package db

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

// The Overview mixes two units, and they must not be confused: tasks are what
// a person counts, runs are the agent processes underneath them. The old
// figures counted runs, divided a day that had not happened yet, put running
// rows in the success denominator, and measured a task's "duration" from its
// creation days earlier.
func TestDashboardStatsSeparateTasksFromRuns(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Now()
	windowStart := now.Add(-6 * time.Hour)

	mkTask := func(id, state string, created, updated time.Time) {
		if err := c.InternalTasks().CreateTask(ctx, &model.InternalTask{
			ID: id, Title: id, State: model.TaskStateRunning,
		}); err != nil {
			t.Fatalf("create task %s: %v", id, err)
		}
		if _, err := c.db.ExecContext(ctx,
			"UPDATE internal_tasks SET state=?, created_at=?, updated_at=? WHERE id=?",
			state, created, updated, id); err != nil {
			t.Fatalf("stamp task %s: %v", id, err)
		}
	}

	// One task that both started and finished inside the window: its duration
	// is the only one that says anything about how long today's work takes.
	mkTask("T-new-done", "done", now.Add(-2*time.Hour), now.Add(-1*time.Hour))
	// Finished inside the window but created days ago: it must count as
	// finished, and must NOT contribute its multi-day life to the average.
	mkTask("T-old-done", "done", now.AddDate(0, 0, -3), now.Add(-time.Hour))
	mkTask("T-old-failed", "failed", now.AddDate(0, 0, -3), now.Add(-30*time.Minute))

	mkRun := func(taskID, status string, duration time.Duration, created time.Time) {
		exec, err := c.CreateExecution(ctx, taskID, "engineer", "t", "#1", "", "m", "cli", 1)
		if err != nil {
			t.Fatalf("create execution for %s: %v", taskID, err)
		}
		if _, err := c.db.ExecContext(ctx,
			"UPDATE task_executions SET status=?, duration_ms=?, created_at=? WHERE id=?",
			status, duration.Milliseconds(), created, exec.ID); err != nil {
			t.Fatalf("stamp execution for %s: %v", taskID, err)
		}
	}
	mkRun("T-new-done", "success", time.Minute, now.Add(-3*time.Hour))
	mkRun("T-new-done", "success", 2*time.Minute, now.Add(-2*time.Hour))
	mkRun("T-old-failed", "failed", 30*time.Second, now.Add(-time.Hour))
	mkRun("T-old-done", "running", 0, now.Add(-4*time.Hour))
	mkRun("T-old-done", "success", time.Hour, now.AddDate(0, 0, -2)) // outside window

	stats, err := c.GetDashboardStats(ctx, windowStart)
	if err != nil {
		t.Fatalf("GetDashboardStats: %v", err)
	}

	if stats.TasksCompleted != 2 || stats.TasksFailed != 1 {
		t.Errorf("tasks = %d done / %d failed, want 2/1", stats.TasksCompleted, stats.TasksFailed)
	}
	if got := time.Duration(stats.AvgTaskDurationMs) * time.Millisecond; math.Abs(got.Hours()-1) > 0.001 {
		t.Errorf("AvgTaskDurationMs = %v, want 1h — only the task created inside the window", got)
	}
	if math.Abs(stats.SuccessRate-2.0/3.0) > 1e-9 {
		t.Errorf("SuccessRate = %v, want 2/3 of finished tasks", stats.SuccessRate)
	}

	if stats.RunsCompleted != 2 || stats.RunsFailed != 1 {
		t.Errorf("runs = %d ok / %d failed, want 2/1 (running and out-of-window excluded)",
			stats.RunsCompleted, stats.RunsFailed)
	}
	if got := time.Duration(stats.AvgRunDurationMs) * time.Millisecond; math.Abs(got.Seconds()-70) > 1 {
		t.Errorf("AvgRunDurationMs = %v, want 70s over finished runs only", got)
	}
	if stats.ActiveRuns != 1 {
		t.Errorf("ActiveRuns = %d, want 1 — a right-now figure, not a windowed one", stats.ActiveRuns)
	}

	// Six-hour window: 2 tasks and 3 finished runs.
	if math.Abs(stats.TasksPerHour-2.0/6.0) > 1e-3 {
		t.Errorf("TasksPerHour = %v, want %v", stats.TasksPerHour, 2.0/6.0)
	}
	if math.Abs(stats.RunsPerHour-3.0/6.0) > 1e-3 {
		t.Errorf("RunsPerHour = %v, want %v", stats.RunsPerHour, 3.0/6.0)
	}
}

// Just after midnight the window is minutes long; dividing by it would turn a
// single task into a huge rate. The denominator is clamped to one hour.
func TestDashboardStatsRateIsClampedForATinyWindow(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "clamp.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Now()
	if err := c.InternalTasks().CreateTask(ctx, &model.InternalTask{
		ID: "T-1", Title: "one", State: model.TaskStateRunning,
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := c.db.ExecContext(ctx,
		"UPDATE internal_tasks SET state='done', created_at=?, updated_at=? WHERE id='T-1'",
		now.Add(-time.Minute), now); err != nil {
		t.Fatalf("stamp task: %v", err)
	}

	stats, err := c.GetDashboardStats(ctx, now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("GetDashboardStats: %v", err)
	}
	if stats.TasksPerHour > 1.0001 {
		t.Errorf("TasksPerHour = %v for one task in one minute, want it clamped to 1", stats.TasksPerHour)
	}
}
