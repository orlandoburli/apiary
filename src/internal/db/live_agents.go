package db

import (
	"context"
	"database/sql"
	"time"
)

// LiveAgentCounts returns, per agent, how many agent processes are actually
// running right now.
//
// This is the number the daemon's status reports as "agents active", and it is
// deliberately not the same as the number of dispatch slots the agent's
// semaphore holds. A workflow instance takes one slot for its whole run, but
// the run may fan out to several agent processes at once — a reviewer and a QA
// on one ticket, three engineers on one board — so the slot count understates
// the work in flight, sometimes by a lot.
//
// The liveness rule is the same one the dashboard's Running tab uses
// (ExecutionIsAlive), so the two can never disagree: a process counts as
// running only while its heartbeat is fresh.
func (c *Client) LiveAgentCounts(ctx context.Context) (map[string]int, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT agent_id, pid, heartbeat_at, started_at
		FROM task_executions
		WHERE status = 'running'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now()
	counts := map[string]int{}
	for rows.Next() {
		var agentID string
		var pid sql.NullInt64
		var hbStr, startedStr sql.NullString
		if err := rows.Scan(&agentID, &pid, &hbStr, &startedStr); err != nil {
			continue
		}
		var heartbeat, started *time.Time
		if hbStr.Valid {
			if t, ok := parseSQLiteTime(hbStr.String); ok {
				heartbeat = &t
			}
		}
		if startedStr.Valid {
			if t, ok := parseSQLiteTime(startedStr.String); ok {
				started = &t
			}
		}
		p := 0
		if pid.Valid {
			p = int(pid.Int64)
		}
		if ExecutionIsAlive(p, heartbeat, started, now) {
			counts[agentID]++
		}
	}
	return counts, rows.Err()
}
