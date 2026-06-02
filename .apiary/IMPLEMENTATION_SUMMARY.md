# Apiary Implementation Summary

## Completed Work

### Phase 1: Agent Config Redesign ✅

Separated agent definitions from routing logic:
- Agents have explicit IDs, descriptions, soul files, preferred models, and skills
- Routes reference agents by ID instead of workers
- Config: `apiary.yaml` contains both agents and routes
- Soul files: markdown files with agent personality/expertise loaded at dispatch time
- Default directory: `.apiary/` for skills and souls

**Key files:**
- `src/internal/config/config.go` — AgentConfig struct, Config updates
- `src/internal/config/validate.go` — Agent validation
- `src/internal/daemon/dispatcher.go` — Soul file loading, agent instantiation

### Phase 2: SQLite + Logging Foundation ✅

Built persistent state storage and structured logging:

**Database Schema:**
- `task_executions` — Track each attempt (task_id, agent_id, attempt, status, duration, error, can_retry, next_retry_at)
- `task_checkpoints` — Store execution stages (for future resumption)
- `tasks` — Task history (for dashboard)
- `agents` — Agent tracking (for dashboard)
- `task_logs`, `service_logs` — Detailed logging

**Logging System:**
- File-based logging to `~/.apiary/logs/apiary.log`
- Per-task logs to `~/.apiary/logs/tasks/{taskID}.log`
- SQLite logging for structured queries
- Logger interface: `Info()`, `Debug()`, `Error()`, `TaskInfo()`, `TaskError()`

**Key files:**
- `src/internal/db/schema.go` — SQLite schema with tables and indices
- `src/internal/db/client.go` — Database operations (execution tracking, task state, logging)
- `src/internal/logging/logger.go` — Structured logger (file + DB)
- `src/internal/cli/run.go` — Database initialization (paths: `~/.apiary/apiary.db`, `~/.apiary/logs/`)

### Phase 3: Crash Recovery Implementation ✅

Enabled automatic retry with exponential backoff:

**Configuration:**
```yaml
retry_policy:
  enabled: true
  max_attempts: 3
  backoff_strategy: exponential  # or "fixed"
  backoff_base: 1s               # base delay for backoff calculation
  
  retriable_errors:              # Errors that should retry
    - timeout
    - connection_error
    - rate_limited
  
  non_retriable_errors:          # Errors that should NOT retry
    - validation_error
    - not_found
```

**Behavior:**
1. On task execution, create execution record (task_id, agent_id, attempt, status, started_at)
2. After execution:
   - If success → mark as completed
   - If failure and retriable → schedule retry with `next_retry_at = now + backoff`
   - If failure and non-retriable → mark as failed, don't retry
3. Backoff calculation:
   - Exponential: `backoff_base * 2^(attempt-1)` → 1s, 2s, 4s, 8s...
   - Fixed: `backoff_base * attempt` → 1s, 2s, 3s, 4s...

**Key files:**
- `src/internal/config/config.go` — RetryPolicy struct, defaults
- `src/internal/daemon/retry.go` — RetryManager (IsRetriable, GetBackoffDuration, ShouldRetry)
- `src/internal/daemon/dispatcher.go` — Execution tracking, retry scheduling

## Architecture

```
┌─ CLI (apiary run, apiary validate, etc.)
├─ Config (apiary.yaml) with agents, routes, retry_policy
├─ Sources (Plane) → poll for tasks
├─ Router → match tasks to agents
├─ Dispatcher
│  ├─ SQLite Database (~/.apiary/apiary.db)
│  ├─ Structured Logger (file + DB)
│  ├─ RetryManager (exponential backoff)
│  └─ Runners (CLI, Script, etc.)
└─ Results → write back to sources (Plane)
```

## Current Limitations & Future Work

### Retry Mechanism (Not Yet Implemented)
- Execution records are created with `next_retry_at` timestamp
- But the poll loop doesn't check for pending retries yet
- **Need to add:** Retry queue check in `pollLoop()` and `RunOnce()` to re-dispatch when `next_retry_at <= now()`

### Dashboard & Service Management (Deferred)
- Database structure exists but no TUI yet
- Service management (systemd) not yet implemented
- Can be added in Phase 4

### Monitoring & Alerts (Future)
- Task execution history is persisted but no queries/reports yet
- No alerting on repeated failures

## Testing Crash Recovery

1. **Create a task that will fail:**
   ```bash
   # Set up a route that maps to an agent
   # Configure retry_policy with max_attempts: 3
   apiary run --once
   ```

2. **Check execution history:**
   ```bash
   sqlite3 ~/.apiary/apiary.db "SELECT * FROM task_executions WHERE task_id='abc123';"
   ```

3. **View logs:**
   ```bash
   tail -f ~/.apiary/logs/apiary.log
   tail -f ~/.apiary/logs/tasks/{task_id}.log
   ```

## Configuration Example

See `.apiary/example-with-recovery.yaml` for a complete example with retry policy.

## Summary

✅ Agent config redesign (agents separate from routes)  
✅ SQLite + structured logging (file + DB)  
✅ Crash recovery with retry tracking (execution state, backoff calculation)  
⏳ Retry queue mechanism (poll loop integration) — next step  
⏳ Dashboard TUI (k9s-style monitoring) — Phase 4  
⏳ Service management (systemd) — Phase 4

The foundation is in place. Next step: integrate retry queue checks into the poll loop so scheduled retries actually fire.
