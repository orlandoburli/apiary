package db

import (
	"context"
	"testing"
	"time"
)

func TestListUnpricedExecutions(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	base := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	ins := func(runner, status string, total int, cost float64, completed any) int64 {
		t.Helper()
		res, err := c.db.ExecContext(ctx, `
			INSERT INTO task_executions
			  (task_id, agent_id, workflow_instance_id, step_id, runner, status,
			   total_tokens, cost_usd, started_at, completed_at, created_at)
			VALUES ('42', 'engineer', 'wf_1', 'implement', ?, ?, ?, ?, ?, ?, ?)`,
			runner, status, total, cost, base, completed, base)
		if err != nil {
			t.Fatalf("insert exec: %v", err)
		}
		id, _ := res.LastInsertId()
		return id
	}

	want := ins("cursor-cli", "success", 1000, 0, base.Add(5*time.Minute))
	ins("cursor-cli", "failed", 500, 0, base.Add(6*time.Minute)) // failed still billed
	ins("cursor-cli", "running", 100, 0, nil)                    // in flight: skip
	ins("cursor-cli", "success", 0, 0, base.Add(5*time.Minute))  // no tokens: skip
	ins("cursor-cli", "success", 900, 0.5, base.Add(5*time.Minute))
	ins("claude-cli", "success", 900, 0, base.Add(5*time.Minute))
	ins("cursor-cli", "success", 800, 0, base.Add(-100*time.Hour)) // too old

	rows, err := c.ListUnpricedExecutions(ctx, "cursor-cli", base.Add(-72*time.Hour))
	if err != nil {
		t.Fatalf("ListUnpricedExecutions: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (success + failed unpriced cursor rows)", len(rows))
	}
	if rows[0].ID != want {
		t.Errorf("rows[0].ID = %d, want %d (oldest completed first)", rows[0].ID, want)
	}
	if rows[0].TotalTokens != 1000 || rows[0].WorkflowInstanceID != "wf_1" || rows[0].StepID != "implement" {
		t.Errorf("row = %+v", rows[0])
	}
	if rows[0].StartedAt.IsZero() || rows[0].CompletedAt.IsZero() {
		t.Errorf("timestamps not scanned: %+v", rows[0])
	}
}

func TestSetExecutionCostAndRefreshStepRun(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	base := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	res, err := c.db.ExecContext(ctx, `
		INSERT INTO task_executions
		  (task_id, agent_id, workflow_instance_id, step_id, runner, status,
		   total_tokens, cost_usd, started_at, completed_at, created_at)
		VALUES ('42', 'engineer', 'wf_1', 'implement', 'cursor-cli', 'success',
		   1000, 0, ?, ?, ?)`, base, base.Add(5*time.Minute), base)
	if err != nil {
		t.Fatalf("insert exec: %v", err)
	}
	execID, _ := res.LastInsertId()
	// A second attempt on the same step already carries cost (failover mix).
	_, err = c.db.ExecContext(ctx, `
		INSERT INTO task_executions
		  (task_id, agent_id, workflow_instance_id, step_id, runner, status,
		   total_tokens, cost_usd, started_at, completed_at, created_at)
		VALUES ('42', 'engineer', 'wf_1', 'implement', 'claude-cli', 'success',
		   500, 0.30, ?, ?, ?)`, base, base.Add(8*time.Minute), base)
	if err != nil {
		t.Fatalf("insert exec 2: %v", err)
	}
	_, err = c.db.ExecContext(ctx, `
		INSERT INTO step_runs (id, workflow_instance_id, step_id, state, cost_usd, started_at)
		VALUES ('sr_1', 'wf_1', 'implement', 'passed', 0.30, ?)`, base)
	if err != nil {
		t.Fatalf("insert step_run: %v", err)
	}

	if err := c.SetExecutionCost(ctx, execID, 1.25); err != nil {
		t.Fatalf("SetExecutionCost: %v", err)
	}
	var cost float64
	var status string
	if err := c.db.QueryRowContext(ctx, `SELECT cost_usd, status FROM task_executions WHERE id = ?`, execID).Scan(&cost, &status); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if cost != 1.25 || status != "success" {
		t.Errorf("execution = $%v/%s, want $1.25 with status untouched", cost, status)
	}

	if err := c.RefreshStepRunCost(ctx, "wf_1", "implement"); err != nil {
		t.Fatalf("RefreshStepRunCost: %v", err)
	}
	var srCost float64
	if err := c.db.QueryRowContext(ctx, `SELECT cost_usd FROM step_runs WHERE id = 'sr_1'`).Scan(&srCost); err != nil {
		t.Fatalf("read step_run: %v", err)
	}
	if srCost != 1.55 {
		t.Errorf("step_run cost = %v, want 1.55 (sum of both attempts)", srCost)
	}

	// Legacy executions without a step link must be a no-op, not an error.
	if err := c.RefreshStepRunCost(ctx, "", ""); err != nil {
		t.Fatalf("RefreshStepRunCost empty link: %v", err)
	}
}
