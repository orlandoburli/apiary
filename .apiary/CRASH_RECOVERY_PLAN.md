# Crash Recovery Implementation Plan

## Scope: Crash Recovery Only

**Goal**: Enable automatic retry and recovery from agent execution failures.

## SQLite Schema (Minimal)

```sql
-- Task execution attempts
CREATE TABLE task_executions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  attempt NUMBER DEFAULT 1,
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

-- Simple checkpoint system
CREATE TABLE task_checkpoints (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT NOT NULL,
  attempt NUMBER,
  stage TEXT,                     -- initialized, running, completed
  metadata JSON,                  -- Optional state data
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(task_id) REFERENCES tasks(id)
);

CREATE INDEX idx_executions_task ON task_executions(task_id);
CREATE INDEX idx_executions_retry ON task_executions(next_retry_at) WHERE status='failed';
```

## Configuration (YAML)

```yaml
# apiary.yaml
retry_policy:
  enabled: true
  max_attempts: 3
  backoff_strategy: exponential    # exponential or fixed
  backoff_base: 1s                 # 1s, 2s, 4s for exponential
  
  retriable_errors:                # Errors that should trigger retry
    - timeout
    - connection_error
    - resource_unavailable
    - rate_limited
  
  non_retriable_errors:            # Errors that should NOT retry
    - validation_error
    - configuration_error
    - not_found
```

## Code Changes

### 1. Config Types (`src/internal/config/config.go`)

```go
type RetryPolicy struct {
  Enabled           bool
  MaxAttempts       int
  BackoffStrategy   string        // "exponential" or "fixed"
  BackoffBase       time.Duration
  RetriableErrors   []string
  NonRetriableErrors []string
}

type Config struct {
  // ... existing fields ...
  RetryPolicy RetryPolicy `yaml:"retry_policy"`
}
```

### 2. Database Layer (`src/internal/db/execution.go`)

```go
package db

type Execution struct {
  ID          int64
  TaskID      string
  AgentID     string
  Attempt     int
  Status      string        // pending, running, success, failed
  StartedAt   *time.Time
  CompletedAt *time.Time
  DurationMs  int64
  ErrorMsg    string
  CanRetry    bool
  NextRetryAt *time.Time
  CreatedAt   time.Time
}

func (c *Client) CreateExecution(ctx context.Context, taskID, agentID string) (*Execution, error)
func (c *Client) UpdateExecution(ctx context.Context, exec *Execution) error
func (c *Client) GetLastExecution(ctx context.Context, taskID string) (*Execution, error)
func (c *Client) ShouldRetry(ctx context.Context, taskID string) (bool, *time.Time)
```

### 3. Retry Logic (`src/internal/daemon/retry.go`)

```go
package daemon

type RetryManager struct {
  policy *config.RetryPolicy
  db     *db.Client
}

// IsRetriable determines if an error should trigger a retry
func (rm *RetryManager) IsRetriable(err error) bool {
  errMsg := err.Error()
  for _, pattern := range rm.policy.RetriableErrors {
    if strings.Contains(errMsg, pattern) {
      return true
    }
  }
  return false
}

// GetBackoffDuration calculates wait time before retry
func (rm *RetryManager) GetBackoffDuration(attempt int) time.Duration {
  if rm.policy.BackoffStrategy == "exponential" {
    // 1s, 2s, 4s, 8s, etc.
    return rm.policy.BackoffBase * time.Duration(math.Pow(2, float64(attempt-1)))
  }
  // Fixed backoff
  return rm.policy.BackoffBase * time.Duration(attempt)
}

// ScheduleRetry marks task for retry and sets next retry time
func (rm *RetryManager) ScheduleRetry(ctx context.Context, exec *Execution) error {
  backoff := rm.GetBackoffDuration(exec.Attempt)
  exec.NextRetryAt = pointerto.Time(time.Now().Add(backoff))
  return rm.db.UpdateExecution(ctx, exec)
}
```

### 4. Dispatcher Integration (`src/internal/daemon/dispatcher.go`)

```go
// In Dispatcher struct
retryMgr *retry.RetryManager

// Modified dispatch flow
func (d *Dispatcher) processCell(ctx context.Context, cell model.Cell, adapter source.Adapter) {
  for {
    // Check if we should retry
    shouldRetry, nextRetry := d.db.ShouldRetry(ctx, cell.ID)
    if shouldRetry && nextRetry.After(time.Now()) {
      // Not yet time to retry
      d.rescheduleCell(cell, *nextRetry)
      return
    }
    
    // Get last execution to determine attempt number
    lastExec, _ := d.db.GetLastExecution(ctx, cell.ID)
    attempt := 1
    if lastExec != nil {
      attempt = lastExec.Attempt + 1
    }
    
    if attempt > d.cfg.RetryPolicy.MaxAttempts {
      // Exhausted retries
      aplog.Error("cell %s: exhausted max retries (%d)", cell.ID, d.cfg.RetryPolicy.MaxAttempts)
      return
    }
    
    // Create execution record
    exec := &db.Execution{
      TaskID:  cell.ID,
      AgentID: match.Route.Agent,
      Attempt: attempt,
      Status:  "running",
    }
    d.db.CreateExecution(ctx, exec)
    
    // Dispatch to agent
    result := d.dispatch(ctx, cell, adapter, match)
    
    // Update execution with result
    if result.Success {
      exec.Status = "success"
      d.db.UpdateExecution(ctx, exec)
      aplog.Info("cell %s: completed after %d attempt(s)", cell.ID, attempt)
      return
    }
    
    // Check if error is retriable
    if !d.retryMgr.IsRetriable(result.Error) {
      exec.Status = "failed"
      exec.CanRetry = false
      d.db.UpdateExecution(ctx, exec)
      aplog.Error("cell %s: non-retriable error: %v", cell.ID, result.Error)
      return
    }
    
    // Schedule retry
    exec.Status = "failed"
    exec.CanRetry = true
    exec.ErrorMsg = result.Error.Error()
    backoff := d.retryMgr.GetBackoffDuration(attempt)
    exec.NextRetryAt = pointerto.Time(time.Now().Add(backoff))
    d.db.UpdateExecution(ctx, exec)
    
    aplog.Warn("cell %s: attempt %d failed, retrying in %v: %v", 
      cell.ID, attempt, backoff, result.Error)
    
    // Loop will retry after backoff expires
    d.rescheduleCell(cell, *exec.NextRetryAt)
    return
  }
}

// Reschedule cell for later processing
func (d *Dispatcher) rescheduleCell(cell model.Cell, retryAt time.Time) {
  // Store in a "retry queue" for later
  // When cell gets re-polled after retryAt, it will try again
}
```

## Files to Create

```
src/internal/
├── db/
│   ├── schema.go          # SQLite schema + migrations
│   ├── client.go          # DB operations
│   └── execution.go       # Execution table operations (NEW)
└── daemon/
    └── retry.go           # Retry manager (NEW)
```

## Files to Modify

```
src/internal/
├── config/config.go       # Add RetryPolicy struct
├── daemon/dispatcher.go   # Add retry logic
└── model/model.go         # Add ExecutionID to RunRequest
```

## Behavior

### Example Flow

```
Task: "Fix login bug"
Configuration: max_attempts=3, exponential backoff, base=1s

Attempt 1:
  ├─ Start: 10:00:00
  ├─ Error: "timeout"
  ├─ Is retriable? YES
  ├─ Save execution: attempt=1, status=failed, next_retry=10:00:01
  └─ Wait 1s

Attempt 2 (10:00:01):
  ├─ Start: 10:00:01
  ├─ Error: "connection_error"
  ├─ Is retriable? YES
  ├─ Save execution: attempt=2, status=failed, next_retry=10:00:03
  └─ Wait 2s

Attempt 3 (10:00:03):
  ├─ Start: 10:00:03
  ├─ Output: "✓ Fixed"
  ├─ Is retriable? N/A (success)
  ├─ Save execution: attempt=3, status=success
  └─ Task complete ✓

Database shows:
  execution_1: attempt=1, failed, error="timeout"
  execution_2: attempt=2, failed, error="connection_error"
  execution_3: attempt=3, success, duration=5000ms
```

## Testing

```bash
# Test retry mechanism
go test ./internal/daemon -run TestRetryLogic

# Test backoff calculation
go test ./internal/daemon -run TestBackoffCalculation

# Test database persistence
go test ./internal/db -run TestExecutionTracking
```

## Monitoring

```bash
# View retry statistics
apiary status --retries    # Show retry queue

# View execution history
apiary logs --executions task-id

# View task attempts
sqlite3 apiary.db "SELECT attempt, status, error_message, created_at FROM task_executions WHERE task_id='abc123';"
```

## Rollout Plan

1. ✅ Add config types
2. ✅ Create SQLite schema
3. ✅ Implement DB layer
4. ✅ Implement retry manager
5. ✅ Integrate into dispatcher
6. ✅ Add tests
7. ✅ Update CLI to show retry status
8. ✅ Test with real failures

**Estimated time: 4-5 hours**
