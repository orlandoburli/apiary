# Apiary — Plugin API

Apiary is extended through two plugin types: **Source Adapters** and **Runner Adapters**. Both implement simple Go interfaces.

## Source Adapter

A Source Adapter connects Apiary to a task management system.

```go
// SourceAdapter pulls tasks from an external system.
type SourceAdapter interface {
    // ID returns the adapter type name (e.g. "plane", "jira").
    ID() string

    // Connect initialises the connection using the raw config map from apiary.yaml.
    Connect(ctx context.Context, config map[string]any) error

    // Poll returns tasks matching the source's filter config since the last call.
    // Apiary calls this on the configured poll_interval.
    Poll(ctx context.Context, since time.Time) ([]Cell, error)

    // Acknowledge is called after a Cell has been dispatched.
    // Adapters use this to move the task to "in progress" or add a comment.
    Acknowledge(ctx context.Context, cell Cell, action AckAction) error

    // WriteResult posts the agent output back to the task (comment, state change, etc).
    WriteResult(ctx context.Context, cell Cell, result RunResult) error

    // WebhookHandler returns an http.Handler for push-mode sources, or nil for poll-only.
    WebhookHandler() http.Handler
}
```

### The `Cell` type

```go
// Cell is a normalised task unit, source-system-agnostic.
type Cell struct {
    ID          string            // source-system native ID
    SourceID    string            // apiary source id ("main-plane")
    Title       string
    Description string            // markdown body
    Labels      []string
    Type        string            // "bug", "feature", "improvement", etc.
    Priority    string            // "urgent", "high", "medium", "low"
    State       string            // current state name in source system
    URL         string            // link back to source task
    Metadata    map[string]any    // adapter-specific extra fields
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

## Runner Adapter

A Runner Adapter invokes an AI agent with a task's context.

```go
// RunnerAdapter executes an agent for a given Cell.
type RunnerAdapter interface {
    // ID returns the adapter type name (e.g. "claude-code", "opencode").
    ID() string

    // Configure sets runner-level options from the worker config block.
    Configure(config map[string]any) error

    // Run executes the agent and streams progress. Blocks until done.
    Run(ctx context.Context, req RunRequest) (RunResult, error)
}

type RunRequest struct {
    Cell        Cell
    WorkerID    string
    Model       string
    MaxTurns    int
    SystemAppend string   // extra system prompt text from worker config
    WorkingDir  string
}

type RunResult struct {
    WorkerID  string
    Success   bool
    Output    string        // final agent message / summary
    Logs      []LogEntry
    Duration  time.Duration
    Error     error
}
```

## Built-in Adapters (v1)

### Sources

| Adapter | Type key | Notes |
|---|---|---|
| Plane | `plane` | REST API + webhook support |
| Jira Cloud | `jira` | REST API v3; webhook support |
| Linear | `linear` | GraphQL API; webhook support |
| GitHub Issues | `github` | REST API; webhook via GitHub App |

### Runners

| Adapter | Type key | Notes |
|---|---|---|
| Claude Code CLI | `claude-code` | Invokes `claude` CLI; streams output |
| OpenCode | `opencode` | Invokes `opencode` CLI; streams output |
| Shell Script | `script` | Runs an arbitrary shell command with Cell data as env vars |

## Custom Adapters (v2)

In v2, Apiary will support external plugin binaries via a gRPC-based plugin protocol (similar to Terraform/Vault plugin model). Until then, custom adapters are registered by importing the `apiary/sdk` Go package and calling `apiary.RegisterSource` / `apiary.RegisterRunner` before starting the server.

```go
// Example: registering a custom source in a fork/extension
func main() {
    apiary.RegisterSource(&MyCustomSource{})
    apiary.RegisterRunner(&MyCustomRunner{})
    apiary.Run()
}
```
