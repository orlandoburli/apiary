package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// The Overview's "Agents: N active" counter is the number of distinct agents
// that ran in the last hour. It used to read
//
//	WHERE created_at >= datetime('now', '-1 hour')
//
// which never matched anything in a negative-offset timezone: created_at is
// written by the Go driver as a *local* timestamp with its offset
// ("2026-11-16 16:01:00.08254-04:00"), while SQLite's datetime('now') returns
// *UTC* ("2026-11-16 20:01:00"). The two are compared as strings, so the
// stored local time always sorted before the UTC cutoff and the Overview
// reported 0 active agents however many had just run.
//
// The cutoff is now computed in Go and passed as a parameter, which the driver
// serializes in the same format the rows are stored in.
func TestDashboardStatsActiveAgentsCountsRecentRuns(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// One execution just now, one two hours ago.
	now := time.Now()
	if _, err := c.CreateExecution(ctx, "T-fresh", "engineer", "recent work", "#1", "", "m", "cli", 1); err != nil {
		t.Fatalf("create fresh execution: %v", err)
	}
	old := &Execution{
		TaskID: "T-old", AgentID: "investigator", Title: "older work",
		Status: "success", StartedAt: &oldTime, CreatedAt: oldTime,
	}
	if err := c.UpdateExecution(ctx, old); err != nil {
		t.Fatalf("record older execution: %v", err)
	}
	// Backdate the older row so it falls outside the hour.
	if _, err := c.db.ExecContext(ctx,
		"UPDATE task_executions SET created_at = ? WHERE task_id = 'T-old'",
		now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("backdate older execution: %v", err)
	}

	stats, err := c.GetDashboardStats(ctx, now.AddDate(0, 0, -1))
	if err != nil {
		t.Fatalf("GetDashboardStats: %v", err)
	}
	if stats.ActiveAgents != 1 {
		t.Errorf("ActiveAgents = %d, want 1 (only the agent that ran within the hour)", stats.ActiveAgents)
	}
}

var oldTime = time.Now().Add(-2 * time.Hour)

// A run that happened just now must count, and one two hours ago must not.
// This is the same assertion as above, spelled out per agent, so a regression
// names the agent that went missing rather than just a count.
func TestDashboardStatsActiveAgentsWindow(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "window.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Now()
	seed := func(taskID, agentID string, age time.Duration) {
		if _, err := c.CreateExecution(ctx, taskID, agentID, "t", taskID, "", "m", "cli", 1); err != nil {
			t.Fatalf("create %s: %v", taskID, err)
		}
		if _, err := c.db.ExecContext(ctx,
			"UPDATE task_executions SET created_at = ? WHERE task_id = ?",
			now.Add(-age), taskID); err != nil {
			t.Fatalf("age %s: %v", taskID, err)
		}
	}

	seed("T-1", "engineer", 5*time.Minute)
	seed("T-2", "investigator", 30*time.Minute)
	seed("T-3", "qa", 2*time.Hour)
	seed("T-4", "reviewer", 5*time.Hour)

	stats, err := c.GetDashboardStats(ctx, now.AddDate(0, 0, -1))
	if err != nil {
		t.Fatalf("GetDashboardStats: %v", err)
	}
	if stats.ActiveAgents != 2 {
		t.Errorf("ActiveAgents = %d, want 2 (engineer and investigator ran within the hour)", stats.ActiveAgents)
	}
}

// GetDailyUsage had the same UTC-vs-local comparison on its window, so the
// most recent hours of the newest day were cut off in a negative-offset zone.
func TestGetDailyUsageIncludesRecentRows(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Now()
	if _, err := c.CreateExecution(ctx, "T-1", "engineer", "t", "#1", "", "m", "cli", 1); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	if _, err := c.db.ExecContext(ctx,
		"UPDATE task_executions SET total_tokens = 100, created_at = ? WHERE task_id = 'T-1'",
		now); err != nil {
		t.Fatalf("stamp usage: %v", err)
	}

	rows, err := c.GetDailyUsage(ctx, 7)
	if err != nil {
		t.Fatalf("GetDailyUsage: %v", err)
	}
	var total int
	for _, r := range rows {
		total += r.TotalTokens
	}
	if total != 100 {
		t.Errorf("total tokens = %d, want 100 (a row written just now must be inside the window)", total)
	}
}
