package db

import (
	"context"
	"strings"
	"testing"
	"time"
)

// GetInstanceStepUsage returns the latest token/cost totals per step for a whole
// instance in one query (the batched replacement for per-step GetStepUsage).
func TestGetInstanceStepUsage(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	base := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	ins := func(step string, total int, cost float64, at time.Time) {
		t.Helper()
		_, err := c.db.ExecContext(ctx, `
			INSERT INTO task_executions
			  (task_id, agent_id, workflow_instance_id, step_id, status,
			   input_tokens, output_tokens, total_tokens, cost_usd, created_at)
			VALUES (?, ?, ?, ?, 'done', ?, ?, ?, ?, ?)`,
			"42", "engineer", "wf_1", step, total/2, total/2, total, cost, at)
		if err != nil {
			t.Fatalf("insert exec: %v", err)
		}
	}
	// 'implement' ran twice (a re-dispatch); the later row must win.
	ins("implement", 100, 0.10, base)
	ins("implement", 200, 0.20, base.Add(time.Minute))
	ins("review", 50, 0.05, base.Add(2*time.Minute))
	// A different instance must not leak in.
	_, _ = c.db.ExecContext(ctx, `INSERT INTO task_executions
		(task_id, agent_id, workflow_instance_id, step_id, status, total_tokens, created_at)
		VALUES ('42','engineer','wf_other','implement','done',9999,?)`, base)

	usage, err := c.GetInstanceStepUsage(ctx, "wf_1")
	if err != nil {
		t.Fatalf("GetInstanceStepUsage: %v", err)
	}
	if got := usage["implement"].TotalTokens; got != 200 {
		t.Errorf("implement TotalTokens = %d, want 200 (latest execution wins)", got)
	}
	if got := usage["implement"].CostUSD; got != 0.20 {
		t.Errorf("implement CostUSD = %v, want 0.20", got)
	}
	if got := usage["review"].TotalTokens; got != 50 {
		t.Errorf("review TotalTokens = %d, want 50", got)
	}
	// wf_other's row (9999) must not leak in: only the two wf_1 steps.
	if len(usage) != 2 {
		t.Errorf("usage has %d steps, want 2 (no cross-instance leak)", len(usage))
	}
}

// New opens the DB in WAL journal mode so the dashboard's large cold log reads
// don't block the daemon's writes.
func TestNew_WALEnabled(t *testing.T) {
	c := newTestClient(t)
	var mode string
	if err := c.db.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}
