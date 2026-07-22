# Apiary — Plugin API

Apiary's built-in extensions implement Go interfaces. Third-party extensions use
the versioned out-of-process protocol documented below and are adapted to the
same internal contracts.

## Source Adapter

A Source Adapter connects Apiary to a task management system.

> **Naming note:** the normalised task unit was renamed from `Cell` to
> `SourceItem` in the internal-task-model change. A `SourceItem` represents a raw
> item in the source system; once polled it is bound to a canonical
> **InternalTask** by the `SourceBinder` (below). Older field/variable names like
> `RunRequest.Cell` are retained, but their type is now `SourceItem`.

```go
// Adapter pulls tasks from an external task system.
type Adapter interface {
    // ID returns the adapter type key (e.g. "plane", "jira", "linear").
    ID() string

    // Connect initialises the connection using the raw config map from apiary.yaml.
    Connect(ctx context.Context, config map[string]any) error

    // Poll returns tasks matching the source's filter config since the last call.
    // Apiary calls this on the configured poll_interval.
    Poll(ctx context.Context, since time.Time) ([]SourceItem, error)

    // Acknowledge is called after a SourceItem has been dispatched to a worker.
    // Adapters use this to transition the task state or add a comment.
    Acknowledge(ctx context.Context, item SourceItem, action AckAction) error

    // WriteResult posts the agent run output back to the source task.
    WriteResult(ctx context.Context, item SourceItem, result RunResult) error

    // WebhookHandler returns an http.Handler for push-mode sources.
    // Return nil for poll-only adapters.
    WebhookHandler() http.Handler
}
```

### The `SourceItem` type

`SourceItem` is a normalised, source-system-agnostic task unit (formerly `Cell`).

```go
type SourceItem struct {
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

### The `SourceBinder` (binding layer)

A `SourceBinder` normalises a polled `SourceItem` into the canonical
**InternalTask** the engine runs on, creating a `SourceBinding` row on first sight
and returning the existing task on every subsequent poll. This is what lets one
internal task carry many source bindings, and lets spawned (binding-less) tasks
exist with no source item at all. The default binder is built in; adapters do not
implement it.

```go
// SourceBinder maps a SourceItem to its InternalTask (find-or-create).
type SourceBinder interface {
    // Bind returns the InternalTask for the given SourceItem, creating it (and
    // its SourceBinding) on first sight and returning the existing one on every
    // subsequent poll.
    Bind(ctx context.Context, item SourceItem) (InternalTask, error)
}

// InternalTask is the canonical, source-independent unit of work.
type InternalTask struct {
    ID                   string         // sortable id
    ParentTaskID         string         // set for spawned tasks (lineage); empty for roots
    Title                string
    Description          string
    Input                map[string]any // structured input from the spawner; nil for source-bound tasks
    State                string         // registered | running | approval_waiting | done | failed
    Metadata             TaskMetadata   // labels, priority, type, source, live source state
    OutstandingWorkflows int            // workflows still running; completion hook fires at 0
    CreatedAt, UpdatedAt time.Time
}

// SourceBinding links a source item to an InternalTask (one task → many bindings).
type SourceBinding struct {
    ID               string
    TaskID           string // references InternalTask.ID
    SourceID         string // e.g. "github", "plane"
    SourceItemID     string // source-native id
    SourceItemURL    string
    SourceItemNumber string // human ref, e.g. "#42", "ERP-42"
    CreatedAt        time.Time
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
    PollTask(ctx context.Context, sourceItemID string) (SourceItem, error)
}
```

`PollTask` populates `SourceItem.Comments` so the engine can evaluate approval
conditions (`comment_contains`, `label_added`, `state_changed`):

```go
type Comment struct {
    ID        string
    Body      string
    CreatedAt time.Time
}
// SourceItem gains: Comments []Comment  // populated by TaskPoller; empty otherwise
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
PublishPayload   string         // APIARY_PUBLISH block; written back per step.publish
SpawnRequest     *SpawnRequest  // APIARY_SPAWN request {workflow,title,input}; drives internal fan-out
```

(`RunRequest.Cell` is a `SourceItem`.) The engine parses the `APIARY_PUBLISH` and
`APIARY_SPAWN` markers out of the agent's raw output; see the marker reference in
the config schema spec.

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

## External Plugin Protocol (manifest 1 / protocol 1)

External plugins are installed as directories containing `apiary-plugin.json`
and a relative executable. The manifest declares semantic plugin and Apiary
compatibility versions, capabilities, configuration JSON Schema, protocol, and
security requirements. Supported capability IDs are `source`, `runner`,
`workflow_action`, `approval_provider`, `secret_provider`, and `event_exporter`.

Protocol 1 uses one newline-delimited JSON request and response over child-process
stdin/stdout. Apiary starts a fresh process per invocation, enforces a deadline
and output bounds, forwards only declared secret environment variables, and
reports crash/protocol/timeout errors without crashing the dispatcher. Discovery
and validation never execute plugin code.

The first runtime proxy is `event_exporter`, method `export`, which receives the
persisted and redacted execution-event envelope. The other capability IDs reserve
the same manifest and transport boundary for adapters to the Go contracts above.
See `docs/plugins.md` for the normative field and envelope definitions.

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
