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
			AVG(duration_ms),
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

// AgentStats holds agent performance information.
type AgentStats struct {
	ID              string
	Status          string
	QueuedCount     int
	CompletedCount  int
	AvgDurationMs   int64
	SuccessRate     float64
	LastTaskEndedAt *time.Time
}

// GetAgentStats retrieves statistics for all agents.
func (c *Client) GetAgentStats(ctx context.Context) ([]AgentStats, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT
			agent_id,
			COUNT(*) as total,
			COUNT(CASE WHEN status = 'success' THEN 1 END) as completed,
			AVG(duration_ms),
			CAST(COUNT(CASE WHEN status = 'success' THEN 1 END) AS FLOAT) /
				NULLIF(COUNT(*), 0) as success_rate,
			MAX(completed_at)
		FROM task_executions
		GROUP BY agent_id
		ORDER BY agent_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []AgentStats
	for rows.Next() {
		var s AgentStats
		var avgMs sql.NullInt64
		var successRate sql.NullFloat64
		var lastEnded sql.NullTime
		err := rows.Scan(&s.ID, &s.QueuedCount, &s.CompletedCount, &avgMs, &successRate, &lastEnded)
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
			s.LastTaskEndedAt = &lastEnded.Time
		}
		s.Status = "idle" // Would need active task tracking for "active"
		stats = append(stats, s)
	}
	return stats, nil
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
