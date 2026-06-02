# Apiary Dashboard Implementation Plan

## Architecture Overview

**Single Binary**: `apiary`  
**State Storage**: SQLite (local)  
**UI**: Embedded Terminal Dashboard (Bubble Tea)  
**Service**: Optional systemd integration  

```
┌─ apiary (binary)
├─ run (default)      → Start dispatcher + embedded dashboard
├─ dashboard          → View dashboard (connects to running dispatcher DB)
├─ logs               → View service/task logs
├─ start              → Start as systemd service
├─ stop               → Stop systemd service
├─ status             → Show service status
├─ install            → Install as systemd service
├─ uninstall          → Remove systemd service
├─ validate           → Validate config
├─ config             → Manage configuration
└─ [other]            → All existing CLI functions
```

## Command Structure

### Core Operations

```bash
# Start dispatcher with embedded dashboard (default)
apiary run

# Start dispatcher in background (systemd service)
apiary install
apiary start

# View dashboard (if dispatcher already running)
apiary dashboard

# View logs
apiary logs                    # All logs
apiary logs --follow          # Tail in real-time
apiary logs --level ERROR      # Filter by level
apiary logs --task abc123     # Task-specific logs
apiary logs --agent engineer   # Agent-specific logs

# Service management
apiary start                   # Start service
apiary stop                    # Stop service
apiary status                  # Show service status
apiary restart                 # Restart service

# All existing CLI functions still work
apiary validate
apiary config get default_runner
apiary config set default_runner claude-api
```

## Implementation Structure

```
src/
├── internal/
│   ├── config/             (existing)
│   ├── daemon/             (existing - dispatcher)
│   ├── router/             (existing)
│   ├── source/             (existing)
│   ├── runner/             (existing)
│   ├── db/                 (NEW)
│   │   ├── schema.go       # SQLite schema + migrations
│   │   ├── client.go       # DB operations
│   │   └── models.go       # Data structures
│   ├── logging/            (NEW)
│   │   ├── file.go         # File-based logging
│   │   ├── db.go           # Log to SQLite
│   │   └── logger.go       # Unified logger
│   ├── dashboard/          (NEW)
│   │   ├── app.go          # Bubble Tea app
│   │   ├── views.go        # Tab views
│   │   ├── models.go       # TUI models
│   │   └── styles.go       # Colors/styles
│   └── service/            (NEW)
│       ├── systemd.go      # systemd service management
│       └── types.go        # Service types
│
cmd/
├── apiary/                 (existing - modify)
│   ├── main.go            # Entry point (route to subcommands)
│   ├── cmd/               # Subcommand handlers
│   │   ├── run.go         # dispatcher + dashboard
│   │   ├── dashboard.go   # dashboard only
│   │   ├── logs.go        # log viewer
│   │   ├── service.go     # systemd management
│   │   └── config.go      # config management
│   └── cli/               (existing)
```

## SQLite Schema

```sql
-- Dispatcher state
CREATE TABLE dispatcher_state (
  id INTEGER PRIMARY KEY,
  status TEXT,              -- healthy, degraded, error
  uptime_seconds INTEGER,
  version TEXT,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Agent tracking
CREATE TABLE agents (
  id TEXT PRIMARY KEY,
  description TEXT,
  status TEXT,              -- active, idle, error
  current_task_id TEXT,
  queued_count INTEGER DEFAULT 0,
  total_completed INTEGER DEFAULT 0,
  avg_duration_ms INTEGER DEFAULT 0,
  success_rate REAL DEFAULT 0.0,
  last_task_ended_at TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Task history
CREATE TABLE tasks (
  id TEXT PRIMARY KEY,
  source_id TEXT,
  title TEXT,
  agent_id TEXT,
  state TEXT,               -- pending, running, completed, failed
  started_at TIMESTAMP,
  completed_at TIMESTAMP,
  duration_ms INTEGER,
  success BOOLEAN,
  output TEXT,              -- truncated for display
  full_output TEXT,         -- full output, nullable
  error_message TEXT,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(agent_id) REFERENCES agents(id)
);

-- Detailed task logs (for log viewer)
CREATE TABLE task_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT,
  level TEXT,               -- DEBUG, INFO, WARN, ERROR
  message TEXT,
  timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(task_id) REFERENCES tasks(id)
);

-- Service logs
CREATE TABLE service_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  level TEXT,               -- DEBUG, INFO, WARN, ERROR
  message TEXT,
  component TEXT,           -- dispatcher, router, runner, etc.
  timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Metrics (for performance dashboard)
CREATE TABLE metrics (
  timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  throughput REAL,          -- tasks/min
  avg_duration_ms INTEGER,
  success_rate REAL,
  queued_count INTEGER,
  active_agents INTEGER,
  PRIMARY KEY(timestamp)
);

-- Indices
CREATE INDEX idx_tasks_agent ON tasks(agent_id);
CREATE INDEX idx_tasks_state ON tasks(state);
CREATE INDEX idx_tasks_created ON tasks(created_at DESC);
CREATE INDEX idx_task_logs_task ON task_logs(task_id);
CREATE INDEX idx_service_logs_timestamp ON service_logs(timestamp DESC);
CREATE INDEX idx_metrics_timestamp ON metrics(timestamp DESC);
```

## Dashboard Views

### 1. Overview (Default)
```
┌─────────────────────────────────────────────────────────┐
│ APIARY DISPATCHER DASHBOARD                             │
├─────────────────────────────────────────────────────────┤
│                                                         │
│ Status: ✓ Healthy  |  Uptime: 2h 34m                   │
│                                                         │
│ AGENTS STATUS                TASKS TODAY                │
│ ├─ engineer    ✓ (2 active)  ├─ Completed:  8          │
│ ├─ reviewer    ✓ (1 active)  ├─ Failed:     0          │
│ ├─ qa          ○ (0 active)  ├─ Running:    3          │
│ ├─ po          ○ (0 active)  └─ Pending:    2          │
│                                                         │
│ RUNNING TASKS (3)                                       │
│ 1. engineer: Fix login bug        [████░░░░] 45% 12s   │
│ 2. reviewer: Review PR #234       [██████░░] 60% 8s    │
│ 3. qa: Test form validation       [███░░░░░] 30% 5s    │
│                                                         │
│ METRICS                                                 │
│ Throughput: 0.5 tasks/min  |  Avg Time: 11.2s          │
│ Success Rate: 94%          |  Queue: 2 tasks           │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

### 2. Tasks Tab
- List all tasks with filters (agent, state, date)
- Click to see full output and logs
- Search by title

### 3. Agents Tab
- Per-agent performance metrics
- Task history for each agent
- Success rate, avg duration

### 4. Logs Tab
- Real-time log stream
- Filter by level, component, agent
- Search functionality
- Follow mode (`tail -f` style)

### 5. Details Modal
- Full task output
- Step-by-step execution logs
- Error messages with stack traces
- Raw task data (JSON)

## Logging System

### File Logging
```
~/.apiary/
├── logs/
│   ├── apiary.log         # Service log (rotated daily)
│   ├── apiary-2024-06.log # Archive
│   └── tasks/
│       ├── abc123.log     # Individual task log
│       └── xyz789.log
└── apiary.db             # SQLite state
```

### Structured Logging
```go
// All logs go to:
// 1. SQLite (for dashboard queries)
// 2. File (for persistence)
// 3. Stdout (for terminal)

aplog.Info("task dispatched", 
  "task_id", cell.ID,
  "agent", agentID,
  "model", model,
)
```

### Log Viewer
```bash
# View all logs with real-time tail
apiary logs --follow

# Filter by level
apiary logs --level ERROR

# View specific task
apiary logs --task 6fc6b7c3-9452

# View specific agent
apiary logs --agent engineer

# Search logs
apiary logs --grep "error|failed"

# Time-based filtering
apiary logs --since "10 minutes ago"
apiary logs --since "2026-06-02 10:00:00"
```

## Service Management

### Install as systemd service
```bash
apiary install
# Creates: /etc/systemd/system/apiary.service
# Sets up: logs at ~/.apiary/logs/apiary.log
# Database: ~/.apiary/apiary.db
```

### Service file template
```ini
[Unit]
Description=Apiary Task Dispatcher
After=network.target

[Service]
Type=simple
User=orlando
WorkingDirectory=/path/to/project
ExecStart=/usr/local/bin/apiary run --db ~/.apiary/apiary.db
Restart=on-failure
StandardOutput=append:/home/orlando/.apiary/logs/apiary.log
StandardError=append:/home/orlando/.apiary/logs/apiary.log

[Install]
WantedBy=multi-user.target
```

### Service commands
```bash
apiary install          # Install service
apiary uninstall        # Remove service
apiary start            # systemctl start apiary
apiary stop             # systemctl stop apiary
apiary restart          # systemctl restart apiary
apiary status           # systemctl status apiary
apiary logs --follow    # View service logs (tail -f style)
```

## Dashboard Integration

### Embedded Dashboard (Primary)
```bash
apiary run
# Shows dispatcher + dashboard in same terminal
# Updates in real-time as tasks execute
# Press 'q' to quit, 'd' for dashboard view, 'l' for logs
```

### Standalone Dashboard
```bash
# If dispatcher is running in background/systemd:
apiary dashboard
# Opens dashboard to monitor running dispatcher
```

## Full CLI Access

**All functions remain available via CLI:**

```bash
apiary validate                    # Validate config
apiary run                        # Run dispatcher (with dashboard)
apiary dashboard                  # View dashboard only
apiary logs [options]             # View logs
apiary config get key             # Get config value
apiary config set key value       # Set config value
apiary install                    # Install service
apiary start/stop/restart/status  # Control service
apiary --version                  # Show version
apiary --help                     # Show help
```

## Features

✓ **Single binary** (`apiary`) handles all operations  
✓ **Embedded dashboard** shows real-time status  
✓ **SQLite database** for state and logs  
✓ **Service management** (install/start/stop)  
✓ **Full log access** (service logs + task logs)  
✓ **CLI compatibility** - all functions work from terminal  
✓ **Real-time updates** in dashboard and logs  
✓ **Search & filter** capabilities  
✓ **Persistent storage** of task history and metrics  

## Implementation Timeline

| Phase | Task | Time |
|-------|------|------|
| 1 | SQLite integration + schema | 2h |
| 2 | Logging system (file + DB) | 1.5h |
| 3 | Service management (systemd) | 1h |
| 4 | Dashboard UI (Bubble Tea) | 3h |
| 5 | CLI commands (start/stop/logs/etc) | 1.5h |
| 6 | Integration & testing | 2h |
| **Total** | | **~11h** |

## Next Steps

1. Add SQLite dependency and schema
2. Modify dispatcher to write state to DB
3. Update logger to write to file + DB
4. Add systemd service management
5. Build dashboard TUI
6. Add CLI commands
7. Test full workflow
