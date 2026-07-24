package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
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

-- Versioned structured lifecycle events. Metadata is redacted before insert;
-- the same stored envelope powers queries, SSE subscribers, CLI, and dashboard.
CREATE TABLE IF NOT EXISTS execution_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  schema_version INTEGER NOT NULL,
  type TEXT NOT NULL,
  timestamp TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  task_id TEXT,
  workflow_id TEXT,
  workflow_instance_id TEXT,
  step_id TEXT,
  attempt_id TEXT,
  metadata TEXT NOT NULL DEFAULT '{}'
);

-- Dispatcher state
CREATE TABLE IF NOT EXISTS dispatcher_state (
  id INTEGER PRIMARY KEY,
  status TEXT,                    -- healthy, degraded, error
  uptime_seconds INTEGER,
  version TEXT,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Workflow instances: one execution of a workflow bound to a Cell.
CREATE TABLE IF NOT EXISTS workflow_instances (
  id TEXT PRIMARY KEY,
  workflow_id TEXT NOT NULL,
  cell_id TEXT NOT NULL,
  source_id TEXT,
  state TEXT NOT NULL,            -- pending|running|approval_waiting|interrupted|done|failed
  parent_instance_id TEXT,       -- set for sub-workflow child instances
  resumed_from TEXT,             -- instance id this was resumed from
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Immutable workflow definition used by an instance. Kept separately from
-- workflow_instances so the hot instance-list scan remains narrow.
CREATE TABLE IF NOT EXISTS workflow_instance_snapshots (
  instance_id TEXT PRIMARY KEY,
  workflow_json TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(instance_id) REFERENCES workflow_instances(id) ON DELETE CASCADE
);

-- Step runs: one row per step execution within a workflow instance.
CREATE TABLE IF NOT EXISTS step_runs (
  id TEXT PRIMARY KEY,
  workflow_instance_id TEXT NOT NULL,
  step_id TEXT NOT NULL,
  agent_id TEXT,
  state TEXT NOT NULL,           -- pending|running|passed|failed|skipped|skipped_cached
  output TEXT,
  structured_output TEXT,        -- JSON-encoded structured output
  summary TEXT,
  exit_code INTEGER,
  skipped_cached BOOLEAN DEFAULT 0,
  started_at TIMESTAMP,
  finished_at TIMESTAMP,
  FOREIGN KEY(workflow_instance_id) REFERENCES workflow_instances(id)
);

-- CI poll checks: one row per poll of a wait_for step's external status (CI).
-- A parked CI wait re-polls the PR every cycle; recording each poll makes the
-- wait auditable — how many times it checked, when, and what each poll returned
-- (passed|failed|pending|timeout|error) — instead of an opaque "waiting".
CREATE TABLE IF NOT EXISTS ci_poll_checks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  workflow_instance_id TEXT NOT NULL,
  step_id TEXT NOT NULL,
  status TEXT NOT NULL,           -- passed|failed|pending|timeout|error|unknown
  pr_url TEXT,
  detail TEXT,                    -- JSON of per-check states, or an error message
  checked_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(workflow_instance_id) REFERENCES workflow_instances(id)
);

-- Canonical internal task registry: the source-independent unit of work.
CREATE TABLE IF NOT EXISTS internal_tasks (
  id TEXT PRIMARY KEY,                          -- ulid
  parent_task_id TEXT,                          -- set for spawned tasks (lineage)
  title TEXT NOT NULL,
  description TEXT,
  input TEXT,                                   -- JSON: structured input from spawner
  state TEXT NOT NULL DEFAULT 'registered',     -- registered|running|approval_waiting|done|failed
  metadata TEXT,                                -- JSON: labels, priority, type, etc.
  outstanding_workflows INTEGER DEFAULT 0,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(parent_task_id) REFERENCES internal_tasks(id)
);

-- Links source items to InternalTasks (one-to-many, optional).
CREATE TABLE IF NOT EXISTS source_bindings (
  id TEXT PRIMARY KEY,                          -- ulid
  task_id TEXT NOT NULL,
  source_id TEXT NOT NULL,                      -- e.g. "github", "plane"
  source_item_id TEXT NOT NULL,                 -- source-native item ID
  source_item_url TEXT,                         -- deep-link for display
  source_item_number TEXT,                      -- human ref: "#42", "ERP-42"
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(task_id) REFERENCES internal_tasks(id),
  UNIQUE(source_id, source_item_id)
);

-- Pull requests linked to an InternalTask, discovered from the source (e.g. a
-- GitHub issue's cross-referenced PRs) when a task's detail is opened. Persisted
-- so the dashboard can offer "open the latest PR" from the list, not just detail.
-- seq is the source-order position; MAX(seq) is the most recent PR.
CREATE TABLE IF NOT EXISTS task_pull_requests (
  id TEXT PRIMARY KEY,                          -- ulid
  task_id TEXT NOT NULL,
  source_id TEXT NOT NULL,                      -- e.g. "github"
  pr_number INTEGER NOT NULL,
  pr_url TEXT NOT NULL,                         -- browser deep-link
  pr_state TEXT,                                -- open|closed|merged, nullable
  seq INTEGER NOT NULL DEFAULT 0,               -- source order; MAX(seq) = most recent
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(task_id) REFERENCES internal_tasks(id),
  UNIQUE(task_id, source_id, pr_number)
);

-- Create indices
CREATE INDEX IF NOT EXISTS idx_executions_task ON task_executions(task_id);
CREATE INDEX IF NOT EXISTS idx_executions_retry ON task_executions(next_retry_at) WHERE status='failed';
CREATE INDEX IF NOT EXISTS idx_tasks_agent ON tasks(agent_id);
CREATE INDEX IF NOT EXISTS idx_tasks_state ON tasks(state);
CREATE INDEX IF NOT EXISTS idx_tasks_created ON tasks(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_task_logs_task ON task_logs(task_id);
CREATE INDEX IF NOT EXISTS idx_task_logs_timestamp ON task_logs(timestamp);
CREATE INDEX IF NOT EXISTS idx_service_logs_timestamp ON service_logs(timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_execution_events_task ON execution_events(task_id, id);
CREATE INDEX IF NOT EXISTS idx_execution_events_instance ON execution_events(workflow_instance_id, id);
CREATE INDEX IF NOT EXISTS idx_execution_events_type ON execution_events(type, id);

-- Durable dispatch queue. A job holds one immutable, versioned dispatch
-- snapshot and at most one active lease. The lease attempt/token pair is the
-- compare-and-set fence that prevents a reclaimed worker from completing stale
-- work after another worker has claimed it.
CREATE TABLE IF NOT EXISTS dispatch_jobs (
  id TEXT PRIMARY KEY,
  idempotency_key TEXT NOT NULL UNIQUE,
  project_id TEXT, source_id TEXT, task_id TEXT, workflow_id TEXT,
  agent_id TEXT, runner_id TEXT, pool TEXT,
  required_labels TEXT NOT NULL DEFAULT '[]',
  required_capabilities TEXT NOT NULL DEFAULT '[]',
  affinity_key TEXT, affinity_worker_id TEXT,
  payload_version INTEGER NOT NULL,
  payload TEXT NOT NULL,
  priority INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL DEFAULT 'queued',
  attempt_count INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 3,
  cancel_requested INTEGER NOT NULL DEFAULT 0,
  available_at TIMESTAMP NOT NULL,
  lease_attempt_id TEXT, lease_token TEXT, lease_worker_id TEXT,
  lease_expires_at TIMESTAMP,
  terminal_at TIMESTAMP,
  created_at TIMESTAMP NOT NULL,
  updated_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS dispatch_attempts (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL,
  attempt_number INTEGER NOT NULL,
  worker_id TEXT NOT NULL,
  claim_token TEXT NOT NULL,
  state TEXT NOT NULL,
  lease_expires_at TIMESTAMP NOT NULL,
  heartbeat_at TIMESTAMP NOT NULL,
  started_at TIMESTAMP NOT NULL,
  finished_at TIMESTAMP,
  error_message TEXT,
  FOREIGN KEY(job_id) REFERENCES dispatch_jobs(id),
  UNIQUE(job_id, attempt_number)
);

CREATE TABLE IF NOT EXISTS worker_registrations (
  id TEXT PRIMARY KEY,
  protocol_version INTEGER NOT NULL,
  pool TEXT,
  labels TEXT NOT NULL DEFAULT '[]',
  capabilities TEXT NOT NULL DEFAULT '[]',
  capacity INTEGER NOT NULL DEFAULT 1,
  ready INTEGER NOT NULL DEFAULT 1,
  draining INTEGER NOT NULL DEFAULT 0,
  active_jobs INTEGER NOT NULL DEFAULT 0,
  last_heartbeat TIMESTAMP NOT NULL,
  registered_at TIMESTAMP NOT NULL,
  updated_at TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_dispatch_jobs_claim ON dispatch_jobs(state, available_at, priority DESC, created_at);
CREATE INDEX IF NOT EXISTS idx_dispatch_jobs_lease ON dispatch_jobs(state, lease_expires_at);
CREATE INDEX IF NOT EXISTS idx_dispatch_jobs_scopes ON dispatch_jobs(state, project_id, source_id, agent_id, runner_id, pool);
CREATE INDEX IF NOT EXISTS idx_dispatch_attempts_job ON dispatch_attempts(job_id, attempt_number);
CREATE INDEX IF NOT EXISTS idx_dispatch_attempts_lease ON dispatch_attempts(state, lease_expires_at);
CREATE INDEX IF NOT EXISTS idx_workers_heartbeat ON worker_registrations(last_heartbeat);

CREATE TABLE IF NOT EXISTS approval_requests (
  id TEXT PRIMARY KEY,
  workflow_instance_id TEXT NOT NULL,
  task_id TEXT, workflow_id TEXT, step_id TEXT NOT NULL, message TEXT,
  approvers TEXT NOT NULL DEFAULT '[]', delegates TEXT NOT NULL DEFAULT '{}', required_approvals INTEGER NOT NULL DEFAULT 1, fields TEXT NOT NULL DEFAULT '[]',
  status TEXT NOT NULL DEFAULT 'pending', response_values TEXT NOT NULL DEFAULT '{}',
  feedback TEXT, responded_by TEXT, response_channel TEXT,
  idempotency_key TEXT UNIQUE, created_at DATETIME NOT NULL, expires_at DATETIME,
  reminded_at DATETIME, escalated_at DATETIME, responded_at DATETIME,
  UNIQUE(workflow_instance_id, step_id)
);
CREATE TABLE IF NOT EXISTS approval_responses (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id TEXT NOT NULL, decision TEXT NOT NULL, actor TEXT NOT NULL, approver TEXT NOT NULL,
  channel TEXT NOT NULL, idempotency_key TEXT NOT NULL UNIQUE,
  feedback TEXT, values_json TEXT NOT NULL DEFAULT '{}', created_at DATETIME NOT NULL,
  UNIQUE(request_id, approver), FOREIGN KEY(request_id) REFERENCES approval_requests(id)
);
CREATE INDEX IF NOT EXISTS idx_approval_requests_status ON approval_requests(status, created_at);
CREATE INDEX IF NOT EXISTS idx_approval_requests_instance ON approval_requests(workflow_instance_id);
CREATE INDEX IF NOT EXISTS idx_wf_instances_state ON workflow_instances(state);
CREATE INDEX IF NOT EXISTS idx_wf_instances_cell ON workflow_instances(cell_id);
CREATE INDEX IF NOT EXISTS idx_wf_instances_parent ON workflow_instances(parent_instance_id);
CREATE INDEX IF NOT EXISTS idx_step_runs_instance ON step_runs(workflow_instance_id);
CREATE INDEX IF NOT EXISTS idx_ci_poll_checks_instance ON ci_poll_checks(workflow_instance_id, step_id);
CREATE INDEX IF NOT EXISTS idx_internal_tasks_state ON internal_tasks(state);
CREATE INDEX IF NOT EXISTS idx_internal_tasks_parent ON internal_tasks(parent_task_id);
CREATE INDEX IF NOT EXISTS idx_source_bindings_task ON source_bindings(task_id);
CREATE INDEX IF NOT EXISTS idx_source_bindings_item ON source_bindings(source_id, source_item_id);
CREATE INDEX IF NOT EXISTS idx_task_pull_requests_task ON task_pull_requests(task_id);

-- Config revisions: one row per recorded effective configuration state.
-- Each row captures the config digest and (when available) git revision at
-- the time of a promote, rollback, or daemon startup. Used by apiary promote,
-- apiary rollback, and the environment audit trail.
CREATE TABLE IF NOT EXISTS config_revisions (
  id TEXT PRIMARY KEY,              -- ulid
  environment TEXT NOT NULL DEFAULT '',  -- environment name, or '' for base config
  config_digest TEXT NOT NULL,          -- hex SHA-256 of the canonical config YAML
  git_revision TEXT NOT NULL DEFAULT '', -- git HEAD at record time, or ''
  event TEXT NOT NULL DEFAULT 'startup', -- startup|promote|rollback
  from_environment TEXT,                 -- set on promote/rollback: the source env
  note TEXT,                            -- optional operator note
  recorded_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_config_revisions_env ON config_revisions(environment, recorded_at DESC);
CREATE INDEX IF NOT EXISTS idx_config_revisions_digest ON config_revisions(config_digest);
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
	`ALTER TABLE task_executions ADD COLUMN pid INTEGER`,
	`ALTER TABLE task_executions ADD COLUMN heartbeat_at TIMESTAMP`,
	`ALTER TABLE task_executions ADD COLUMN heartbeat_count INTEGER DEFAULT 0`,
	`ALTER TABLE task_executions ADD COLUMN input_tokens INTEGER DEFAULT 0`,
	`ALTER TABLE task_executions ADD COLUMN output_tokens INTEGER DEFAULT 0`,
	`ALTER TABLE task_executions ADD COLUMN total_tokens INTEGER DEFAULT 0`,
	// Cache token breakdown of the input (input_tokens already includes these;
	// pure uncached input = input_tokens - cache_creation - cache_read). Reported
	// by the Claude and Cursor CLIs; zero for runners that don't surface it.
	`ALTER TABLE task_executions ADD COLUMN cache_creation_tokens INTEGER DEFAULT 0`,
	`ALTER TABLE task_executions ADD COLUMN cache_read_tokens INTEGER DEFAULT 0`,
	`ALTER TABLE task_executions ADD COLUMN num_turns INTEGER DEFAULT 0`,
	`ALTER TABLE task_executions ADD COLUMN num_tool_calls INTEGER DEFAULT 0`,
	`ALTER TABLE task_executions ADD COLUMN cost_usd REAL DEFAULT 0.0`,
	`ALTER TABLE task_executions ADD COLUMN workflow_instance_id TEXT`,
	`ALTER TABLE task_executions ADD COLUMN step_id TEXT`,
	// Internal Task Model: bind workflow instances to an InternalTask. Nullable
	// during migration; source_item_id (cell_id) + source_id retained until a
	// later phase drops them.
	`ALTER TABLE workflow_instances ADD COLUMN task_id TEXT REFERENCES internal_tasks(id)`,
	// Index on the migration-added task_id column. Must live here (after the
	// ALTER) rather than in the CREATE INDEX block, which runs before migrations.
	// Backs the dashboard's "instances for a task" query (ListWorkflowInstancesByTask).
	`CREATE INDEX IF NOT EXISTS idx_wf_instances_task ON workflow_instances(task_id)`,
	// Internal Task Model: per-step write-back (APIARY_PUBLISH) and internal
	// fan-out (APIARY_SPAWN) tracking.
	`ALTER TABLE step_runs ADD COLUMN publish_payload TEXT`,
	`ALTER TABLE step_runs ADD COLUMN publish_state TEXT`,
	`ALTER TABLE step_runs ADD COLUMN spawned_task_id TEXT REFERENCES internal_tasks(id)`,
	// Per-step prompt capture: the full composed input prompt sent to the runner
	// and the raw agent output text, persisted alongside the token/cost columns
	// for cost auditing and replay. task_executions already carries the token and
	// cost columns (one row per runner invocation, so failovers stay distinct).
	`ALTER TABLE task_executions ADD COLUMN input_prompt TEXT`,
	`ALTER TABLE task_executions ADD COLUMN output_text TEXT`,
	// Per-step usage rollup on the logical step record: token counts and cost are
	// summed across the step's failover attempts; input_prompt holds the final
	// (winning) attempt's composed prompt. Timing (started_at/finished_at) and the
	// output text already live on step_runs.
	`ALTER TABLE step_runs ADD COLUMN input_prompt TEXT`,
	`ALTER TABLE step_runs ADD COLUMN input_tokens INTEGER DEFAULT 0`,
	`ALTER TABLE step_runs ADD COLUMN output_tokens INTEGER DEFAULT 0`,
	`ALTER TABLE step_runs ADD COLUMN total_tokens INTEGER DEFAULT 0`,
	// Cache token breakdown, summed across the step's failover attempts. See the
	// task_executions cache columns above for semantics.
	`ALTER TABLE step_runs ADD COLUMN cache_creation_tokens INTEGER DEFAULT 0`,
	`ALTER TABLE step_runs ADD COLUMN cache_read_tokens INTEGER DEFAULT 0`,
	`ALTER TABLE step_runs ADD COLUMN num_turns INTEGER DEFAULT 0`,
	`ALTER TABLE step_runs ADD COLUMN num_tool_calls INTEGER DEFAULT 0`,
	`ALTER TABLE step_runs ADD COLUMN cost_usd REAL DEFAULT 0.0`,
	// Idempotent spawn (issue #119): a deterministic per-parent key so a re-run of
	// the same decomposition resolves to the existing child instead of creating a
	// duplicate set of sub-issues. The partial unique index enforces at-most-one
	// child per (parent, dedup_key); NULL/empty keys (source-bound tasks) are exempt.
	`ALTER TABLE internal_tasks ADD COLUMN dedup_key TEXT`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_internal_tasks_dedup ON internal_tasks(parent_task_id, dedup_key) WHERE dedup_key IS NOT NULL AND dedup_key != ''`,
	// Dispatch generations: reopening a settled task (re-dispatch/escalation)
	// starts a new round, and task settlement aggregates failures only within
	// the current round. Without this, one failed instance kept the task failed
	// forever, even after a later round completed successfully.
	`ALTER TABLE internal_tasks ADD COLUMN generation INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE workflow_instances ADD COLUMN task_generation INTEGER NOT NULL DEFAULT 0`,
	// Credit-aware fallback: failure classification columns on execution rows.
	// credit_exhausted is a convenience boolean; failure_kind is the canonical
	// value set by the FailureDetector: "none", "rate_limited", "credit_exhausted",
	// "aborted".
	`ALTER TABLE task_executions ADD COLUMN credit_exhausted INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE task_executions ADD COLUMN failure_kind TEXT`,
	// Environment promotion: record the effective config digest and git
	// revision on each workflow instance so the audit trail is queryable.
	`ALTER TABLE workflow_instances ADD COLUMN config_digest TEXT`,
	`ALTER TABLE workflow_instances ADD COLUMN git_revision TEXT`,
	`ALTER TABLE workflow_instances ADD COLUMN environment TEXT`,
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
	if err := normalizeLegacyTimestamps(ctx, db); err != nil {
		return fmt.Errorf("normalize legacy timestamps: %w", err)
	}
	if err := repairSupersededFailedTasks(ctx, db); err != nil {
		return fmt.Errorf("repair superseded failed tasks: %w", err)
	}
	return nil
}

// repairSupersededFailedTasks corrects tasks stranded in 'failed' by builds
// that aggregated failed instances across the task's whole history: a failed
// instance kept failing the task even after a later re-dispatch or escalation
// completed successfully. A task qualifies when it is settled (no outstanding
// workflows) and its newest terminal top-level instance is done — the last
// round of work actually succeeded. Sub-workflow child instances are excluded
// because they are inserted before their parent settles, so by-rowid ordering
// would misread a done child under a failed parent. Idempotent: flipped rows
// stop matching.
func repairSupersededFailedTasks(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		UPDATE internal_tasks
		SET state = 'done', updated_at = ?
		WHERE state = 'failed'
		  AND COALESCE(outstanding_workflows, 0) = 0
		  AND (
		    SELECT wi.state FROM workflow_instances wi
		    WHERE wi.task_id = internal_tasks.id
		      AND (wi.parent_instance_id IS NULL OR wi.parent_instance_id = '')
		      AND wi.state IN ('done','failed')
		    ORDER BY wi.rowid DESC LIMIT 1
		  ) = 'done'
	`, time.Now())
	return err
}

// legacyTimestampColumns lists every TIMESTAMP column written from a time.Time.
// Older builds (and beta binaries before the _time_format=sqlite DSN fix) used
// modernc's default time.Time.String() encoding — "2006-01-02 15:04:05.999999999
// -0700 MST m=±…" — which SQLite's DATE()/datetime() cannot parse. Those rows
// vanish from windowed/grouped dashboard queries. normalizeLegacyTimestamps
// rewrites them in place to the canonical SQLite layout.
var legacyTimestampColumns = []struct{ table, column string }{
	{"task_executions", "started_at"},
	{"task_executions", "completed_at"},
	{"task_executions", "next_retry_at"},
	{"task_executions", "created_at"},
	{"task_executions", "heartbeat_at"},
	{"tasks", "started_at"},
	{"tasks", "completed_at"},
	{"tasks", "created_at"},
	{"tasks", "updated_at"},
	{"task_checkpoints", "created_at"},
	{"workflow_instances", "created_at"},
	{"workflow_instances", "updated_at"},
	{"step_runs", "started_at"},
	{"step_runs", "finished_at"},
	{"internal_tasks", "created_at"},
	{"internal_tasks", "updated_at"},
	{"source_bindings", "created_at"},
	{"source_bindings", "updated_at"},
	{"task_logs", "timestamp"},
	{"service_logs", "timestamp"},
}

// sqliteTimeLayout is the canonical on-disk format (parseTimeFormats[0] in
// modernc) that DATE()/datetime() parse and that _time_format=sqlite now writes.
const sqliteTimeLayout = "2006-01-02 15:04:05.999999999-07:00"

// normalizeLegacyTimestamps rewrites Go-native time.Time.String() values (which
// carry two-plus space-separated tokens, e.g. " -0700 MST m=…") into the
// canonical SQLite layout. It is idempotent: already-canonical values have a
// single space and are skipped by the LIKE filter, so re-runs on a clean DB are
// a no-op. Per column, all updates run in one transaction.
func normalizeLegacyTimestamps(ctx context.Context, db *sql.DB) error {
	for _, tc := range legacyTimestampColumns {
		// A canonical value has exactly one space (date/time separator); the
		// broken Go encoding always has two or more ("… -0700 MST [m=…]").
		// CAST(... AS TEXT) is essential: scanning a TIMESTAMP column into a Go
		// string makes modernc auto-parse and reformat it to RFC3339, hiding the
		// raw bytes we need to normalize. The CAST strips the column's type
		// affinity so the literal stored text comes through unchanged.
		q := fmt.Sprintf(
			"SELECT rowid, CAST(%s AS TEXT) FROM %s WHERE %s LIKE '%% %% %%'",
			tc.column, tc.table, tc.column,
		)
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			// Table/column absent on this DB (e.g. partial schema) — skip.
			if strings.Contains(err.Error(), "no such table") ||
				strings.Contains(err.Error(), "no such column") {
				continue
			}
			return err
		}
		type fix struct {
			rowid int64
			val   string
		}
		var fixes []fix
		for rows.Next() {
			var rowid int64
			var raw string
			if err := rows.Scan(&rowid, &raw); err != nil {
				rows.Close()
				return err
			}
			if norm, ok := normalizeGoTime(raw); ok {
				fixes = append(fixes, fix{rowid, norm})
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(fixes) == 0 {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		upd := fmt.Sprintf("UPDATE %s SET %s = ? WHERE rowid = ?", tc.table, tc.column)
		stmt, err := tx.PrepareContext(ctx, upd)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		for _, f := range fixes {
			if _, err := stmt.ExecContext(ctx, f.val, f.rowid); err != nil {
				stmt.Close()
				_ = tx.Rollback()
				return err
			}
		}
		stmt.Close()
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// normalizeGoTime converts a Go time.Time.String() rendering — e.g.
// "2026-06-06 18:14:37.630756 -0400 -04 m=+301.17" — into the canonical SQLite
// layout "2026-06-06 18:14:37.630756-04:00". It returns ok=false for values it
// cannot parse, leaving them untouched. Already-canonical values never reach
// here (filtered out by the caller's single-space LIKE).
func normalizeGoTime(s string) (string, bool) {
	if i := strings.Index(s, " m="); i >= 0 {
		s = s[:i] // drop the monotonic-clock suffix
	}
	// Fields: date, time, numeric-offset, [zone-abbrev]. The abbreviation is
	// redundant given the numeric offset, so parse only the first three.
	parts := strings.Fields(s)
	if len(parts) < 3 {
		return "", false
	}
	base := parts[0] + " " + parts[1] + " " + parts[2]
	t, err := time.Parse("2006-01-02 15:04:05.999999999 -0700", base)
	if err != nil {
		return "", false
	}
	return t.Format(sqliteTimeLayout), true
}
