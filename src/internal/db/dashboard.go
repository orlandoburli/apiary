package db

import (
	"context"
	"database/sql"
	"time"
)

// DashboardStats holds overview statistics.
//
// Two units are reported side by side. A task is the unit a person cares about:
// a task that runs four agent steps is one task, and a task retried twice is
// still one task that eventually finished. A run is one agent process. The
// task figures are the headline; the run figures are the detail that shows how
// much work each task actually took.
type DashboardStats struct {
	DispatcherStatus string
	Uptime           time.Duration
	ActiveAgents     int
	ActiveRuns       int
	QueuedTasks      int

	// Task-level, over the window.
	TasksCompleted    int
	TasksFailed       int
	AvgTaskDurationMs int64

	// Run-level, over the window.
	RunsCompleted    int
	RunsFailed       int
	AvgRunDurationMs int64

	// Rates over the window, per day. Per hour is noise once the window is a
	// week or longer: 0.4 tasks/hour reads worse than 9 tasks/day.
	TasksPerDay float64
	RunsPerDay  float64

	// SuccessRate is task-level; RunSuccessRate is the detail underneath it.
	// Both exclude work that has not finished: counting a running row in the
	// denominator made the rate move whenever anything was in flight.
	SuccessRate    float64
	RunSuccessRate float64

	TodayCostUSD      float64
	TodayTokens       int
	TodayInputTokens  int
	TodayOutputTokens int
}

// GetDashboardStats retrieves the figures for one window, normally local
// midnight to now.
//
// The window start comes from the caller because "today" is a local-day
// question. Deriving it here with time.Now().Truncate(24*time.Hour) truncates
// to UTC midnight, which in any non-UTC zone is the wrong day — it silently
// included the previous evening's work while the page said "since midnight".
func (c *Client) GetDashboardStats(ctx context.Context, startTime time.Time) (*DashboardStats, error) {
	stats := &DashboardStats{
		DispatcherStatus: "Healthy",
	}

	// A zero start means "all time". The queries accept it as a lower bound, but
	// the rate denominator must be the span the hive has actually existed, not
	// two thousand years — so resolve it to the earliest execution.
	if startTime.IsZero() {
		var earliest sql.NullTime
		row := c.db.QueryRowContext(ctx, `SELECT MIN(created_at) FROM task_executions`)
		if err := row.Scan(&earliest); err == nil && earliest.Valid {
			startTime = earliest.Time
		} else {
			startTime = time.Now().Add(-24 * time.Hour)
		}
	}

	// Active runs are a right-now figure, not a windowed one.
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

	// Task-level counts: tasks that reached a terminal state in the window.
	// internal_tasks is the registry every execution hangs off, so it counts a
	// task once however many agent runs or retries it took.
	row = c.db.QueryRowContext(ctx, `
		SELECT
			COUNT(CASE WHEN state = 'done' THEN 1 END),
			COUNT(CASE WHEN state = 'failed' THEN 1 END)
		FROM internal_tasks
		WHERE updated_at >= ? AND state IN ('done','failed')
	`, startTime)
	_ = row.Scan(&stats.TasksCompleted, &stats.TasksFailed)

	// Average task duration over the cohort that both started and finished in
	// the window. A task created days ago and finished today (blocked, waiting
	// on a human) would otherwise report its whole blocked life as duration —
	// seen at 80 hours for one failed task, which says nothing about how long
	// the work takes.
	row = c.db.QueryRowContext(ctx, `
		SELECT CAST(AVG((julianday(updated_at) - julianday(created_at)) * 86400000) AS INTEGER)
		FROM internal_tasks
		WHERE state IN ('done','failed') AND created_at >= ? AND updated_at >= ?
	`, startTime, startTime)
	var avgTask sql.NullInt64
	_ = row.Scan(&avgTask)
	if avgTask.Valid {
		stats.AvgTaskDurationMs = avgTask.Int64
	}

	// Run-level counts over the window. Running rows are left out of the
	// average and the rate on purpose: a run that has not finished has no
	// duration, and counting it in the denominator made the success rate dip
	// whenever work was in flight.
	row = c.db.QueryRowContext(ctx, `
		SELECT
			COUNT(CASE WHEN status = 'success' THEN 1 END),
			COUNT(CASE WHEN status = 'failed' THEN 1 END),
			CAST(AVG(CASE WHEN status IN ('success','failed') THEN duration_ms END) AS INTEGER)
		FROM task_executions
		WHERE created_at >= ?
	`, startTime)
	var avgRun sql.NullInt64
	_ = row.Scan(&stats.RunsCompleted, &stats.RunsFailed, &avgRun)
	if avgRun.Valid {
		stats.AvgRunDurationMs = avgRun.Int64
	}

	// Rates. The denominator is clamped to one hour so a very short window does
	// not turn a single task into "20 tasks/hour".
	days := time.Since(startTime).Hours() / 24
	if days < 1.0/24 {
		days = 1.0 / 24
	}
	stats.TasksPerDay = float64(stats.TasksCompleted) / days
	stats.RunsPerDay = float64(stats.RunsCompleted+stats.RunsFailed) / days

	if finished := stats.TasksCompleted + stats.TasksFailed; finished > 0 {
		stats.SuccessRate = float64(stats.TasksCompleted) / float64(finished)
	}
	if finished := stats.RunsCompleted + stats.RunsFailed; finished > 0 {
		stats.RunSuccessRate = float64(stats.RunsCompleted) / float64(finished)
	}

	// Count active agents (those with recent activity).
	//
	// The cutoff is computed in Go and passed as a parameter rather than
	// written as datetime('now', '-1 hour'). created_at is written by the Go
	// driver as a *local* timestamp with its offset
	// ("2026-11-16 16:01:00.08254-04:00"), while SQLite's datetime('now')
	// returns *UTC* ("2026-11-16 20:01:00"). Comparing the two as strings is
	// comparing wall-clock in two different zones: in any negative-offset
	// zone the stored value always sorts before the UTC cutoff, so the query
	// matched nothing and the Overview reported 0 active agents however many
	// had run. A Go-computed cutoff is serialized in the same format the rows
	// are stored in, so the comparison is exact.
	row = c.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT agent_id) FROM task_executions
		WHERE created_at >= ?
	`, time.Now().Add(-time.Hour))
	_ = row.Scan(&stats.ActiveAgents)

	// Get today's usage totals
	row = c.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(input_tokens),0),
			COALESCE(SUM(output_tokens),0),
			COALESCE(SUM(total_tokens),0),
			COALESCE(SUM(cost_usd),0)
		FROM task_executions
		WHERE created_at >= ?
	`, startTime)
	_ = row.Scan(&stats.TodayInputTokens, &stats.TodayOutputTokens, &stats.TodayTokens, &stats.TodayCostUSD)

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
	// Go-computed cutoff, for the same local-vs-UTC reason as GetDashboardStats.
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, title, agent_id, state, success, duration_ms, error_message
		FROM tasks
		WHERE created_at >= ?
		ORDER BY created_at DESC
		LIMIT ?
	`, time.Now().Add(-24*time.Hour), limit)
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

// RunningExecution is one in-flight agent process.
//
// A row here means a runner process was actually spawned and has not returned
// yet — the execution row is written immediately before the runner is invoked
// and flipped to success/failed when it returns. Unlike the aggregate
// AgentStats, which collapses every execution of an agent into one row, this
// keeps one row per live process, which is what the Running tab lists.
type RunningExecution struct {
	ExecID       int64
	TaskID       string
	TaskNumber   string
	TaskTitle    string
	TaskURL      string
	AgentID      string
	Model        string
	Runner       string
	Attempt      int
	PID          int
	HeartbeatAt  *time.Time
	HeartbeatCnt int
	StartedAt    *time.Time
}

// GetRunningExecutions lists the agent processes that are running right now.
//
// Scoped to status='running' executions, ordered by start time so the
// longest-running process sits at the top of the list.
//
// Every row with status='running' is listed, without narrowing to the latest
// execution per task. A previous version joined to MAX(id) per task to avoid
// showing one row per retry attempt of the same task, but the status filter
// already does that: a retried task's earlier attempts are 'failed' or
// 'success', not 'running'. The join only ever hid rows — specifically a
// still-'running' execution whose task later got another attempt, and two
// agents running the same task at once (a reviewer and a QA on one ticket),
// which are two real processes, not one. Work that is stuck is exactly what
// this list exists to show, so a stale row must not be dropped.
func (c *Client) GetRunningExecutions(ctx context.Context) ([]RunningExecution, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT e.id, e.task_id, e.task_number, e.task_url, e.title, e.agent_id, e.model, e.runner,
		       e.attempt, e.pid, e.heartbeat_at, e.heartbeat_count, e.started_at
		FROM task_executions e
		WHERE e.status = 'running'
		ORDER BY e.started_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RunningExecution
	for rows.Next() {
		var e RunningExecution
		var number, url, title, model, runner sql.NullString
		var pid sql.NullInt64
		var hbStr, startedStr sql.NullString
		var hbCount sql.NullInt64
		if err := rows.Scan(&e.ExecID, &e.TaskID, &number, &url, &title, &e.AgentID, &model, &runner,
			&e.Attempt, &pid, &hbStr, &hbCount, &startedStr); err != nil {
			continue
		}
		e.TaskNumber = number.String
		e.TaskURL = url.String
		e.TaskTitle = title.String
		e.Model = model.String
		e.Runner = runner.String
		if pid.Valid {
			e.PID = int(pid.Int64)
		}
		if hbCount.Valid {
			e.HeartbeatCnt = int(hbCount.Int64)
		}
		if hbStr.Valid {
			if t, ok := parseSQLiteTime(hbStr.String); ok {
				e.HeartbeatAt = &t
			}
		}
		if startedStr.Valid {
			if t, ok := parseSQLiteTime(startedStr.String); ok {
				e.StartedAt = &t
			}
		}
		out = append(out, e)
	}
	return out, nil
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
	TotalCostUSD    float64
	TotalTokens     int
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
			MAX(CASE WHEN e.status = 'running' THEN e.heartbeat_count END) as current_hb_count,
			COALESCE(SUM(e.total_tokens),0),
			COALESCE(SUM(e.cost_usd),0)
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
			&lastEnded, &running, &currentTask, &pid, &hbStr, &hbCount,
			&s.TotalTokens, &s.TotalCostUSD)
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

// parseSQLiteTime parses the timestamp formats SQLite may emit for aggregated
// time columns (which arrive as strings). Returns ok=false if none match.
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
	TaskID       string
	Number       string
	URL          string
	Title        string
	AgentID      string
	Model        string
	Runner       string
	Status       string // running, success, failed
	Attempt      int    // total attempts for this task
	DurationMs   int64
	StartedAt    *time.Time
	CompletedAt  *time.Time
	Error        string
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	NumTurns     int
	NumToolCalls int
	CostUSD      float64
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
		       duration_ms, started_at, completed_at, error_message,
		       COALESCE(input_tokens,0), COALESCE(output_tokens,0), COALESCE(total_tokens,0),
		       COALESCE(num_turns,0), COALESCE(num_tool_calls,0), COALESCE(cost_usd,0)
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
		&dur, &startedStr, &completedStr, &errMsg,
		&it.InputTokens, &it.OutputTokens, &it.TotalTokens,
		&it.NumTurns, &it.NumToolCalls, &it.CostUSD)
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
	ID        int64
	Timestamp time.Time
	Level     string
	Message   string
}

// GetTaskLogs returns the most recent `limit` log lines for a task, in
// chronological order (oldest first).
func (c *Client) GetTaskLogs(ctx context.Context, taskID string, limit int) ([]TaskLogLine, error) {
	return c.taskLogsPage(ctx, taskID, 0, limit)
}

// GetTaskLogsBefore returns up to `limit` log lines older than beforeID (a row
// id cursor), in chronological order. Used to lazily load earlier history when
// the logs view is scrolled to the top, so the first open only pays for a tail.
func (c *Client) GetTaskLogsBefore(ctx context.Context, taskID string, beforeID int64, limit int) ([]TaskLogLine, error) {
	return c.taskLogsPage(ctx, taskID, beforeID, limit)
}

// taskLogsPage returns up to `limit` of a task's log lines with id < beforeID
// (beforeID == 0 means no upper bound — the newest page), ordered oldest-first.
func (c *Client) taskLogsPage(ctx context.Context, taskID string, beforeID int64, limit int) ([]TaskLogLine, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, timestamp, level, message
		FROM task_logs
		WHERE task_id = ? AND (? = 0 OR id < ?)
		ORDER BY id DESC
		LIMIT ?
	`, taskID, beforeID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []TaskLogLine
	for rows.Next() {
		var l TaskLogLine
		var level, msg sql.NullString
		if err := rows.Scan(&l.ID, &l.Timestamp, &level, &msg); err != nil {
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

// GetTaskLogsAfter returns up to `limit` log lines newer than afterID (a row id
// cursor), in chronological order (oldest first). Used to live-tail the logs view
// while a task is running: pass the newest loaded row id to pull only the lines
// appended since.
func (c *Client) GetTaskLogsAfter(ctx context.Context, taskID string, afterID int64, limit int) ([]TaskLogLine, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, timestamp, level, message
		FROM task_logs
		WHERE task_id = ? AND id > ?
		ORDER BY id ASC
		LIMIT ?
	`, taskID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []TaskLogLine
	for rows.Next() {
		var l TaskLogLine
		var level, msg sql.NullString
		if err := rows.Scan(&l.ID, &l.Timestamp, &level, &msg); err != nil {
			continue
		}
		l.Level = level.String
		l.Message = msg.String
		logs = append(logs, l)
	}
	return logs, nil
}

// GetTaskLogsInRange returns log lines for a task within a time window, used to
// isolate logs belonging to a specific workflow step run.
func (c *Client) GetTaskLogsInRange(ctx context.Context, taskID string, from, to *time.Time) ([]TaskLogLine, error) {
	q := `SELECT timestamp, level, message FROM task_logs WHERE task_id = ?`
	args := []any{taskID}
	if from != nil {
		q += " AND timestamp >= ?"
		args = append(args, from)
	}
	if to != nil {
		q += " AND timestamp <= ?"
		args = append(args, to)
	}
	q += " ORDER BY id ASC LIMIT 2000"

	rows, err := c.db.QueryContext(ctx, q, args...)
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

// DailyUsage holds aggregated usage for a single day.
type DailyUsage struct {
	Date         string // "Jan 02"
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	CostUSD      float64
}

// GetDailyUsage returns per-day usage aggregates for the last N days.
func (c *Client) GetDailyUsage(ctx context.Context, days int) ([]DailyUsage, error) {
	// Same reasoning as the ActiveAgents cutoff above: the window is computed
	// in Go so it is serialized in the same local+offset format the rows are
	// stored in. A SQLite-computed UTC cutoff would drop the most recent
	// hours of every day in a negative-offset zone.
	cutoff := time.Now().AddDate(0, 0, -days)
	rows, err := c.db.QueryContext(ctx, `
		SELECT
			DATE(created_at) as day,
			COALESCE(SUM(input_tokens),0),
			COALESCE(SUM(output_tokens),0),
			COALESCE(SUM(total_tokens),0),
			COALESCE(SUM(cost_usd),0)
		FROM task_executions
		WHERE created_at >= ?
		GROUP BY DATE(created_at)
		ORDER BY day ASC
	`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []DailyUsage
	for rows.Next() {
		var d DailyUsage
		var dateStr string
		if err := rows.Scan(&dateStr, &d.InputTokens, &d.OutputTokens, &d.TotalTokens, &d.CostUSD); err != nil {
			continue
		}
		if t, err := time.Parse("2006-01-02", dateStr); err == nil {
			d.Date = t.Format("Jan 02")
		} else {
			d.Date = dateStr
		}
		items = append(items, d)
	}
	return items, nil
}

// AgentUsage holds per-agent usage aggregates.
type AgentUsage struct {
	AgentID     string
	TotalTokens int
	CostUSD     float64
	RunCount    int
}

// GetAgentUsage returns per-agent usage totals.
func (c *Client) GetAgentUsage(ctx context.Context) ([]AgentUsage, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT
			agent_id,
			COUNT(*) as runs,
			COALESCE(SUM(total_tokens),0),
			COALESCE(SUM(cost_usd),0)
		FROM task_executions
		GROUP BY agent_id
		ORDER BY SUM(cost_usd) DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []AgentUsage
	for rows.Next() {
		var a AgentUsage
		if err := rows.Scan(&a.AgentID, &a.RunCount, &a.TotalTokens, &a.CostUSD); err != nil {
			continue
		}
		items = append(items, a)
	}
	return items, nil
}
