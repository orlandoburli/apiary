package db

import (
	"context"
	"testing"
)

// ScrubPrompts must delete task_logs rows whose message starts with
// "prompt sent to agent:", clear input_prompt on task_executions and
// step_runs, and leave unrelated rows untouched.
func TestScrubPrompts(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	insert := func(stmt string, args ...any) {
		t.Helper()
		if _, err := c.db.ExecContext(ctx, stmt, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Seed task_logs: two prompt rows and one unrelated row.
	insert(`INSERT INTO task_logs (task_id, level, message) VALUES ('t1', 'DEBUG', 'prompt sent to agent: full ticket text')`)
	insert(`INSERT INTO task_logs (task_id, level, message) VALUES ('t1', 'DEBUG', 'prompt sent to agent: another prompt')`)
	insert(`INSERT INTO task_logs (task_id, level, message) VALUES ('t1', 'INFO',  'task started')`)

	// Seed task_executions: two rows with input_prompt set, one without.
	insert(`INSERT INTO task_executions (task_id, agent_id, status, input_prompt) VALUES ('t1', 'eng', 'success', 'full prompt')`)
	insert(`INSERT INTO task_executions (task_id, agent_id, status, input_prompt) VALUES ('t1', 'eng', 'success', 'another full prompt')`)
	insert(`INSERT INTO task_executions (task_id, agent_id, status) VALUES ('t1', 'eng', 'success')`)

	// Seed step_runs with a workflow_instance_id to satisfy the NOT NULL constraint.
	insert(`INSERT INTO workflow_instances (id, cell_id, workflow_id, state) VALUES ('inst1', 't1', 'wf1', 'passed')`)
	insert(`INSERT INTO step_runs (id, workflow_instance_id, step_id, state, input_prompt) VALUES ('sr1', 'inst1', 'plan', 'passed', 'prompt text')`)
	insert(`INSERT INTO step_runs (id, workflow_instance_id, step_id, state) VALUES ('sr2', 'inst1', 'impl', 'passed')`)

	if err := c.ScrubPrompts(ctx); err != nil {
		t.Fatalf("ScrubPrompts: %v", err)
	}

	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := c.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatalf("count query %q: %v", query, err)
		}
		return n
	}

	// Only the non-prompt task_log row should remain.
	if n := count(`SELECT COUNT(*) FROM task_logs`); n != 1 {
		t.Errorf("task_logs rows = %d, want 1", n)
	}
	if n := count(`SELECT COUNT(*) FROM task_logs WHERE message LIKE 'prompt sent to agent:%'`); n != 0 {
		t.Errorf("prompt task_logs rows = %d, want 0", n)
	}

	// All task_executions.input_prompt must be NULL now.
	if n := count(`SELECT COUNT(*) FROM task_executions WHERE input_prompt IS NOT NULL`); n != 0 {
		t.Errorf("task_executions with input_prompt = %d, want 0", n)
	}

	// All step_runs.input_prompt must be NULL now.
	if n := count(`SELECT COUNT(*) FROM step_runs WHERE input_prompt IS NOT NULL`); n != 0 {
		t.Errorf("step_runs with input_prompt = %d, want 0", n)
	}

	// Calling again is idempotent.
	if err := c.ScrubPrompts(ctx); err != nil {
		t.Fatalf("ScrubPrompts (2nd): %v", err)
	}
}
