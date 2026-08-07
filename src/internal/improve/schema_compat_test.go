package improve

import (
	"database/sql"
	"fmt"
	"net/url"
	"testing"
	"time"
)

// The improve command opens the database read-only and so never migrates it.
// A database written by an older binary is missing columns added since, and
// selecting one is a hard SQL error rather than a null — so every post-release
// column must be probed before use. Without this the command fails outright on
// any database that predates the newest migration.
func TestMetricsToleratePreTimingSchema(t *testing.T) {
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_time_format=sqlite", url.PathEscape(t.Name()))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// step_runs exactly as it looked before the wall-clock columns (#399).
	stmts := []string{
		`CREATE TABLE workflow_instances (
			id TEXT PRIMARY KEY, workflow_id TEXT, state TEXT,
			created_at TIMESTAMP, updated_at TIMESTAMP)`,
		`CREATE TABLE step_runs (
			id TEXT PRIMARY KEY, workflow_instance_id TEXT, step_id TEXT, agent_id TEXT,
			state TEXT, skipped_cached BOOLEAN DEFAULT 0,
			started_at TIMESTAMP, finished_at TIMESTAMP, input_prompt TEXT,
			input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0,
			total_tokens INTEGER DEFAULT 0, cache_read_tokens INTEGER DEFAULT 0,
			num_turns INTEGER DEFAULT 0, num_tool_calls INTEGER DEFAULT 0,
			cost_usd REAL DEFAULT 0)`,
		`CREATE TABLE task_executions (
			id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT, agent_id TEXT, model TEXT,
			runner TEXT, attempt INTEGER DEFAULT 1, status TEXT, started_at TIMESTAMP,
			duration_ms INTEGER, error_message TEXT, workflow_instance_id TEXT,
			step_id TEXT, num_turns INTEGER DEFAULT 0, cost_usd REAL DEFAULT 0,
			failure_kind TEXT)`,
		`CREATE TABLE ci_poll_checks (
			id INTEGER PRIMARY KEY AUTOINCREMENT, workflow_instance_id TEXT,
			step_id TEXT, status TEXT, checked_at TIMESTAMP)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("create legacy schema: %v", err)
		}
	}

	if _, err := db.Exec(`INSERT INTO workflow_instances (id, workflow_id, state, created_at, updated_at)
	                      VALUES ('i1','wf','done',?,?)`, base, base); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO step_runs
		(id, workflow_instance_id, step_id, agent_id, state, started_at, finished_at, cost_usd)
		VALUES ('s1','i1','implement','engineer','passed',?,?,0.5)`, base, base); err != nil {
		t.Fatalf("seed step: %v", err)
	}

	if got := hasColumn(ctx(), db, "step_runs", "time_thinking_ms"); got {
		t.Fatal("fixture is not actually a legacy schema")
	}

	steps, err := StepMetricsFor(ctx(), db, testWindow(), Scope{})
	if err != nil {
		t.Fatalf("StepMetricsFor must work against a pre-timing database: %v", err)
	}
	m := findStep(t, steps, "wf", "implement")
	if m.Runs != 1 {
		t.Errorf("Runs = %d, want 1", m.Runs)
	}
	// The timing metrics are simply absent, not wrong.
	if m.ThinkingShare != 0 || m.WritingShare != 0 || m.ToolWaitShare != 0 {
		t.Errorf("timing shares should be zero on a legacy schema, got %v/%v/%v",
			m.ThinkingShare, m.WritingShare, m.ToolWaitShare)
	}

	// The whole pack must build, not just the step query.
	if _, err := Build(ctx(), db, Options{Window: testWindow(), Clock: func() time.Time { return base }}); err != nil {
		t.Fatalf("Build must work against a pre-timing database: %v", err)
	}
}
