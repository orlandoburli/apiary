package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// LiveAgentCounts reports the agent processes that are actually running, by the
// same liveness rule the dashboard's Running tab uses.
//
// The daemon's status used to report the number of dispatch slots an agent's
// semaphore held, which understates the work in flight: a workflow instance
// takes one slot for its whole run, but the run may fan out to several agent
// processes at once.
func TestLiveAgentCountsCountsRunningProcesses(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Now()
	seed := func(taskID, agentID string, pid int, hb *time.Time) {
		t.Helper()
		exec, err := c.CreateExecution(ctx, taskID, agentID, "work", taskID, "", "m", "cli", 1)
		if err != nil {
			t.Fatalf("create execution %s: %v", taskID, err)
		}
		if pid > 0 {
			if err := c.SetPID(ctx, exec.ID, pid); err != nil {
				t.Fatalf("set pid: %v", err)
			}
		}
	}

	// Two live engineers (a fan-out of one workflow), one live qa.
	fresh := now.Add(-5 * time.Second)
	seed("T-1", "engineer", 111, &fresh)
	seed("T-2", "engineer", 112, &fresh)
	seed("T-3", "qa", 113, &fresh)

	// A dead engineer: the heartbeat is old, so it is not running any more.
	// The heartbeat is aged directly, because SetPID always stamps "now".
	dead := now.Add(-2 * time.Hour)
	seed("T-4", "engineer", 114, nil)
	if _, err := c.db.ExecContext(ctx,
		"UPDATE task_executions SET heartbeat_at = ? WHERE task_id = 'T-4'",
		dead.UTC().Format("2006-01-02 15:04:05")); err != nil {
		t.Fatalf("age heartbeat: %v", err)
	}

	counts, err := c.LiveAgentCounts(ctx)
	if err != nil {
		t.Fatalf("LiveAgentCounts: %v", err)
	}
	if counts["engineer"] != 2 {
		t.Errorf("engineer count = %d, want 2 (the two live ones; the stale row is not running)", counts["engineer"])
	}
	if counts["qa"] != 1 {
		t.Errorf("qa count = %d, want 1", counts["qa"])
	}
}

// A finished execution is not running, whatever its heartbeat says.
func TestLiveAgentCountsIgnoresFinishedExecutions(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "live2.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	exec, err := c.CreateExecution(ctx, "T-1", "engineer", "work", "T-1", "", "m", "cli", 1)
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}
	if err := c.SetPID(ctx, exec.ID, 111); err != nil {
		t.Fatalf("set pid: %v", err)
	}
	exec.Status = "success"
	now := time.Now()
	exec.CompletedAt = &now
	if err := c.UpdateExecution(ctx, exec); err != nil {
		t.Fatalf("finish execution: %v", err)
	}

	counts, err := c.LiveAgentCounts(ctx)
	if err != nil {
		t.Fatalf("LiveAgentCounts: %v", err)
	}
	if counts["engineer"] != 0 {
		t.Errorf("engineer count = %d, want 0 (a finished execution is not running)", counts["engineer"])
	}
}
