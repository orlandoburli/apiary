# Design: Internal Task Model

## Architecture Overview

The new layered model introduces InternalTask as the central unit. Sources are binders on the left edge; write-back targets on the right. Workflows can spawn new InternalTasks internally, creating a lineage tree independent of source systems.

```mermaid
flowchart TB
    subgraph SOURCES["Sources — Binders"]
        GH[GitHub Issues]
        JI[Jira Tickets]
        LM[Log Monitor]
    end

    subgraph BINDING["Binding Layer"]
        AD["Adapter — Poll to SourceItem"]
        SB["SourceBinder<br/>FindOrCreate InternalTask"]
        BDB[(source_bindings)]
    end

    subgraph CORE["Core — Internal"]
        IT[InternalTask]
        TDB[(internal_tasks)]
        RO["Router<br/>matches all triggers"]
        SP["WorkflowSpawner<br/>APIARY_SPAWN handler"]
    end

    subgraph EXECUTION["Execution Layer"]
        WF_A[Workflow A]
        WF_B[Workflow B]
        ENG["WorkflowEngine<br/>DAG · steps · approvals"]
    end

    subgraph WRITEBACK["Write-back"]
        PUB["PublishQueue<br/>APIARY_PUBLISH"]
        HOOK["TaskCompletionHook<br/>on_complete · on_fail"]
        TC["Agent Tool Calls<br/>create issue · create task"]
    end

    GH -->|Poll| AD
    JI -->|Poll| AD
    LM -->|Poll| AD
    AD --> SB
    SB -->|writes| BDB
    SB -->|creates or resolves| IT
    IT -->|persisted in| TDB
    IT --> RO
    RO -->|fan-out| WF_A
    RO -->|fan-out| WF_B
    WF_A --> ENG
    WF_B --> ENG
    ENG -->|APIARY_SPAWN| SP
    SP -->|creates child InternalTask| IT
    ENG -->|APIARY_PUBLISH| PUB
    ENG -->|all workflows done| HOOK
    ENG -->|source tool calls| TC
    PUB -->|WriteResult| GH
    PUB -->|WriteResult| JI
    HOOK -->|SetState AddLabels| GH
    HOOK -->|SetState AddLabels| JI
    TC -->|creates items| GH
    TC -->|creates items| JI
```

---

## Task Creation Paths

There are two ways an InternalTask is created. Both enter the same Router → WorkflowEngine pipeline.

```mermaid
flowchart LR
    subgraph PATH_A["Path A — Source-bound"]
        SA["Source Adapter<br/>Poll"] --> SI[SourceItem]
        SI --> SB2["SourceBinder<br/>FindOrCreate"]
        SB2 --> IT_A["InternalTask<br/>has SourceBinding"]
    end

    subgraph PATH_B["Path B — Spawned"]
        WF["Running Workflow Step"] -->|"APIARY_SPAWN marker"| SP2["WorkflowSpawner"]
        SP2 --> IT_B["InternalTask<br/>parent_task_id set<br/>no SourceBinding"]
    end

    IT_A --> RO2[Router]
    IT_B --> RO2
    RO2 --> ENG2[WorkflowEngine]
```

---

## Binding Flow

How a source item becomes an InternalTask (Path A).

```mermaid
sequenceDiagram
    participant Src as Source Adapter
    participant Binder as SourceBinder
    participant DB as Database
    participant Router
    participant Dispatcher

    Src->>Binder: SourceItem from Poll
    Binder->>DB: lookup source_bindings by source_id and source_item_id
    alt binding exists
        DB-->>Binder: existing task_id
        Binder->>DB: SELECT internal_tasks by id
        DB-->>Binder: InternalTask
    else new item
        Binder->>DB: INSERT internal_tasks
        DB-->>Binder: new InternalTask state=registered
        Binder->>DB: INSERT source_bindings
    end
    Binder->>Router: RouteAll InternalTask
    Router-->>Binder: list of WorkflowMatches
    loop each WorkflowMatch
        Binder->>Dispatcher: Dispatch task and match
    end
```

---

## Spawn Flow

How a workflow step creates a child InternalTask (Path B).

```mermaid
sequenceDiagram
    participant Agent
    participant Engine as WorkflowEngine
    participant Spawner as WorkflowSpawner
    participant DB as Database
    participant Router
    participant Dispatcher

    Agent->>Engine: step output with APIARY_SPAWN marker
    Engine->>Spawner: SpawnRequest: workflow + title + input JSON
    Spawner->>DB: INSERT internal_tasks with parent_task_id and input
    DB-->>Spawner: child InternalTask state=registered
    Spawner->>Router: RouteAll child task
    Router-->>Spawner: WorkflowMatch for named workflow
    Spawner->>Dispatcher: Dispatch child task and match
    Spawner-->>Engine: spawn acknowledged
    Engine->>Engine: step continues — fire and forget by default
```

---

## Fan-out Dispatch

One InternalTask triggers N workflows. Each runs independently; the task tracks how many are outstanding.

```mermaid
flowchart LR
    IT["InternalTask<br/>state: registered"]

    IT --> R{"Router<br/>evaluates ALL triggers"}

    R -->|"p=100 exclusive=false"| WF_A["code-review"]
    R -->|"p=200 exclusive=false"| WF_B["docs-update"]
    R -->|"p=300 exclusive=true"| WF_C["security-scan"]

    WF_A --> RUN_A["RunInstance A<br/>running"]
    WF_B --> RUN_B["RunInstance B<br/>running"]
    WF_C --> RUN_C["RunInstance C<br/>running"]

    RUN_A -->|done| TRACK{"outstanding = 0?"}
    RUN_B -->|done| TRACK
    RUN_C -->|done| TRACK

    TRACK -->|yes| HOOK[TaskCompletionHook]
    TRACK -->|no| WAIT[wait]
```

**Exclusive flag semantics:**

```mermaid
flowchart TD
    T["Evaluate triggers<br/>ordered by priority"]
    T --> M{"trigger matches?"}
    M -->|no| NEXT[next trigger]
    M -->|yes| DISPATCH[dispatch workflow]
    DISPATCH --> EX{"exclusive: true?"}
    EX -->|yes| STOP["stop — no more triggers"]
    EX -->|no| NEXT
    NEXT --> M
```

---

## Per-Step Write-back

Write-back is agent-driven. The agent emits an `APIARY_PUBLISH` block; the engine fans it out to every SourceBinding. If the task has no bindings the marker is silently ignored.

```mermaid
sequenceDiagram
    participant Agent
    participant Engine as WorkflowEngine
    participant PQ as PublishQueue
    participant DB
    participant Src as Source Adapters

    Agent->>Engine: step output
    Engine->>Engine: parse APIARY_PUBLISH marker
    alt marker found
        Engine->>DB: SELECT source_bindings by task_id
        DB-->>Engine: list of SourceBindings
        alt bindings exist
            loop each binding
                Engine->>PQ: enqueue binding and payload
            end
            PQ->>Src: WriteResult with payload
        else no bindings
            Engine->>Engine: silently ignore
        end
    else no marker
        Engine->>Engine: no write-back
    end
```

**Step config — publish control:**

```mermaid
flowchart LR
    SC["StepConfig<br/>publish: auto or off"]
    SC -->|auto| AGT{"Agent emits<br/>APIARY_PUBLISH?"}
    SC -->|off| SKIP["skip write-back"]
    AGT -->|yes| WB[write-back fired]
    AGT -->|no| NOP[no write-back]
```

---

## Source-mediated Fan-out vs Internal Spawn

```mermaid
flowchart TB
    TW["Triage Workflow<br/>running on InternalTask T1"]

    TW -->|"Route A:<br/>add label workflow:engineer<br/>via tool call"| SRC["Source System<br/>GitHub or Jira"]
    SRC -->|"next Poll cycle"| SB3["SourceBinder<br/>new SourceItem found"]
    SB3 --> T2["InternalTask T2<br/>SourceBinding to child issue"]
    T2 --> EW["Engineer Workflow"]

    TW -->|"Route B:<br/>APIARY_SPAWN marker"| SP3["WorkflowSpawner"]
    SP3 --> T3["InternalTask T3<br/>parent = T1<br/>no SourceBinding"]
    T3 --> CW["Collect Workflow"]
```

**When to use each:**

| | Source-mediated | APIARY_SPAWN |
|---|---|---|
| Downstream work needs a ticket | yes | no |
| No source system at the top | no | yes |
| Incident or log-driven chains | no | yes |
| Staff creating Jira stories | yes | no |
| Collect workflow before Staff | no | yes |

---

## Task Lineage

The Incident → Collect → Staff → Fix chain as a lineage tree.

```mermaid
flowchart TB
    L["Log event<br/>source: log-monitor"]
    L --> INC["InternalTask: Incident<br/>source binding: log-monitor"]
    INC -->|"APIARY_SPAWN"| COL["InternalTask: Collect<br/>parent: Incident<br/>no binding"]
    COL -->|"APIARY_SPAWN"| STA["InternalTask: Staff<br/>parent: Collect<br/>no binding"]
    STA -->|"tool call: create issue"| FA["InternalTask: Fix A<br/>source binding: github"]
    STA -->|"tool call: create task"| FB["InternalTask: Fix B<br/>source binding: jira"]
    STA -->|"tool call: create task"| FC["InternalTask: Fix C<br/>source binding: jira"]
    FA --> EW_A["Engineer Workflow"]
    FB --> EW_B["Engineer Workflow"]
    FC --> EW_C["Engineer Workflow"]
```

---

## InternalTask State Machine

```mermaid
stateDiagram-v2
    [*] --> registered: SourceBinder creates or Spawner creates

    registered --> running: first workflow dispatched

    running --> running: fan-out workflow added

    running --> approval_waiting: step parks for approval

    approval_waiting --> running: approval granted or timed out

    running --> done: last outstanding workflow succeeds

    running --> failed: workflow fails with no retry

    done --> [*]
    failed --> [*]
```

---

## Data Model

### New Tables

```sql
-- Canonical internal task registry
CREATE TABLE internal_tasks (
    id                    TEXT PRIMARY KEY,     -- ulid
    parent_task_id        TEXT REFERENCES internal_tasks(id),  -- set for spawned tasks
    title                 TEXT NOT NULL,
    description           TEXT,
    input                 TEXT,                 -- JSON: structured input from spawner
    state                 TEXT NOT NULL DEFAULT 'registered',
    -- 'registered' | 'running' | 'approval_waiting' | 'done' | 'failed'
    metadata              TEXT,                 -- JSON: labels, priority, type, etc.
    outstanding_workflows INTEGER DEFAULT 0,
    created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Links source items to InternalTasks (one-to-many, optional)
CREATE TABLE source_bindings (
    id                 TEXT PRIMARY KEY,        -- ulid
    task_id            TEXT NOT NULL REFERENCES internal_tasks(id),
    source_id          TEXT NOT NULL,           -- e.g. "github", "jira"
    source_item_id     TEXT NOT NULL,           -- source-native item ID
    source_item_url    TEXT,                    -- deep-link for display
    source_item_number TEXT,                    -- human ref: "#42", "ERP-42"
    created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(source_id, source_item_id)
);
```

### Modified Tables

```sql
-- workflow_instances: replace source_item_id+source_id with task_id
ALTER TABLE workflow_instances
    ADD COLUMN task_id TEXT REFERENCES internal_tasks(id);
-- source_item_id and source_id retained for backward compat during migration, then dropped

-- step_runs: add publish and spawn tracking
ALTER TABLE step_runs
    ADD COLUMN publish_payload TEXT,            -- extracted APIARY_PUBLISH content, if any
    ADD COLUMN publish_state   TEXT,            -- null | 'queued' | 'sent' | 'failed'
    ADD COLUMN spawned_task_id TEXT REFERENCES internal_tasks(id);  -- if step emitted APIARY_SPAWN
```

### Dropped (after migration)

```sql
-- workflow_instances.source_item_id  → replaced by task_id
-- workflow_instances.source_id       → resolved via source_bindings
```

---

## Go Types

```go
// internal/model/task.go

type TaskState string

const (
    TaskStateRegistered   TaskState = "registered"
    TaskStateRunning      TaskState = "running"
    TaskStateApprovalWait TaskState = "approval_waiting"
    TaskStateDone         TaskState = "done"
    TaskStateFailed       TaskState = "failed"
)

type InternalTask struct {
    ID                   string
    ParentTaskID         string            // empty if root task
    Title                string
    Description          string
    Input                map[string]any    // structured input from spawner, nil for source-bound tasks
    State                TaskState
    Metadata             TaskMetadata
    OutstandingWorkflows int
    CreatedAt            time.Time
    UpdatedAt            time.Time
}

type TaskMetadata struct {
    Labels   []string
    Priority string
    Type     string     // "issue", "work_item", "log_event", "internal", ...
}

type SourceBinding struct {
    ID               string
    TaskID           string
    SourceID         string
    SourceItemID     string
    SourceItemURL    string
    SourceItemNumber string
    CreatedAt        time.Time
}
```

```go
// internal/source/binder.go

type SourceBinder interface {
    // Bind normalizes a SourceItem into an InternalTask, creates a SourceBinding
    // if one does not exist yet, and returns the task (new or existing).
    Bind(ctx context.Context, item model.SourceItem) (model.InternalTask, error)
}
```

```go
// internal/workflow/spawner.go

type SpawnRequest struct {
    ParentTaskID string
    WorkflowID   string
    Title        string
    Input        map[string]any
}

type WorkflowSpawner interface {
    Spawn(ctx context.Context, req SpawnRequest) (model.InternalTask, error)
}
```

```go
// internal/config/config.go — additions

type TriggerConfig struct {
    Priority  int        `yaml:"priority"`
    Exclusive bool       `yaml:"exclusive"` // stops evaluating further triggers
    Match     RouteMatch `yaml:"match"`
}

type StepConfig struct {
    // ... existing fields ...
    Publish string `yaml:"publish"` // "auto" (default) | "off"
    Spawn   string `yaml:"spawn"`   // "auto" (default, fire-and-forget) | "await"
}

type TasksConfig struct {              // top-level tasks: block
    OnComplete *OnComplete `yaml:"on_complete"`
    OnFail     *OnComplete `yaml:"on_fail"`
}
```

---

## Affected Components

| Component | Change |
|---|---|
| `internal/source/binder.go` | **New.** SourceBinder; replaces inline SourceItem→dispatch in daemon |
| `internal/workflow/spawner.go` | **New.** WorkflowSpawner; handles APIARY_SPAWN, creates child InternalTasks |
| `internal/model/task.go` | **New.** InternalTask, SourceBinding, TaskMetadata types |
| `internal/model/source_item.go` | **Renamed from cell.go.** SourceItem stays within binding layer only |
| `internal/router/router.go` | **Changed.** `Route(SourceItem)` → `RouteAll(InternalTask) []Match` |
| `internal/daemon/dispatcher.go` | **Changed.** Calls binder, passes task to RouteAll, fans out dispatches |
| `internal/workflow/engine.go` | **Changed.** Accepts InternalTask; parses APIARY_SPAWN and APIARY_PUBLISH markers; resolves bindings for write-back |
| `internal/daemon/workflow.go` | **Changed.** SideEffects resolves adapter via SourceBinding, not SourceItem.SourceID |
| `internal/db/schema.go` | **Changed.** New tables; migration for workflow_instances |
| `internal/config/config.go` | **Changed.** TriggerConfig.Exclusive, StepConfig.Publish, StepConfig.Spawn, new TasksConfig |
| `apps/dashboard` | **Changed.** Task view shows lineage tree, SourceBindings, and WorkflowInstances per task |

---

## Open Questions

1. **Multi-source binding**: today a task has one binding (one source item). The schema supports many. Should the binder ever merge two source items into one InternalTask? Deferred.

2. **Await semantics for spawn**: when `spawn: await`, the spawning step blocks until the child task reaches a terminal state. If the child fails, does the parent step fail too? Proposed default: yes (propagate failure). Deferred to implementation.

3. **Per-binding publish**: currently `APIARY_PUBLISH` fans out to all bindings equally. A future `APIARY_PUBLISH[github]` syntax could target a specific source. Deferred.

4. **Tool-created source items and lineage**: when an agent creates a GitHub issue via tool call, there is no automatic link back to the parent task. The spawned InternalTask only gets its SourceBinding when the new issue is polled and bound. A future `APIARY_BIND` marker or tool return hook could accelerate this. Deferred.
