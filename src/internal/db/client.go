package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Client struct {
	db *sql.DB
}

// New opens a SQLite database and initializes schema.
func New(ctx context.Context, dbPath string) (*Client, error) {
	db, err := sql.Open("sqlite3", dbPath)
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
	ID             int64
	TaskID         string
	AgentID        string
	Title          string
	Number         string
	URL            string
	Model          string
	Runner         string
	Attempt        int
	Status         string
	PID            int
	HeartbeatAt    *time.Time
	HeartbeatCount int
	StartedAt      *time.Time
	CompletedAt    *time.Time
	DurationMs     int64
	ErrorMsg       string
	CanRetry       bool
	NextRetryAt    *time.Time
	CreatedAt      time.Time
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
		SET status = ?, completed_at = ?, duration_ms = ?, error_message = ?, can_retry = ?, next_retry_at = ?
		WHERE id = ?
	`, exec.Status, exec.CompletedAt, exec.DurationMs, exec.ErrorMsg, exec.CanRetry, exec.NextRetryAt, exec.ID)
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
		SELECT id, task_id, agent_id, attempt, status, started_at, completed_at, duration_ms, error_message, can_retry, next_retry_at, created_at
		FROM task_executions
		WHERE task_id = ?
		ORDER BY attempt DESC
		LIMIT 1
	`, taskID)

	exec := &Execution{}
	err := row.Scan(&exec.ID, &exec.TaskID, &exec.AgentID, &exec.Attempt, &exec.Status,
		&exec.StartedAt, &exec.CompletedAt, &exec.DurationMs, &exec.ErrorMsg, &exec.CanRetry,
		&exec.NextRetryAt, &exec.CreatedAt)
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
