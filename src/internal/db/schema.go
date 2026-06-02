package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const schema = `
-- Task execution attempts (for crash recovery)
CREATE TABLE IF NOT EXISTS task_executions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  title TEXT,                     -- task title at dispatch time
  task_number TEXT,               -- human reference, e.g. "ERP-42"
  task_url TEXT,                  -- link to the task in its source UI
  model TEXT,                     -- LLM model used for this attempt
  runner TEXT,                    -- runner type (cli, script, …)
  attempt INTEGER DEFAULT 1,
  status TEXT,                    -- pending, running, success, failed
  started_at TIMESTAMP,
  completed_at TIMESTAMP,
  duration_ms INTEGER,
  error_message TEXT,
  can_retry BOOLEAN,
  next_retry_at TIMESTAMP,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(task_id) REFERENCES tasks(id),
  FOREIGN KEY(agent_id) REFERENCES agents(id)
);

-- Checkpoint system for recovery
CREATE TABLE IF NOT EXISTS task_checkpoints (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT NOT NULL,
  attempt INTEGER,
  stage TEXT,                     -- initialized, running, completed
  metadata TEXT,                  -- JSON state data
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(task_id) REFERENCES tasks(id)
);

-- Task history (for dashboard)
CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY,
  source_id TEXT,
  title TEXT,
  agent_id TEXT,
  state TEXT,                     -- pending, running, completed, failed
  started_at TIMESTAMP,
  completed_at TIMESTAMP,
  duration_ms INTEGER,
  success BOOLEAN,
  output TEXT,
  full_output TEXT,
  error_message TEXT,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(agent_id) REFERENCES agents(id)
);

-- Agent tracking (for dashboard)
CREATE TABLE IF NOT EXISTS agents (
  id TEXT PRIMARY KEY,
  description TEXT,
  status TEXT,                    -- active, idle, error
  current_task_id TEXT,
  queued_count INTEGER DEFAULT 0,
  total_completed INTEGER DEFAULT 0,
  avg_duration_ms INTEGER DEFAULT 0,
  success_rate REAL DEFAULT 0.0,
  last_task_ended_at TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Task logs
CREATE TABLE IF NOT EXISTS task_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT,
  level TEXT,                     -- DEBUG, INFO, WARN, ERROR
  message TEXT,
  timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(task_id) REFERENCES tasks(id)
);

-- Service logs
CREATE TABLE IF NOT EXISTS service_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  level TEXT,                     -- DEBUG, INFO, WARN, ERROR
  message TEXT,
  component TEXT,                 -- dispatcher, router, runner, etc.
  timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Dispatcher state
CREATE TABLE IF NOT EXISTS dispatcher_state (
  id INTEGER PRIMARY KEY,
  status TEXT,                    -- healthy, degraded, error
  uptime_seconds INTEGER,
  version TEXT,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Create indices
CREATE INDEX IF NOT EXISTS idx_executions_task ON task_executions(task_id);
CREATE INDEX IF NOT EXISTS idx_executions_retry ON task_executions(next_retry_at) WHERE status='failed';
CREATE INDEX IF NOT EXISTS idx_tasks_agent ON tasks(agent_id);
CREATE INDEX IF NOT EXISTS idx_tasks_state ON tasks(state);
CREATE INDEX IF NOT EXISTS idx_tasks_created ON tasks(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_task_logs_task ON task_logs(task_id);
CREATE INDEX IF NOT EXISTS idx_service_logs_timestamp ON service_logs(timestamp DESC);
`

// migrations are idempotent ALTER statements applied to databases created
// before a column existed. CREATE TABLE IF NOT EXISTS never alters an existing
// table, so new columns must be added here. A "duplicate column name" error
// just means the migration already ran and is ignored.
var migrations = []string{
	`ALTER TABLE task_executions ADD COLUMN title TEXT`,
	`ALTER TABLE task_executions ADD COLUMN model TEXT`,
	`ALTER TABLE task_executions ADD COLUMN runner TEXT`,
	`ALTER TABLE task_executions ADD COLUMN task_number TEXT`,
	`ALTER TABLE task_executions ADD COLUMN task_url TEXT`,
}

// InitSchema creates all tables and indices. Safe to call multiple times (uses IF NOT EXISTS).
func InitSchema(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	for _, m := range migrations {
		if _, err := db.ExecContext(ctx, m); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return fmt.Errorf("migrate (%s): %w", m, err)
		}
	}
	return nil
}
