#!/bin/bash
set -e

SCREENSHOTS_DIR="/tmp/apiary-screenshots"
DB="$SCREENSHOTS_DIR/.apiary/apiary.db"

# Create .apiary directory alongside the config
mkdir -p "$SCREENSHOTS_DIR/.apiary"
# Remove old DB if it exists
rm -f "$DB"

# Initialize schema and seed data
sqlite3 "$DB" <<'EOSQL'
CREATE TABLE IF NOT EXISTS task_executions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  title TEXT,
  task_number TEXT,
  task_url TEXT,
  model TEXT,
  runner TEXT,
  attempt INTEGER DEFAULT 1,
  status TEXT,
  started_at TIMESTAMP,
  completed_at TIMESTAMP,
  duration_ms INTEGER,
  error_message TEXT,
  can_retry BOOLEAN,
  next_retry_at TIMESTAMP,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS task_checkpoints (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT NOT NULL,
  attempt INTEGER,
  stage TEXT,
  metadata TEXT,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY,
  source_id TEXT,
  title TEXT,
  agent_id TEXT,
  state TEXT,
  started_at TIMESTAMP,
  completed_at TIMESTAMP,
  duration_ms INTEGER,
  success BOOLEAN,
  output TEXT,
  full_output TEXT,
  error_message TEXT,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS agents (
  id TEXT PRIMARY KEY,
  description TEXT,
  status TEXT,
  current_task_id TEXT,
  queued_count INTEGER DEFAULT 0,
  total_completed INTEGER DEFAULT 0,
  avg_duration_ms INTEGER DEFAULT 0,
  success_rate REAL DEFAULT 0.0,
  last_task_ended_at TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS task_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT,
  level TEXT,
  message TEXT,
  timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS service_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  level TEXT,
  message TEXT,
  component TEXT,
  timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS dispatcher_state (
  id INTEGER PRIMARY KEY,
  status TEXT,
  uptime_seconds INTEGER,
  version TEXT,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_executions_task ON task_executions(task_id);
CREATE INDEX IF NOT EXISTS idx_executions_retry ON task_executions(next_retry_at) WHERE status='failed';
CREATE INDEX IF NOT EXISTS idx_tasks_agent ON tasks(agent_id);
CREATE INDEX IF NOT EXISTS idx_tasks_state ON tasks(state);
CREATE INDEX IF NOT EXISTS idx_tasks_created ON tasks(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_task_logs_task ON task_logs(task_id);
CREATE INDEX IF NOT EXISTS idx_service_logs_timestamp ON service_logs(timestamp DESC);

-- Dispatcher state
INSERT OR REPLACE INTO dispatcher_state (id, status, uptime_seconds, version, updated_at)
VALUES (1, 'healthy', 84321, '0.5.0', datetime('now', '-5 seconds'));

-- Agents
INSERT OR REPLACE INTO agents (id, description, status, current_task_id, queued_count, total_completed, avg_duration_ms, success_rate, last_task_ended_at, updated_at)
VALUES
  ('fe-specialist', 'Handles UI and frontend tasks', 'active', 'task_003', 1, 47, 32500, 0.91, datetime('now', '-2 minutes'), datetime('now')),
  ('be-specialist', 'Handles API and backend tasks', 'active', 'task_004', 0, 82, 28400, 0.94, datetime('now', '-1 minute'), datetime('now')),
  ('data-analyst', 'Handles data processing and analysis', 'idle', NULL, 0, 31, 45200, 0.87, datetime('now', '-15 minutes'), datetime('now'));

-- Tasks
INSERT OR REPLACE INTO tasks (id, source_id, title, agent_id, state, started_at, completed_at, duration_ms, success, error_message, created_at, updated_at)
VALUES
  ('task_001', 'ERP-128', 'Implement user authentication flow', 'fe-specialist', 'completed', datetime('now', '-3 hours'), datetime('now', '-2 hours'), 3600000, 1, NULL, datetime('now', '-3 hours'), datetime('now', '-2 hours')),
  ('task_002', 'ERP-129', 'Create database migration for orders', 'be-specialist', 'completed', datetime('now', '-2 hours'), datetime('now', '-1 hour'), 2400000, 1, NULL, datetime('now', '-2 hours'), datetime('now', '-1 hour')),
  ('task_003', 'ERP-130', 'Design dashboard layout components', 'fe-specialist', 'running', datetime('now', '-30 minutes'), NULL, NULL, 0, NULL, datetime('now', '-30 minutes'), datetime('now')),
  ('task_004', 'ERP-131', 'Build payment processing API', 'be-specialist', 'running', datetime('now', '-15 minutes'), NULL, NULL, 0, NULL, datetime('now', '-15 minutes'), datetime('now')),
  ('task_005', 'ERP-132', 'Generate monthly sales report', 'data-analyst', 'completed', datetime('now', '-4 hours'), datetime('now', '-3 hours'), 1800000, 1, NULL, datetime('now', '-4 hours'), datetime('now', '-3 hours')),
  ('task_006', 'ERP-133', 'Fix login page responsive layout', 'fe-specialist', 'failed', datetime('now', '-5 hours'), datetime('now', '-4 hours'), 900000, 0, 'CSS grid not rendering correctly on Safari', datetime('now', '-5 hours'), datetime('now', '-4 hours')),
  ('task_007', 'ERP-134', 'Add export to CSV feature', 'data-analyst', 'completed', datetime('now', '-6 hours'), datetime('now', '-5 hours'), 1500000, 1, NULL, datetime('now', '-6 hours'), datetime('now', '-5 hours')),
  ('task_008', 'ERP-135', 'Implement rate limiting middleware', 'be-specialist', 'failed', datetime('now', '-7 hours'), datetime('now', '-5 hours'), 3600000, 0, 'Race condition in token bucket implementation', datetime('now', '-7 hours'), datetime('now', '-5 hours')),
  ('task_009', 'ERP-136', 'Update user profile page', 'fe-specialist', 'completed', datetime('now', '-8 hours'), datetime('now', '-7 hours'), 2400000, 1, NULL, datetime('now', '-8 hours'), datetime('now', '-7 hours')),
  ('task_010', 'ERP-137', 'Set up CI/CD pipeline', 'be-specialist', 'completed', datetime('now', '-10 hours'), datetime('now', '-9 hours'), 4800000, 1, NULL, datetime('now', '-10 hours'), datetime('now', '-9 hours'));

-- Task executions
INSERT OR REPLACE INTO task_executions (task_id, agent_id, title, task_number, task_url, model, runner, attempt, status, started_at, completed_at, duration_ms, error_message, can_retry, next_retry_at, created_at)
VALUES
  ('task_001', 'fe-specialist', 'Implement user authentication flow', 'ERP-128', 'https://plane.so/apiary/issue/ERP-128', 'claude-sonnet-4-6', 'cli', 1, 'success', datetime('now', '-3 hours'), datetime('now', '-2 hours'), 3600000, NULL, 0, NULL, datetime('now', '-3 hours')),
  ('task_002', 'be-specialist', 'Create database migration for orders', 'ERP-129', 'https://plane.so/apiary/issue/ERP-129', 'claude-sonnet-4-6', 'cli', 1, 'success', datetime('now', '-2 hours'), datetime('now', '-1 hour'), 2400000, NULL, 0, NULL, datetime('now', '-2 hours')),
  ('task_003', 'fe-specialist', 'Design dashboard layout components', 'ERP-130', 'https://plane.so/apiary/issue/ERP-130', 'claude-sonnet-4-6', 'cli', 1, 'running', datetime('now', '-30 minutes'), NULL, NULL, NULL, 0, NULL, datetime('now', '-30 minutes')),
  ('task_004', 'be-specialist', 'Build payment processing API', 'ERP-131', 'https://plane.so/apiary/issue/ERP-131', 'claude-4-20250514', 'cli', 1, 'running', datetime('now', '-15 minutes'), NULL, NULL, NULL, 0, NULL, datetime('now', '-15 minutes')),
  ('task_005', 'data-analyst', 'Generate monthly sales report', 'ERP-132', 'https://plane.so/apiary/issue/ERP-132', 'claude-sonnet-4-6', 'cli', 1, 'success', datetime('now', '-4 hours'), datetime('now', '-3 hours'), 1800000, NULL, 0, NULL, datetime('now', '-4 hours')),
  ('task_006', 'fe-specialist', 'Fix login page responsive layout', 'ERP-133', 'https://plane.so/apiary/issue/ERP-133', 'claude-sonnet-4-6', 'cli', 2, 'failed', datetime('now', '-5 hours'), datetime('now', '-4 hours'), 900000, 'CSS grid not rendering correctly on Safari', 0, NULL, datetime('now', '-5 hours')),
  ('task_006', 'fe-specialist', 'Fix login page responsive layout', 'ERP-133', 'https://plane.so/apiary/issue/ERP-133', 'claude-sonnet-4-6', 'cli', 1, 'failed', datetime('now', '-6 hours'), datetime('now', '-5 hours'), 1800000, 'Timeout waiting for browser render', 1, datetime('now', '-5.5 hours'), datetime('now', '-6 hours')),
  ('task_007', 'data-analyst', 'Add export to CSV feature', 'ERP-134', 'https://plane.so/apiary/issue/ERP-134', 'claude-sonnet-4-6', 'cli', 1, 'success', datetime('now', '-6 hours'), datetime('now', '-5 hours'), 1500000, NULL, 0, NULL, datetime('now', '-6 hours')),
  ('task_008', 'be-specialist', 'Implement rate limiting middleware', 'ERP-135', 'https://plane.so/apiary/issue/ERP-135', 'claude-4-20250514', 'cli', 2, 'failed', datetime('now', '-7 hours'), datetime('now', '-5 hours'), 3600000, 'Race condition in token bucket implementation', 1, datetime('now', '-4.5 hours'), datetime('now', '-7 hours')),
  ('task_008', 'be-specialist', 'Implement rate limiting middleware', 'ERP-135', 'https://plane.so/apiary/issue/ERP-135', 'claude-4-20250514', 'cli', 1, 'failed', datetime('now', '-9 hours'), datetime('now', '-8 hours'), 4200000, 'Deadlock detected in concurrent access', 1, datetime('now', '-8.5 hours'), datetime('now', '-9 hours')),
  ('task_009', 'fe-specialist', 'Update user profile page', 'ERP-136', 'https://plane.so/apiary/issue/ERP-136', 'claude-sonnet-4-6', 'cli', 1, 'success', datetime('now', '-8 hours'), datetime('now', '-7 hours'), 2400000, NULL, 0, NULL, datetime('now', '-8 hours')),
  ('task_010', 'be-specialist', 'Set up CI/CD pipeline', 'ERP-137', 'https://plane.so/apiary/issue/ERP-137', 'claude-sonnet-4-6', 'cli', 1, 'success', datetime('now', '-10 hours'), datetime('now', '-9 hours'), 4800000, NULL, 0, NULL, datetime('now', '-10 hours'));

-- Task logs
INSERT OR REPLACE INTO task_logs (task_id, level, message, timestamp)
VALUES
  ('task_003', 'INFO',  'Dispatching task ERP-130 to agent fe-specialist', datetime('now', '-30 minutes')),
  ('task_003', 'DEBUG', 'Routing decision: matched route "frontend" via label match', datetime('now', '-29 minutes')),
  ('task_003', 'DEBUG', 'Selected agent: fe-specialist (Frontend Specialist)', datetime('now', '-29 minutes')),
  ('task_003', 'INFO',  'Agent fe-specialist started processing task', datetime('now', '-28 minutes')),
  ('task_003', 'INFO',  'Prompt sent to claude-sonnet-4-6 (621 tokens)', datetime('now', '-27 minutes')),
  ('task_003', 'DEBUG', '[assistant] I will design the dashboard layout components...', datetime('now', '-26 minutes')),
  ('task_003', 'DEBUG', '[tool→  Artifact] Creating React component: DashboardGrid', datetime('now', '-25 minutes')),
  ('task_003', 'DEBUG', '[tool← result] Component created with responsive grid layout', datetime('now', '-24 minutes')),
  ('task_003', 'DEBUG', '[tool→  Artifact] Creating React component: MetricCard', datetime('now', '-23 minutes')),
  ('task_003', 'DEBUG', '[tool← result] MetricCard component created with loading states', datetime('now', '-22 minutes')),
  ('task_003', 'INFO',  '[result: 12 turns, 18.3s duration, $0.42 cost]', datetime('now', '-21 minutes')),
  ('task_006', 'ERROR', 'Task ERP-133 failed: CSS grid not rendering correctly on Safari', datetime('now', '-4 hours')),
  ('task_006', 'WARN',  'Retry attempt 1/3 scheduled in 30s', datetime('now', '-4 hours')),
  ('task_006', 'ERROR', 'Cross-browser test failure: Safari v17 not supporting subgrid', datetime('now', '-4 hours')),
  ('task_006', 'INFO',  'Fallback to flexbox layout recommended', datetime('now', '-4 hours'));

-- Service logs
INSERT OR REPLACE INTO service_logs (level, message, component, timestamp)
VALUES
  ('INFO',  'Dispatcher started — polling every 10s',                         'dispatcher', datetime('now', '-24 hours')),
  ('INFO',  'Loaded config from apiary.yaml',                                 'dispatcher', datetime('now', '-24 hours')),
  ('INFO',  'Registered 3 agents: fe-specialist, be-specialist, data-analyst','dispatcher', datetime('now', '-24 hours')),
  ('INFO',  'Connected to Plane workspace apiary',                            'source',     datetime('now', '-24 hours')),
  ('INFO',  'Polling Plane for new issues every 30s',                        'source',     datetime('now', '-24 hours')),
  ('INFO',  'Fetched 12 issues from Plane (2 new since last poll)',           'source',     datetime('now', '-12 hours')),
  ('INFO',  'Dispatch cycle started — 2 pending tasks in queue',              'dispatcher', datetime('now', '-10 hours')),
  ('INFO',  'ERP-128 routed to fe-specialist (label: frontend)',              'router',     datetime('now', '-10 hours')),
  ('WARN',  'ERP-133 retry 1/3 — previous attempt failed',                    'dispatcher', datetime('now', '-5 hours')),
  ('INFO',  'ERP-129 completed successfully (2.4s, $0.18)',                   'runner',     datetime('now', '-1 hour')),
  ('ERROR', 'ERP-135 failed: deadlock detected — check task logs',            'runner',     datetime('now', '-5 hours')),
  ('WARN',  'ERP-135 retry 2/3 scheduled',                                    'dispatcher', datetime('now', '-5 hours')),
  ('DEBUG', 'Health check: dispatcher healthy, 2 active runs, 1 queued',      'dispatcher', datetime('now', '-5 minutes')),
  ('INFO',  'ERP-130 status update: 12 turns completed, $0.42 cost',          'runner',     datetime('now', '-1 minute'));
EOSQL

echo "→ Database seeded at $DB"
echo "→ Config at $SCREENSHOTS_DIR/apiary.yaml"
