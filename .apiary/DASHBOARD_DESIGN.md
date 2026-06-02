# Apiary Dashboard - k9s Style Terminal UI

## Architecture

### 1. **API Server** (New)
Add HTTP endpoint to expose dispatcher state:

```
GET /api/v1/status
  └─ dispatcher status
  └─ sources (polling, cell count, last poll)
  └─ agents (active, idle, queued tasks)
  └─ tasks (pending, running, completed)
  └─ metrics (throughput, avg duration, success rate)

WebSocket /api/v1/stream
  └─ Real-time updates (task state changes, agent events)
```

### 2. **Dashboard CLI** (New binary: `apiary dashboard`)
Built with **Bubble Tea** (Go TUI framework):

```
┌─────────────────────────────────────────────────────────────────────┐
│ APIARY DISPATCHER DASHBOARD                                         │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│ SOURCES                                        STATUS: ✓ Healthy   │
│ ├─ project-erp          [████████] 11/11      Polling: 60s (5min)  │
│                                                                     │
│ AGENTS         STATUS   WORKING   QUEUE   AVG TIME                 │
│ ├─ engineer     ✓ active      2       3      12.4s                │
│ ├─ reviewer     ✓ active      1       0       8.2s                │
│ ├─ qa           ○ idle        0       1      15.1s                │
│ ├─ po           ○ idle        0       0       9.3s                │
│                                                                     │
│ RUNNING TASKS (3)                                                  │
│ ├─ engineer-001: Fix Restaurante...              [████░░░░] 45%   │
│ ├─ reviewer-002: Review PR #1234...              [██████░░░] 60%  │
│ ├─ qa-001: Test login flow...                    [███░░░░░░] 30%  │
│                                                                     │
│ COMPLETED TODAY (8)                                                │
│ ├─ ✓ Fix database query (8m 23s)                                  │
│ ├─ ✓ Update documentation (3m 12s)                                │
│ ├─ ✓ Review PR #1233 (11m 45s)                                    │
│ ...                                                                │
│                                                                     │
│ QUEUE (2)                                                          │
│ ├─ Validate form validation (awaiting engineer)                   │
│ ├─ Test API endpoints (awaiting qa)                               │
│                                                                     │
├─────────────────────────────────────────────────────────────────────┤
│ Legend: ✓=active ○=idle ↻=running   Press 'h' for help, 'q' to exit │
└─────────────────────────────────────────────────────────────────────┘
```

## Implementation Steps

### Phase 1: API Endpoint
- [ ] Add `/api/v1/status` HTTP endpoint
- [ ] Expose dispatcher state (sources, agents, tasks, metrics)
- [ ] Update dispatcher to track active runs, queued tasks, completed tasks
- [ ] Add performance metrics (avg duration, success rate)

**New file**: `src/internal/api/server.go`
**Modified**: `src/internal/daemon/dispatcher.go` (add state tracking)

### Phase 2: Dashboard CLI
- [ ] Create `cmd/apiary-dashboard` binary
- [ ] Connect to dispatcher API
- [ ] Build TUI with:
  - **Tabs**: Overview, Agents, Tasks, Metrics, Logs
  - **Real-time updates** (WebSocket or polling)
  - **Keyboard navigation**: Arrow keys to select, Enter to view details
  - **Status colors**: Green (active), Gray (idle), Red (error)
- [ ] Show agent details on click (worker ID, model, task history)
- [ ] Show task details on click (full output, logs, state transitions)

**New file**: `cmd/apiary-dashboard/main.go`
**Library**: `github.com/charmbracelet/bubbletea` (TUI framework)

### Phase 3: Advanced Features
- [ ] Real-time metrics dashboard (charts of throughput, latency)
- [ ] Agent performance leaderboard (tasks completed, avg time, success rate)
- [ ] Historical task view (search, filter by agent, state, duration)
- [ ] Logs viewer with filtering
- [ ] WebSocket push updates (instead of polling)

## API Endpoints

### GET /api/v1/status
```json
{
  "dispatcher": {
    "uptime": "2h 34m",
    "status": "healthy",
    "version": "0.1.0"
  },
  "sources": [
    {
      "id": "project-erp",
      "type": "plane",
      "cells_found": 11,
      "last_poll": "2026-06-02T10:15:30Z",
      "next_poll": "2026-06-02T10:16:30Z",
      "poll_interval": "60s"
    }
  ],
  "agents": [
    {
      "id": "engineer",
      "status": "active",
      "current_task": "cell-abc123",
      "task_title": "Fix login bug",
      "started_at": "2026-06-02T10:12:15Z",
      "queued_tasks": 3,
      "total_completed": 12,
      "avg_duration": "12.4s",
      "success_rate": 0.95
    }
  ],
  "tasks": {
    "pending": 2,
    "running": 3,
    "completed": 8,
    "failed": 0,
    "active_runs": [
      {
        "cell_id": "abc123",
        "title": "Fix Restaurante Comandas",
        "agent": "engineer",
        "started": "2026-06-02T10:12:15Z",
        "progress": "45%"
      }
    ]
  },
  "metrics": {
    "throughput": "0.5 tasks/min",
    "avg_duration": "11.2s",
    "success_rate": 0.94,
    "uptime": 0.99
  }
}
```

### WebSocket /api/v1/stream
```json
{
  "type": "task.started",
  "data": {"cell_id": "xyz789", "agent": "qa", "title": "Test form"}
}

{
  "type": "task.completed",
  "data": {"cell_id": "abc123", "success": true, "duration": "12.4s"}
}

{
  "type": "agent.status_changed",
  "data": {"agent": "engineer", "status": "active", "task": "abc123"}
}
```

## Dashboard Features

### Overview Tab
- Dispatcher health (uptime, status)
- Real-time agent status (active, idle, queue)
- Task counts (pending, running, completed, failed)
- System metrics (throughput, avg time, success rate)

### Agents Tab
- List all agents with status, current task, queue size
- Click to see agent details: task history, performance, logs
- Color-coded: 🟢 active, 🔘 idle, 🔴 error

### Tasks Tab
- Running tasks with progress bar
- Completed tasks (today) with duration
- Pending queue (waiting for agent)
- Click to see full task details, logs, output

### Metrics Tab
- Charts: throughput (tasks/min), latency (avg duration)
- Agent performance leaderboard (tasks completed, success rate)
- System uptime and health over time

### Logs Tab
- Live log stream from dispatcher
- Filter by level (INFO, DEBUG, ERROR)
- Search by agent, task ID, or keyword

## Keyboard Shortcuts

```
q/Ctrl+C    Exit
h           Help
Tab         Next tab
Shift+Tab   Previous tab
↑/↓         Navigate
←/→         Scroll
Enter       View details
e           Edit selection (filter/view)
c           Clear completed tasks
r           Refresh now
```

## Usage

```bash
# Start dispatcher
apiary run

# In another terminal, start dashboard
apiary-dashboard --api http://localhost:8080

# Or with custom API endpoint
apiary-dashboard --api http://dispatcher.example.com:8080
```

## Files to Create

```
src/
├── internal/
│   ├── api/
│   │   ├── server.go        # HTTP API server
│   │   ├── handlers.go      # Route handlers
│   │   └── websocket.go     # WebSocket handler
│   └── metrics/
│       └── collector.go     # Metrics tracking
│
cmd/
├── apiary-dashboard/
│   ├── main.go              # Dashboard entry point
│   ├── ui/
│   │   ├── app.go           # Bubble Tea app
│   │   ├── views.go         # Tab views
│   │   ├── components.go    # Reusable components
│   │   └── styles.go        # Colors and styles
│   └── api/
│       └── client.go        # API client
```

## Dependencies

```go
require (
    github.com/charmbracelet/bubbletea v0.24.0
    github.com/charmbracelet/lipgloss v0.8.0
    github.com/charmbracelet/log v0.2.0
)
```

## Benefits

✓ **Real-time visibility** into dispatcher and agent activity  
✓ **k9s-familiar** interface for developers  
✓ **No browser needed** - works in terminal, SSH, etc.  
✓ **Performance metrics** to track system health  
✓ **Debugging** - watch agent execution live  
✓ **Historical view** - task history and performance  

## Future Enhancements

- Mobile-responsive web UI alongside TUI
- Persistence of metrics to database
- Alerting (Slack, Discord) when tasks fail
- Auto-scaling recommendations based on queue depth
- Agent performance profiling and optimization suggestions
