package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type Client struct {
	db *sql.DB
}

// execer is the subset of *sql.DB / *sql.Tx used by the insert helpers, so the
// same INSERT logic can run either directly or inside a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// New opens a SQLite database and initializes schema.
func New(ctx context.Context, dbPath string) (*Client, error) {
	// Connection pragmas:
	//   busy_timeout  — wait for a held lock instead of failing immediately with
	//                   SQLITE_BUSY (concurrent SourceBinder path: two pollers may
	//                   bind the same item at once).
	//   journal_mode=WAL — readers don't block the writer and vice versa, so the
	//                   dashboard's large cold log reads don't stall the daemon.
	//   synchronous=NORMAL — safe under WAL, far fewer fsyncs.
	//   cache_size=-20000 — ~20MB page cache; task_logs rows are ~10KB of agent
	//                   stream text, so a bigger cache cuts cold-read disk hits.
	//   temp_store=MEMORY — sorts/temp b-trees stay in RAM.
	//   _time_format=sqlite — write time.Time as "2006-01-02 15:04:05.999999999
	//                   -07:00". modernc's default is time.Time.String(), which
	//                   appends " MST m=±…" (monotonic clock) — a form SQLite's
	//                   DATE()/datetime() cannot parse, so every windowed/grouped
	//                   query (dashboard daily charts, 24h totals) silently drops
	//                   those rows. See normalizeLegacyTimestamps for the backfill.
	dsn := dbPath
	if !strings.Contains(dsn, "?") {
		dsn += "?_pragma=busy_timeout(5000)" +
			"&_pragma=journal_mode(WAL)" +
			"&_pragma=synchronous(NORMAL)" +
			"&_pragma=cache_size(-20000)" +
			"&_pragma=temp_store(MEMORY)" +
			"&_time_format=sqlite"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Test connection
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}

	// Initialize schema
	if err := InitSchema(ctx, db); err != nil {
		db.Close()
		return nil, err
	}

	c := &Client{db: db}
	return c, nil
}

func (c *Client) Close() error {
	return c.db.Close()
}

// ErrBindingExists is returned by CreateTaskWithBinding when a binding for the
// (source_id, source_item_id) pair already exists. The transaction is rolled
// back (so no orphan task remains) and the caller should re-fetch the winning
// task via SourceBindingStore.GetBindingBySourceItem.
var ErrBindingExists = errors.New("source binding already exists")

// CreateTaskWithBinding atomically inserts an InternalTask and a SourceBinding
// referencing it, in a single transaction. The binding's TaskID is set to the
// (possibly generated) task ID before insertion. If the binding violates the
// UNIQUE(source_id, source_item_id) constraint — i.e. a concurrent bind won the
// race — the transaction rolls back and ErrBindingExists is returned, leaving no
// orphan task behind.
func (c *Client) CreateTaskWithBinding(ctx context.Context, task *model.InternalTask, binding *model.SourceBinding) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := insertTask(ctx, tx, task); err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	binding.TaskID = task.ID
	if err := insertBinding(ctx, tx, binding); err != nil {
		if isUniqueViolation(err) {
			return ErrBindingExists
		}
		return fmt.Errorf("insert binding: %w", err)
	}
	return tx.Commit()
}

// isUniqueViolation reports whether err is a SQLite UNIQUE-constraint failure.
func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
	}
	return false
}

// Task operations

func (c *Client) CreateTask(ctx context.Context, id, sourceID, title, agentID string) error {
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO tasks (id, source_id, title, agent_id, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, id, sourceID, title, agentID, "pending", time.Now(), time.Now())
	return err
}

func (c *Client) UpdateTaskState(ctx context.Context, taskID, state string) error {
	_, err := c.db.ExecContext(ctx, `
		UPDATE tasks SET state = ?, updated_at = ? WHERE id = ?
	`, state, time.Now(), taskID)
	return err
}

func (c *Client) UpdateTaskOutput(ctx context.Context, taskID, output string, success bool, durationMs int64) error {
	_, err := c.db.ExecContext(ctx, `
		UPDATE tasks
		SET success = ?, output = ?, full_output = ?, duration_ms = ?, completed_at = ?, updated_at = ?
		WHERE id = ?
	`, success, output, output, durationMs, time.Now(), time.Now(), taskID)
	return err
}

// Execution tracking (for crash recovery)

type Execution struct {
	ID                  int64
	TaskID              string
	AgentID             string
	Title               string
	Number              string
	URL                 string
	Model               string
	Runner              string
	Attempt             int
	Status              string
	PID                 int
	HeartbeatAt         *time.Time
	HeartbeatCount      int
	StartedAt           *time.Time
	CompletedAt         *time.Time
	DurationMs          int64
	ErrorMsg            string
	CanRetry            bool
	NextRetryAt         *time.Time
	CreatedAt           time.Time
	InputTokens         int
	OutputTokens        int
	TotalTokens         int
	CacheCreationTokens int
	CacheReadTokens     int
	NumTurns            int
	NumToolCalls        int
	CostUSD             float64
	WorkflowInstanceID  string
	StepID              string
	// InputPrompt is the full composed prompt sent to the agent for this attempt;
	// OutputText is the agent's raw output. Both are persisted for cost auditing
	// and replay. Empty when the runner does not report a prompt.
	InputPrompt string
	OutputText  string
}

func (c *Client) CreateExecution(ctx context.Context, taskID, agentID, title, number, taskURL, model, runner string, attempt int) (*Execution, error) {
	now := time.Now()
	res, err := c.db.ExecContext(ctx, `
		INSERT INTO task_executions (task_id, agent_id, title, task_number, task_url, model, runner, attempt, status, started_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, taskID, agentID, title, number, taskURL, model, runner, attempt, "running", now, now)
	if err != nil {
		return nil, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	return &Execution{
		ID:        id,
		TaskID:    taskID,
		AgentID:   agentID,
		Title:     title,
		Number:    number,
		URL:       taskURL,
		Model:     model,
		Runner:    runner,
		Attempt:   attempt,
		Status:    "running",
		StartedAt: &now,
		CreatedAt: now,
	}, nil
}

func (c *Client) UpdateExecution(ctx context.Context, exec *Execution) error {
	_, err := c.db.ExecContext(ctx, `
		UPDATE task_executions
		SET status = ?, completed_at = ?, duration_ms = ?, error_message = ?, can_retry = ?, next_retry_at = ?,
		    input_tokens = ?, output_tokens = ?, total_tokens = ?, cache_creation_tokens = ?, cache_read_tokens = ?,
		    num_turns = ?, num_tool_calls = ?, cost_usd = ?,
		    input_prompt = ?, output_text = ?
		WHERE id = ?
	`, exec.Status, exec.CompletedAt, exec.DurationMs, exec.ErrorMsg, exec.CanRetry, exec.NextRetryAt,
		exec.InputTokens, exec.OutputTokens, exec.TotalTokens, exec.CacheCreationTokens, exec.CacheReadTokens,
		exec.NumTurns, exec.NumToolCalls, exec.CostUSD,
		nullStr(exec.InputPrompt), nullStr(exec.OutputText),
		exec.ID)
	return err
}

// SetPID stores the OS PID for a running execution.
func (c *Client) SetPID(ctx context.Context, execID int64, pid int) error {
	_, err := c.db.ExecContext(ctx, `
		UPDATE task_executions SET pid = ?, heartbeat_at = ?, heartbeat_count = 1
		WHERE id = ?
	`, pid, time.Now(), execID)
	return err
}

// SendHeartbeat updates the heartbeat timestamp and counter for a running execution.
func (c *Client) SendHeartbeat(ctx context.Context, execID int64) error {
	_, err := c.db.ExecContext(ctx, `
		UPDATE task_executions
		SET heartbeat_at = ?, heartbeat_count = COALESCE(heartbeat_count, 0) + 1
		WHERE id = ?
	`, time.Now(), execID)
	return err
}

// SetStepLink associates a task_execution row with its workflow instance and step,
// enabling per-step usage queries from the dashboard.
func (c *Client) SetStepLink(ctx context.Context, execID int64, instanceID, stepID string) error {
	_, err := c.db.ExecContext(ctx, `
		UPDATE task_executions SET workflow_instance_id = ?, step_id = ? WHERE id = ?
	`, nullStr(instanceID), nullStr(stepID), execID)
	return err
}

// GetStepUsage returns token and cost totals for the most recent execution of a
// specific step within a workflow instance.
func (c *Client) GetStepUsage(ctx context.Context, instanceID, stepID string) (*Execution, error) {
	row := c.db.QueryRowContext(ctx, `
		SELECT input_tokens, output_tokens, total_tokens,
		       COALESCE(cache_creation_tokens,0), COALESCE(cache_read_tokens,0),
		       num_turns, num_tool_calls, cost_usd
		FROM task_executions
		WHERE workflow_instance_id = ? AND step_id = ?
		ORDER BY created_at DESC LIMIT 1
	`, instanceID, stepID)
	var e Execution
	err := row.Scan(&e.InputTokens, &e.OutputTokens, &e.TotalTokens,
		&e.CacheCreationTokens, &e.CacheReadTokens,
		&e.NumTurns, &e.NumToolCalls, &e.CostUSD)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// GetInstanceStepUsage returns the latest token/cost totals per step for a whole
// workflow instance in a single query — the batched form of GetStepUsage, so
// building an instance's step list doesn't fan out into one query per step (the
// N+1 that made opening a task's workflow view slow). Keyed by step_id; rows are
// scanned in ascending execution order so the most recent execution per step wins.
func (c *Client) GetInstanceStepUsage(ctx context.Context, instanceID string) (map[string]Execution, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT step_id,
		       COALESCE(input_tokens,0), COALESCE(output_tokens,0), COALESCE(total_tokens,0),
		       COALESCE(cache_creation_tokens,0), COALESCE(cache_read_tokens,0),
		       COALESCE(num_turns,0), COALESCE(num_tool_calls,0), COALESCE(cost_usd,0)
		FROM task_executions
		WHERE workflow_instance_id = ?
		ORDER BY created_at ASC, id ASC
	`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]Execution)
	for rows.Next() {
		var stepID sql.NullString
		var e Execution
		if err := rows.Scan(&stepID, &e.InputTokens, &e.OutputTokens, &e.TotalTokens,
			&e.CacheCreationTokens, &e.CacheReadTokens,
			&e.NumTurns, &e.NumToolCalls, &e.CostUSD); err != nil {
			continue
		}
		out[stepID.String] = e // ASC order: the last write per step_id is the most recent
	}
	return out, nil
}

// ReconcileOrphanExecutions marks any executions still in the 'running' state
// as failed. A fresh dispatcher process owns no in-flight runs, so a row left
// 'running' is an orphan from a previous process that was killed mid-run.
// Clearing them keeps the dashboard's agent "active/idle" status truthful —
// "running" then always reflects a real, live claude process. Returns the
// number of rows reconciled.
func (c *Client) ReconcileOrphanExecutions(ctx context.Context) (int64, error) {
	now := time.Now()
	res, err := c.db.ExecContext(ctx, `
		UPDATE task_executions
		SET status = 'failed',
		    completed_at = ?,
		    error_message = CASE
		        WHEN error_message IS NULL OR error_message = '' THEN 'interrupted (dispatcher restarted)'
		        ELSE error_message
		    END
		WHERE status = 'running'
	`, now)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (c *Client) GetLastExecution(ctx context.Context, taskID string) (*Execution, error) {
	row := c.db.QueryRowContext(ctx, `
		SELECT id, task_id, agent_id, attempt, status, started_at, completed_at, duration_ms,
		       error_message, can_retry, next_retry_at, created_at,
		       COALESCE(input_tokens,0), COALESCE(output_tokens,0), COALESCE(total_tokens,0),
		       COALESCE(cache_creation_tokens,0), COALESCE(cache_read_tokens,0),
		       COALESCE(num_turns,0), COALESCE(num_tool_calls,0), COALESCE(cost_usd,0),
		       COALESCE(input_prompt,''), COALESCE(output_text,'')
		FROM task_executions
		WHERE task_id = ?
		ORDER BY attempt DESC
		LIMIT 1
	`, taskID)

	exec := &Execution{}
	err := row.Scan(&exec.ID, &exec.TaskID, &exec.AgentID, &exec.Attempt, &exec.Status,
		&exec.StartedAt, &exec.CompletedAt, &exec.DurationMs, &exec.ErrorMsg, &exec.CanRetry,
		&exec.NextRetryAt, &exec.CreatedAt,
		&exec.InputTokens, &exec.OutputTokens, &exec.TotalTokens,
		&exec.CacheCreationTokens, &exec.CacheReadTokens,
		&exec.NumTurns, &exec.NumToolCalls, &exec.CostUSD,
		&exec.InputPrompt, &exec.OutputText)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return exec, nil
}

func (c *Client) ShouldRetry(ctx context.Context, taskID string) (bool, *time.Time) {
	lastExec, err := c.GetLastExecution(ctx, taskID)
	if err != nil || lastExec == nil {
		return false, nil
	}
	if lastExec.Status != "failed" || !lastExec.CanRetry || lastExec.NextRetryAt == nil {
		return false, nil
	}
	return true, lastExec.NextRetryAt
}

// ClearTaskLogs removes all logs and execution records for a given task.
func (c *Client) ClearTaskLogs(ctx context.Context, taskID string) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM task_logs WHERE task_id = ?`, taskID)
	if err != nil {
		return err
	}
	_, err = c.db.ExecContext(ctx, `DELETE FROM task_executions WHERE task_id = ?`, taskID)
	return err
}

// Logging

func (c *Client) WriteTaskLog(ctx context.Context, taskID, level, message string) error {
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO task_logs (task_id, level, message, timestamp)
		VALUES (?, ?, ?, ?)
	`, taskID, level, message, time.Now())
	return err
}

func (c *Client) WriteServiceLog(ctx context.Context, level, message, component string) error {
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO service_logs (level, message, component, timestamp)
		VALUES (?, ?, ?, ?)
	`, level, message, component, time.Now())
	return err
}

// Agent tracking

func (c *Client) UpsertAgent(ctx context.Context, id, description string) error {
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO agents (id, description, status, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET description = ?, updated_at = ?
	`, id, description, "idle", time.Now(), description, time.Now())
	return err
}

func (c *Client) UpdateAgentStatus(ctx context.Context, agentID, status, currentTaskID string) error {
	_, err := c.db.ExecContext(ctx, `
		UPDATE agents SET status = ?, current_task_id = ?, updated_at = ? WHERE id = ?
	`, status, currentTaskID, time.Now(), agentID)
	return err
}

// Dispatcher state

func (c *Client) UpdateDispatcherState(ctx context.Context, status string, uptimeSecs int64, version string) error {
	// Single row, ID=1
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO dispatcher_state (id, status, uptime_seconds, version, updated_at)
		VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET status = ?, uptime_seconds = ?, version = ?, updated_at = ?
	`, status, uptimeSecs, version, time.Now(),
		status, uptimeSecs, version, time.Now())
	return err
}
