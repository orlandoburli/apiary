package db

import (
	"context"
	"database/sql"
	"time"
)

// DashboardStats holds overview statistics.
type DashboardStats struct {
	DispatcherStatus string
	Uptime           time.Duration
	ActiveAgents     int
	ActiveRuns       int
	QueuedTasks      int
	CompletedToday   int
	FailedToday      int
	AvgDurationMs    int64
	SuccessRate      float64
}

// GetDashboardStats retrieves overall statistics.
func (c *Client) GetDashboardStats(ctx context.Context, startTime time.Time) (*DashboardStats, error) {
	stats := &DashboardStats{
		DispatcherStatus: "Healthy",
	}

	// Get active runs
	row := c.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM task_executions WHERE status = 'running'
	`)
	_ = row.Scan(&stats.ActiveRuns)

	// Get queued tasks
	row = c.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM task_executions
		WHERE status = 'failed' AND can_retry = true AND next_retry_at > ?
	`, time.Now())
	_ = row.Scan(&stats.QueuedTasks)

	// Get today's statistics
	todayStart := time.Now().Truncate(24 * time.Hour)
	row = c.db.QueryRowContext(ctx, `
		SELECT
			COUNT(CASE WHEN status = 'success' THEN 1 END),
			COUNT(CASE WHEN status = 'failed' THEN 1 END),
			CAST(AVG(duration_ms) AS INTEGER),
			CAST(COUNT(CASE WHEN status = 'success' THEN 1 END) AS FLOAT) /
				NULLIF(COUNT(*), 0)
		FROM task_executions
		WHERE created_at >= ?
	`, todayStart)
	var avgMs sql.NullInt64
	var successRate sql.NullFloat64
	err := row.Scan(&stats.CompletedToday, &stats.FailedToday, &avgMs, &successRate)
	if err == nil {
		if avgMs.Valid {
			stats.AvgDurationMs = avgMs.Int64
		}
		if successRate.Valid {
			stats.SuccessRate = successRate.Float64
		}
	}

	// Count active agents (those with recent activity)
	row = c.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT agent_id) FROM task_executions
		WHERE created_at >= datetime('now', '-1 hour')
	`)
	_ = row.Scan(&stats.ActiveAgents)

	return stats, nil
}

// RecentTask holds task summary information.
type RecentTask struct {
	ID       string
	Title    string
	AgentID  string
	Status   string
	Success  bool
	Duration int64
	Error    string
}

// GetRecentTasks retrieves recently executed tasks.
func (c *Client) GetRecentTasks(ctx context.Context, limit int) ([]RecentTask, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, title, agent_id, state, success, duration_ms, error_message
		FROM tasks
		WHERE created_at >= datetime('now', '-24 hours')
		ORDER BY created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []RecentTask
	for rows.Next() {
		var task RecentTask
		var success sql.NullBool
		var errMsg sql.NullString
		err := rows.Scan(&task.ID, &task.Title, &task.AgentID, &task.Status, &success, &task.Duration, &errMsg)
		if err != nil {
			continue
		}
		if success.Valid {
			task.Success = success.Bool
		}
		if errMsg.Valid {
			task.Error = errMsg.String
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

// AgentStats holds agent performance and health information.
type AgentStats struct {
	ID              string
	Status          string // active, stale, zombie, idle
	RunningCount    int
	CurrentTask     string
	QueuedCount     int
	CompletedCount  int
	AvgDurationMs   int64
	SuccessRate     float64
	LastTaskEndedAt *time.Time
	PID             int
	HeartbeatAt     *time.Time
	HeartbeatCount  int
}

// GetAgentStats retrieves statistics for all agents. An agent is "active" when
// it has at least one execution still in the 'running' state — that row is
// written immediately before the claude runner is invoked and flipped to
// success/failed when it returns, so it mirrors a real in-flight process rather
// than a guess.
func (c *Client) GetAgentStats(ctx context.Context) ([]AgentStats, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT
			e.agent_id,
			COUNT(*) as total,
			COUNT(CASE WHEN e.status = 'success' THEN 1 END) as completed,
			CAST(AVG(e.duration_ms) AS INTEGER),
			CAST(COUNT(CASE WHEN e.status = 'success' THEN 1 END) AS FLOAT) /
				NULLIF(COUNT(*), 0) as success_rate,
			MAX(e.completed_at),
			COUNT(CASE WHEN e.status = 'running' THEN 1 END) as running,
			MAX(CASE WHEN e.status = 'running' THEN e.title END) as current_task,
			MAX(CASE WHEN e.status = 'running' THEN e.pid END) as current_pid,
			MAX(CASE WHEN e.status = 'running' THEN e.heartbeat_at END) as current_heartbeat,
			MAX(CASE WHEN e.status = 'running' THEN e.heartbeat_count END) as current_hb_count
		FROM task_executions e
		GROUP BY e.agent_id
		ORDER BY e.agent_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now()
	var stats []AgentStats
	for rows.Next() {
		var s AgentStats
		var avgMs sql.NullInt64
		var successRate sql.NullFloat64
		var running sql.NullInt64
		var currentTask sql.NullString
		var pid sql.NullInt64
		var hbStr sql.NullString
		var hbCount sql.NullInt64
		var lastEnded sql.NullString
		err := rows.Scan(&s.ID, &s.QueuedCount, &s.CompletedCount, &avgMs, &successRate,
			&lastEnded, &running, &currentTask, &pid, &hbStr, &hbCount)
		if err != nil {
			continue
		}
		if avgMs.Valid {
			s.AvgDurationMs = avgMs.Int64
		}
		if successRate.Valid {
			s.SuccessRate = successRate.Float64
		}
		if lastEnded.Valid {
			if t, ok := parseSQLiteTime(lastEnded.String); ok {
				s.LastTaskEndedAt = &t
			}
		}
		if running.Valid {
			s.RunningCount = int(running.Int64)
		}
		if pid.Valid {
			s.PID = int(pid.Int64)
		}
		if hbStr.Valid {
			if t, ok := parseSQLiteTime(hbStr.String); ok {
				s.HeartbeatAt = &t
			}
		}
		if hbCount.Valid {
			s.HeartbeatCount = int(hbCount.Int64)
		}

		if s.RunningCount > 0 {
			s.CurrentTask = currentTask.String
			if s.PID > 0 && s.HeartbeatAt != nil && now.Sub(*s.HeartbeatAt) <= 60*time.Second {
				s.Status = "active" // 🟢
			} else if s.PID > 0 && s.HeartbeatAt != nil && now.Sub(*s.HeartbeatAt) > 60*time.Second {
				s.Status = "stale" // 🟡
			} else if s.PID > 0 && s.HeartbeatAt == nil {
				s.Status = "stale" // 🟡 — started, never heartbeated
			} else {
				s.Status = "zombie" // 🔴 — running in DB but no PID tracked
			}
		} else {
			s.Status = "idle"
		}
		stats = append(stats, s)
	}
	return stats, nil
}

// parseSQLiteTime parses the timestamp formats SQLite/go-sqlite3 may emit for
// aggregated time columns (which arrive as strings). Returns ok=false if none match.
func parseSQLiteTime(s string) (time.Time, bool) {
	layouts := []string{
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		time.RFC3339Nano,
		time.RFC3339,
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// TaskHistoryItem is one task in the Tasks-tab list: the most recent execution
// attempt for a given task_id, plus how many attempts it took.
type TaskHistoryItem struct {
	TaskID      string
	Number      string
	URL         string
	Title       string
	AgentID     string
	Model       string
	Runner      string
	Status      string // running, success, failed
	Attempt     int    // total attempts for this task
	DurationMs  int64
	StartedAt   *time.Time
	CompletedAt *time.Time
	Error       string
}

// GetTaskHistory returns recent tasks (running and finished), newest first,
// one row per task_id (its latest attempt).
func (c *Client) GetTaskHistory(ctx context.Context, limit int) ([]TaskHistoryItem, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT e.task_id, e.task_number, e.task_url, e.title, e.agent_id, e.model, e.runner,
		       e.status, e.attempt, e.duration_ms, e.started_at, e.completed_at, e.error_message
		FROM task_executions e
		JOIN (
			SELECT task_id, MAX(id) AS max_id
			FROM task_executions
			GROUP BY task_id
		) latest ON e.id = latest.max_id
		ORDER BY e.created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []TaskHistoryItem
	for rows.Next() {
		var it TaskHistoryItem
		var number, taskURL, title, model, runner, status, errMsg sql.NullString
		var dur sql.NullInt64
		var startedStr, completedStr sql.NullString
		if err := rows.Scan(&it.TaskID, &number, &taskURL, &title, &it.AgentID, &model, &runner,
			&status, &it.Attempt, &dur, &startedStr, &completedStr, &errMsg); err != nil {
			continue
		}
		it.Number = number.String
		it.URL = taskURL.String
		it.Title = title.String
		it.Model = model.String
		it.Runner = runner.String
		it.Status = status.String
		it.Error = errMsg.String
		if dur.Valid {
			it.DurationMs = dur.Int64
		}
		if startedStr.Valid {
			if t, ok := parseSQLiteTime(startedStr.String); ok {
				it.StartedAt = &t
			}
		}
		if completedStr.Valid {
			if t, ok := parseSQLiteTime(completedStr.String); ok {
				it.CompletedAt = &t
			}
		}
		items = append(items, it)
	}
	return items, nil
}

// GetTasksByAgent returns recent tasks handled by a given agent (latest attempt
// per task), newest first. Powers the Agents tab activity view.
func (c *Client) GetTasksByAgent(ctx context.Context, agentID string, limit int) ([]TaskHistoryItem, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT e.task_id, e.task_number, e.task_url, e.title, e.agent_id, e.model, e.runner,
		       e.status, e.attempt, e.duration_ms, e.started_at, e.completed_at, e.error_message
		FROM task_executions e
		JOIN (
			SELECT task_id, MAX(id) AS max_id
			FROM task_executions
			GROUP BY task_id
		) latest ON e.id = latest.max_id
		WHERE e.agent_id = ?
		ORDER BY e.created_at DESC
		LIMIT ?
	`, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []TaskHistoryItem
	for rows.Next() {
		var it TaskHistoryItem
		var number, taskURL, title, model, runner, status, errMsg sql.NullString
		var dur sql.NullInt64
		var startedStr, completedStr sql.NullString
		if err := rows.Scan(&it.TaskID, &number, &taskURL, &title, &it.AgentID, &model, &runner,
			&status, &it.Attempt, &dur, &startedStr, &completedStr, &errMsg); err != nil {
			continue
		}
		it.Number = number.String
		it.URL = taskURL.String
		it.Title = title.String
		it.Model = model.String
		it.Runner = runner.String
		it.Status = status.String
		it.Error = errMsg.String
		if dur.Valid {
			it.DurationMs = dur.Int64
		}
		if startedStr.Valid {
			if t, ok := parseSQLiteTime(startedStr.String); ok {
				it.StartedAt = &t
			}
		}
		if completedStr.Valid {
			if t, ok := parseSQLiteTime(completedStr.String); ok {
				it.CompletedAt = &t
			}
		}
		items = append(items, it)
	}
	return items, nil
}

// GetTaskDetail returns the latest execution for a task with full metadata.
func (c *Client) GetTaskDetail(ctx context.Context, taskID string) (*TaskHistoryItem, error) {
	row := c.db.QueryRowContext(ctx, `
		SELECT task_id, task_number, task_url, title, agent_id, model, runner, status, attempt,
		       duration_ms, started_at, completed_at, error_message
		FROM task_executions
		WHERE task_id = ?
		ORDER BY id DESC
		LIMIT 1
	`, taskID)

	var it TaskHistoryItem
	var number, taskURL, title, model, runner, status, errMsg sql.NullString
	var dur sql.NullInt64
	var startedStr, completedStr sql.NullString
	err := row.Scan(&it.TaskID, &number, &taskURL, &title, &it.AgentID, &model, &runner, &status, &it.Attempt,
		&dur, &startedStr, &completedStr, &errMsg)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	it.Number = number.String
	it.URL = taskURL.String
	it.Title = title.String
	it.Model = model.String
	it.Runner = runner.String
	it.Status = status.String
	it.Error = errMsg.String
	if dur.Valid {
		it.DurationMs = dur.Int64
	}
	if startedStr.Valid {
		if t, ok := parseSQLiteTime(startedStr.String); ok {
			it.StartedAt = &t
		}
	}
	if completedStr.Valid {
		if t, ok := parseSQLiteTime(completedStr.String); ok {
			it.CompletedAt = &t
		}
	}
	return &it, nil
}

// TaskLogLine is a single per-task log record.
type TaskLogLine struct {
	Timestamp time.Time
	Level     string
	Message   string
}

// GetTaskLogs returns per-task log lines in chronological order.
func (c *Client) GetTaskLogs(ctx context.Context, taskID string, limit int) ([]TaskLogLine, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT timestamp, level, message
		FROM task_logs
		WHERE task_id = ?
		ORDER BY id DESC
		LIMIT ?
	`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []TaskLogLine
	for rows.Next() {
		var l TaskLogLine
		var level, msg sql.NullString
		if err := rows.Scan(&l.Timestamp, &level, &msg); err != nil {
			continue
		}
		l.Level = level.String
		l.Message = msg.String
		logs = append(logs, l)
	}
	// Reverse into chronological order (oldest first).
	for i, j := 0, len(logs)-1; i < j; i, j = i+1, j-1 {
		logs[i], logs[j] = logs[j], logs[i]
	}
	return logs, nil
}

// LogEntry holds a log record.
type ServiceLog struct {
	Timestamp time.Time
	Level     string
	Component string
	Message   string
}

// GetRecentLogs retrieves recent service logs.
func (c *Client) GetRecentLogs(ctx context.Context, limit int) ([]ServiceLog, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT timestamp, level, component, message
		FROM service_logs
		ORDER BY timestamp DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []ServiceLog
	for rows.Next() {
		var log ServiceLog
		err := rows.Scan(&log.Timestamp, &log.Level, &log.Component, &log.Message)
		if err != nil {
			continue
		}
		logs = append(logs, log)
	}

	// Reverse to get chronological order
	for i, j := 0, len(logs)-1; i < j; i, j = i+1, j-1 {
		logs[i], logs[j] = logs[j], logs[i]
	}

	return logs, nil
}

// GetActiveRuns retrieves currently running tasks.
func (c *Client) GetActiveRuns(ctx context.Context) ([]struct {
	CellID   string
	Title    string
	AgentID  string
	Duration int64
}, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, title, agent_id,
			CAST((julianday('now') - julianday(started_at)) * 86400000 AS INTEGER) as duration_ms
		FROM tasks
		WHERE state = 'running'
		ORDER BY started_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []struct {
		CellID   string
		Title    string
		AgentID  string
		Duration int64
	}
	for rows.Next() {
		var run struct {
			CellID   string
			Title    string
			AgentID  string
			Duration int64
		}
		err := rows.Scan(&run.CellID, &run.Title, &run.AgentID, &run.Duration)
		if err != nil {
			continue
		}
		runs = append(runs, run)
	}
	return runs, nil
}
