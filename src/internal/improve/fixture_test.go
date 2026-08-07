package improve

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// base is the anchor time for every fixture. Fixed rather than time.Now(), so
// window arithmetic in the tests is exact.
var base = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

func testWindow() Window {
	return Window{Start: base.Add(-24 * time.Hour), End: base.Add(24 * time.Hour)}
}

// seedDB creates an in-memory database with just the tables this package reads.
// It mirrors the production schema's column names and types; using the real
// schema here would drag the whole db package (and its migrations) into a
// read-only analysis test for no benefit.
func seedDB(t *testing.T) *sql.DB {
	t.Helper()

	// The database name is per-test. A shared name plus cache=shared would give
	// every test in the package one database, so rows seeded by one test would
	// silently appear in the next.
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_time_format=sqlite", url.PathEscape(t.Name()))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	stmts := []string{
		`CREATE TABLE workflow_instances (
			id TEXT PRIMARY KEY, workflow_id TEXT, cell_id TEXT, source_id TEXT,
			state TEXT, parent_instance_id TEXT, resumed_from TEXT,
			created_at TIMESTAMP, updated_at TIMESTAMP)`,
		`CREATE TABLE step_runs (
			id TEXT PRIMARY KEY, workflow_instance_id TEXT, step_id TEXT, agent_id TEXT,
			state TEXT, output TEXT, structured_output TEXT, summary TEXT, exit_code INTEGER,
			skipped_cached BOOLEAN DEFAULT 0, started_at TIMESTAMP, finished_at TIMESTAMP,
			input_prompt TEXT, input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0,
			total_tokens INTEGER DEFAULT 0, cache_creation_tokens INTEGER DEFAULT 0,
			cache_read_tokens INTEGER DEFAULT 0, num_turns INTEGER DEFAULT 0,
			num_tool_calls INTEGER DEFAULT 0, cost_usd REAL DEFAULT 0,
			time_thinking_ms INTEGER DEFAULT 0, time_writing_ms INTEGER DEFAULT 0,
			time_model_ms INTEGER DEFAULT 0, time_tool_wait_ms INTEGER DEFAULT 0,
			time_other_ms INTEGER DEFAULT 0, time_background_ms INTEGER DEFAULT 0,
			slow_tools TEXT)`,
		`CREATE TABLE task_executions (
			id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT, agent_id TEXT, model TEXT,
			runner TEXT, attempt INTEGER DEFAULT 1, status TEXT,
			started_at TIMESTAMP, completed_at TIMESTAMP, duration_ms INTEGER,
			error_message TEXT, workflow_instance_id TEXT, step_id TEXT,
			input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0,
			total_tokens INTEGER DEFAULT 0, cache_read_tokens INTEGER DEFAULT 0,
			num_turns INTEGER DEFAULT 0, num_tool_calls INTEGER DEFAULT 0,
			cost_usd REAL DEFAULT 0, credit_exhausted INTEGER DEFAULT 0, failure_kind TEXT)`,
		`CREATE TABLE ci_poll_checks (
			id INTEGER PRIMARY KEY AUTOINCREMENT, workflow_instance_id TEXT, step_id TEXT,
			status TEXT, pr_url TEXT, detail TEXT, checked_at TIMESTAMP)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}
	return db
}

// instanceOpts describes one workflow instance to seed.
type instanceOpts struct {
	id         string
	workflowID string
	state      string
	createdAt  time.Time
	durationMs int64
}

func addInstance(t *testing.T, db *sql.DB, o instanceOpts) {
	t.Helper()
	if o.createdAt.IsZero() {
		o.createdAt = base
	}
	updated := o.createdAt.Add(time.Duration(o.durationMs) * time.Millisecond)
	_, err := db.Exec(`INSERT INTO workflow_instances (id, workflow_id, state, created_at, updated_at)
	                   VALUES (?,?,?,?,?)`, o.id, o.workflowID, o.state, o.createdAt, updated)
	if err != nil {
		t.Fatalf("insert instance: %v", err)
	}
}

// stepOpts describes one step run to seed.
type stepOpts struct {
	id         string
	instanceID string
	stepID     string
	agentID    string
	state      string
	startedAt  time.Time
	durationMs int64
	tokens     int64
	inputTok   int64
	outputTok  int64
	cacheRead  int64
	prompt     string
	cost       float64
	turns      int64
	toolCalls  int64
	cached     bool
	thinkingMS int64
	writingMS  int64
	toolWaitMS int64
}

func addStep(t *testing.T, db *sql.DB, o stepOpts) {
	t.Helper()
	if o.startedAt.IsZero() {
		o.startedAt = base
	}
	if o.state == "" {
		o.state = "passed"
	}
	finished := o.startedAt.Add(time.Duration(o.durationMs) * time.Millisecond)
	cached := 0
	if o.cached {
		cached = 1
	}
	_, err := db.Exec(`INSERT INTO step_runs
		(id, workflow_instance_id, step_id, agent_id, state, skipped_cached,
		 started_at, finished_at, input_prompt, input_tokens, output_tokens,
		 total_tokens, cache_read_tokens, num_turns, num_tool_calls, cost_usd,
		 time_thinking_ms, time_writing_ms, time_tool_wait_ms)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		o.id, o.instanceID, o.stepID, o.agentID, o.state, cached,
		o.startedAt, finished, o.prompt, o.inputTok, o.outputTok,
		o.tokens, o.cacheRead, o.turns, o.toolCalls, o.cost,
		o.thinkingMS, o.writingMS, o.toolWaitMS)
	if err != nil {
		t.Fatalf("insert step run: %v", err)
	}
}

// execOpts describes one runner invocation to seed.
type execOpts struct {
	taskID      string
	agentID     string
	runner      string
	model       string
	attempt     int
	status      string
	startedAt   time.Time
	durationMs  int64
	errMessage  string
	instanceID  string
	stepID      string
	turns       int64
	cost        float64
	failureKind string
}

func addExec(t *testing.T, db *sql.DB, o execOpts) {
	t.Helper()
	if o.startedAt.IsZero() {
		o.startedAt = base
	}
	if o.attempt == 0 {
		o.attempt = 1
	}
	if o.status == "" {
		o.status = "success"
	}
	_, err := db.Exec(`INSERT INTO task_executions
		(task_id, agent_id, runner, model, attempt, status, started_at, duration_ms,
		 error_message, workflow_instance_id, step_id, num_turns, cost_usd, failure_kind)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		o.taskID, o.agentID, o.runner, o.model, o.attempt, o.status, o.startedAt,
		o.durationMs, o.errMessage, o.instanceID, o.stepID, o.turns, o.cost, o.failureKind)
	if err != nil {
		t.Fatalf("insert execution: %v", err)
	}
}

func addPoll(t *testing.T, db *sql.DB, instanceID, stepID, status string, at time.Time) {
	t.Helper()
	if at.IsZero() {
		at = base
	}
	_, err := db.Exec(`INSERT INTO ci_poll_checks (workflow_instance_id, step_id, status, checked_at)
	                   VALUES (?,?,?,?)`, instanceID, stepID, status, at)
	if err != nil {
		t.Fatalf("insert poll: %v", err)
	}
}

// findStep returns the metrics for one (workflow, step) or fails the test.
func findStep(t *testing.T, steps []StepMetrics, workflow, step string) StepMetrics {
	t.Helper()
	for _, s := range steps {
		if s.WorkflowID == workflow && s.StepID == step {
			return s
		}
	}
	t.Fatalf("no metrics for %s/%s in %v", workflow, step, stepKeys(steps))
	return StepMetrics{}
}

func stepKeys(steps []StepMetrics) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, fmt.Sprintf("%s/%s", s.WorkflowID, s.StepID))
	}
	return out
}

func ctx() context.Context { return context.Background() }
