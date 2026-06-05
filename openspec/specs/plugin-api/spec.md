# Apiary — Plugin API

Apiary is extended through two plugin types: **Source Adapters** and **Runner Adapters**. Both implement Go interfaces.

## Source Adapter

A Source Adapter connects Apiary to a task management system.

```go
// SourceAdapter pulls tasks from an external task system.
type SourceAdapter interface {
    // ID returns the adapter type key (e.g. "plane", "jira", "linear").
    ID() string

    // Connect initialises the connection using the raw config map from apiary.yaml.
    Connect(ctx context.Context, config map[string]any) error

    // Poll returns tasks matching the source's filter config since the last call.
    // Apiary calls this on the configured poll_interval.
    Poll(ctx context.Context, since time.Time) ([]Cell, error)

    // Acknowledge is called after a Cell has been dispatched to a worker.
    // Adapters use this to transition the task state or add a comment.
    Acknowledge(ctx context.Context, cell Cell, action AckAction) error

    // WriteResult posts the agent run output back to the source task.
    WriteResult(ctx context.Context, cell Cell, result RunResult) error

    // WebhookHandler returns an http.Handler for push-mode sources.
    // Return nil for poll-only adapters.
    WebhookHandler() http.Handler
}
```

### The `Cell` type

`Cell` is a normalised, source-system-agnostic task unit.

```go
type Cell struct {
    ID          string            // native task ID in the source system
    SourceID    string            // apiary source id (e.g. "main-plane")
    Title       string
    Description string            // markdown body
    Labels      []string
    Type        string            // "bug", "feature", "improvement", etc.
    Priority    string            // "urgent", "high", "medium", "low"
    State       string            // current state name in the source system
    URL         string            // direct link back to the task
    Metadata    map[string]any    // adapter-specific extra fields
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

## Runner Adapter

A Runner Adapter invokes an AI agent runner with a task's context.

```go
// RunnerAdapter executes an agent for a given Cell.
type RunnerAdapter interface {
    // ID returns the adapter type key (e.g. "opencode", "script").
    ID() string

    // Configure sets runner-level options from the worker config block.
    Configure(config map[string]any) error

    // Run executes the agent and streams progress. Blocks until completion.
    Run(ctx context.Context, req RunRequest) (RunResult, error)
}

type RunRequest struct {
    Cell         Cell
    WorkerID     string
    Model        string   // opaque model ID — passed as-is to the runner CLI
    MaxTurns     int
    SystemAppend string   // extra system prompt text from worker config
    WorkingDir   string
    Env          map[string]string
    Timeout      time.Duration
}

type RunResult struct {
    WorkerID string
    Success  bool
    Output   string        // final agent summary / last message
    Logs     []LogEntry
    Duration time.Duration
    Error    error
}
```

## Workflow Mode extensions

The engine drives tasks through multi-step workflows (a plain route is a
single-step workflow). Adapters opt into the extra capabilities through
**optional** interfaces — existing adapters keep working unchanged.

### `TaskPoller` (optional) — required to host `approval` steps

An `approval` step suspends a workflow until a human responds on the task. The
engine re-reads the live task (and its comments) once per poll cycle to decide
whether to resume or abort. A source can only host approvals if its adapter
implements `TaskPoller`; otherwise the instance stays parked.

```go
// TaskPoller fetches a single task's current state, including its comments.
// Implemented by adapters that can host approval steps.
type TaskPoller interface {
    PollTask(ctx context.Context, cellID string) (Cell, error)
}
```

`PollTask` populates `Cell.Comments` so the engine can evaluate approval
conditions (`comment_contains`, `label_added`, `state_changed`):

```go
type Comment struct {
    ID        string
    Body      string
    CreatedAt time.Time
}
// Cell gains: Comments []Comment  // populated by TaskPoller; empty otherwise
```

The built-in **GitHub** adapter implements `TaskPoller` (issue + comments).

### `RunRequest` / `RunResult` workflow fields

Agent steps reuse the same `RunnerAdapter.Run`. Workflow mode adds these fields,
all optional and ignored by runners that do not use them:

```go
// RunRequest, additional fields:
SystemPrepend      string  // workflow memory document injected ahead of the prompt
SummaryPrompt      string  // ask the agent for a short handoff note (memory baton)
StepID             string  // the workflow step id (for logging / persistence)
WorkflowInstanceID string  // the owning instance id

// RunResult, additional fields:
StructuredOutput map[string]any // parsed APIARY_OUTPUT: JSON (per output_schema)
Summary          string         // the agent's handoff summary (APIARY_SUMMARY block)
```

## Built-in Adapters (v1)

### Source Adapters

| Adapter | Type key | Trigger modes |
|---|---|---|
| Plane | `plane` | Poll + webhook |
| Jira Cloud | `jira` | Poll + webhook |
| Linear | `linear` | Poll + webhook (GraphQL) |
| GitHub Issues | `github` | Poll + webhook (GitHub App) |

### Runner Adapters

| Adapter | Type key | Notes |
|---|---|---|
| OpenCode | `opencode` | Invokes `opencode` CLI; streams stdout/stderr |
| Shell Script | `script` | Runs an arbitrary command; Cell fields injected as env vars |

## Custom Adapters (v1 — embedded)

Register custom adapters before calling `apiary.Run()`:

```go
import "github.com/orlandoburli/apiary/sdk"

func main() {
    apiary.RegisterSource(&MyCustomSource{})
    apiary.RegisterRunner(&MyCustomRunner{})
    apiary.Run()
}
```

## External Plugin Protocol (v2 — planned)

In v2, Apiary will support out-of-process plugin binaries via a gRPC-based protocol (similar to the Terraform/Vault plugin model). Plugin binaries will be discovered by name convention (`apiary-source-<type>`, `apiary-runner-<type>`) and launched as child processes with a stable gRPC interface.

This allows the community to distribute adapters as standalone binaries without forking Apiary.

## Cell Env Vars (for `script` runner)

When using the `script` runner, the following environment variables are injected into the subprocess:

| Variable | Value |
|---|---|
| `APIARY_CELL_ID` | Cell.ID |
| `APIARY_CELL_SOURCE_ID` | Cell.SourceID |
| `APIARY_CELL_TITLE` | Cell.Title |
| `APIARY_CELL_DESCRIPTION` | Cell.Description |
| `APIARY_CELL_TYPE` | Cell.Type |
| `APIARY_CELL_PRIORITY` | Cell.Priority |
| `APIARY_CELL_URL` | Cell.URL |
| `APIARY_CELL_LABELS` | comma-separated labels |
| `APIARY_WORKER_ID` | RunRequest.WorkerID |
| `APIARY_MODEL` | RunRequest.Model |
| `APIARY_WORKING_DIR` | RunRequest.WorkingDir |
