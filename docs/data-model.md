# Data Model

Apiary persists its state in a single SQLite database. The schema is defined in
`src/internal/db/schema.go` (base `CREATE TABLE` statements plus idempotent
`ALTER` migrations). This page is the canonical map of the tables and how they
relate.

## Entity-relationship diagram

```mermaid
erDiagram
    agents ||--o{ tasks : "runs"
    agents ||--o{ task_executions : "executes"
    tasks ||--o{ task_executions : "attempts"
    tasks ||--o{ task_checkpoints : "checkpoints"
    tasks ||--o{ task_logs : "logs"

    internal_tasks ||--o{ internal_tasks : "spawns (parent_task_id)"
    internal_tasks ||--o{ source_bindings : "bound to source items"
    internal_tasks ||--o{ workflow_instances : "drives"
    workflow_instances ||--o{ workflow_instances : "sub-workflow (parent_instance_id)"
    workflow_instances ||--o{ step_runs : "steps"
    workflow_instances ||--o{ task_executions : "step attempt (instance_id, step_id)"
    workflow_instances ||--o{ ci_poll_checks : "wait_for CI polls"
    step_runs ||--o| internal_tasks : "APIARY_SPAWN (spawned_task_id)"

    agents {
        TEXT id PK
        TEXT description
        TEXT status "active|idle|error"
        TEXT current_task_id
        INTEGER queued_count
        INTEGER total_completed
        INTEGER avg_duration_ms
        REAL success_rate
        TIMESTAMP last_task_ended_at
        TIMESTAMP updated_at
    }

    tasks {
        TEXT id PK "legacy dashboard history; keyed by source cell id"
        TEXT source_id
        TEXT title
        TEXT agent_id FK
        TEXT state "pending|running|completed|failed"
        TIMESTAMP started_at
        TIMESTAMP completed_at
        INTEGER duration_ms
        BOOLEAN success
        TEXT output
        TEXT full_output
        TEXT error_message
    }

    task_executions {
        INTEGER id PK
        TEXT task_id FK "-> tasks.id (cell id)"
        TEXT agent_id FK
        TEXT title
        TEXT task_number
        TEXT task_url
        TEXT model
        TEXT runner "cli|script"
        INTEGER attempt "one row per invocation/failover"
        TEXT status "pending|running|success|failed"
        TIMESTAMP started_at
        TIMESTAMP completed_at
        INTEGER duration_ms
        TEXT error_message
        BOOLEAN can_retry
        TIMESTAMP next_retry_at
        INTEGER pid
        TIMESTAMP heartbeat_at
        INTEGER heartbeat_count
        INTEGER input_tokens
        INTEGER output_tokens
        INTEGER total_tokens
        INTEGER num_turns
        INTEGER num_tool_calls
        REAL cost_usd
        TEXT workflow_instance_id "soft link -> workflow_instances.id"
        TEXT step_id "soft link -> step config id"
        TEXT input_prompt
        TEXT output_text
    }

    workflow_instances {
        TEXT id PK
        TEXT workflow_id
        TEXT cell_id
        TEXT source_id
        TEXT state "pending|running|approval_waiting|interrupted|done|failed"
        TEXT parent_instance_id "sub-workflow child"
        TEXT resumed_from
        TEXT task_id FK "-> internal_tasks.id"
        TIMESTAMP created_at
        TIMESTAMP updated_at
    }

    step_runs {
        TEXT id PK
        TEXT workflow_instance_id FK
        TEXT step_id "workflow step config id"
        TEXT agent_id
        TEXT state "pending|running|passed|failed|skipped|skipped_cached"
        TEXT output "agent output (output prompt)"
        TEXT structured_output "JSON (APIARY_OUTPUT)"
        TEXT summary
        INTEGER exit_code
        BOOLEAN skipped_cached
        TEXT publish_payload "APIARY_PUBLISH"
        TEXT publish_state "sent|failed|skipped"
        TEXT spawned_task_id FK "-> internal_tasks.id"
        TEXT input_prompt
        INTEGER input_tokens "summed over failover attempts"
        INTEGER output_tokens
        INTEGER total_tokens
        INTEGER num_turns
        INTEGER num_tool_calls
        REAL cost_usd
        INTEGER time_thinking_ms "wall-clock attribution, summed"
        INTEGER time_writing_ms
        INTEGER time_model_ms "latency with no thinking signal"
        INTEGER time_tool_wait_ms
        INTEGER time_other_ms
        INTEGER time_background_ms "overlaps the buckets above"
        TEXT slow_tools "JSON: slowest calls"
        TIMESTAMP started_at
        TIMESTAMP finished_at
    }

    ci_poll_checks {
        INTEGER id PK
        TEXT workflow_instance_id FK "-> workflow_instances.id"
        TEXT step_id "wait_for step config id"
        TEXT status "passed|failed|pending|timeout|error|unknown"
        TEXT pr_url
        TEXT detail "JSON of per-check states, or error message"
        TIMESTAMP checked_at "one row per poll"
    }

    internal_tasks {
        TEXT id PK "ulid; canonical unit of work"
        TEXT parent_task_id FK "lineage (self)"
        TEXT title
        TEXT description
        TEXT input "JSON from spawner"
        TEXT dedup_key "idempotent spawn: UNIQUE(parent_task_id, dedup_key)"
        TEXT state "registered|running|approval_waiting|done|failed"
        TEXT metadata "JSON: labels, priority, type"
        INTEGER outstanding_workflows
        TIMESTAMP created_at
        TIMESTAMP updated_at
    }

    source_bindings {
        TEXT id PK "ulid"
        TEXT task_id FK "-> internal_tasks.id"
        TEXT source_id "github|plane"
        TEXT source_item_id "UNIQUE(source_id, source_item_id)"
        TEXT source_item_url
        TEXT source_item_number "#42|ERP-42"
        TIMESTAMP created_at
    }

    task_checkpoints {
        INTEGER id PK
        TEXT task_id FK
        INTEGER attempt
        TEXT stage "initialized|running|completed"
        TEXT metadata "JSON"
        TIMESTAMP created_at
    }

    task_logs {
        INTEGER id PK
        TEXT task_id FK
        TEXT level "DEBUG|INFO|WARN|ERROR"
        TEXT message
        TIMESTAMP timestamp
    }
```

Two standalone tables carry no foreign keys and are omitted from the graph:

- **`service_logs`** — `id`, `level`, `message`, `component`, `timestamp`
- **`dispatcher_state`** — `id`, `status`, `uptime_seconds`, `version`, `updated_at`

## Reading the model

### Two parallel "task" concepts

There are two task tables, which reflects the in-progress internal-task-model
migration (not an accident):

- **`tasks`** — the legacy dashboard history table, keyed by source cell id.
  This is what `task_executions.task_id` actually references.
- **`internal_tasks`** — the canonical, source-independent unit of work, with
  lineage (`parent_task_id`), source bindings, and the workflow instances it
  drives.

### Execution vs. step run

`task_executions` and `step_runs` describe the same work at different
granularities, linked by a **soft association** (not a database foreign key):

- A **`step_runs`** row is one logical step of a workflow instance.
- A **`task_executions`** row is one runner *invocation*. A single step can
  produce several execution rows when the primary runner is rate-limited and
  fails over to a fallback.
- The two are matched by `task_executions.workflow_instance_id` +
  `task_executions.step_id` against the owning `step_runs` row.

### Token usage, cost, and prompts

Per-step cost/usage detail is recorded in **both** layers:

- **`task_executions`** carries per-invocation timing, token counts
  (`input_tokens`/`output_tokens`/`total_tokens`), `cost_usd`, and the
  `input_prompt` / `output_text` of that attempt. Because there is one row per
  invocation, failovers stay distinct.
- **`step_runs`** carries the same token/cost columns as a **rollup summed
  across the step's failover attempts**, plus the `input_prompt` of the winning
  attempt and the agent `output`. This is the authoritative per-step total the
  dashboard displays.

The cost figure originates from the harness: the CLI runner parses the model's
streamed `total_cost_usd` and token counts — it is reported, not estimated.

### Wall-clock attribution

Alongside the token columns, both layers record **where a step's minutes went**.
Agent steps routinely run 45–90 minutes, and tokens alone cannot tell you whether
that time was thinking, writing, or waiting on a subprocess the agent launched —
three problems with three completely different fixes.

| Column | Meaning |
|---|---|
| `time_thinking_ms` | Model producing thinking tokens |
| `time_writing_ms` | Model producing its visible output |
| `time_model_ms` | Model latency with no thinking signal to split on — **un-attributed**, not a third kind of model work |
| `time_tool_wait_ms` | Blocked on a tool call the agent made |
| `time_other_ms` | Process spawn, prompt upload, teardown |
| `time_background_ms` | At least one background task outstanding |
| `slow_tools` | JSON list of the slowest individual calls |

The first five are **exclusive**: every instant of wall clock is counted in
exactly one of them, and they sum to the run's total. `time_background_ms` is
**not** one of them — it is the union of the intervals with background work
outstanding, and it overlaps the others by design (the model writes while a test
suite runs), so it must never be added in.

Two consequences worth knowing:

- **Per-call durations in `slow_tools` overlap freely.** Parallel tool calls and
  concurrent background tasks are each measured in full, so those durations can
  add up to more than the step took. That is correct: the list answers "which
  call should I go and fix", not "how was the wall clock divided".
- **Zeros are ambiguous without checking.** Rows written before these columns
  existed, and runners with no event stream to attribute (the API-based ones),
  leave every bucket at zero. `apiary profile` reports those steps as *not
  measured* rather than as a breakdown of zeros.

The thinking/writing split comes from the CLI's `system:thinking_tokens` events
and from the separate `assistant` message a turn's thinking arrives in, ahead of
the message carrying the answer. Those signals are emitted for some thinking and
not all, and not at all by some providers; when they are missing the latency is
reported as `time_model_ms` rather than being guessed into one bucket or the
other.

A background task the agent launched through a tool — a `Task` subagent, a
backgrounded shell — is reported twice by the provider: once as the foreground
tool call and once as a background bookend. The two are correlated by tool-use id
and collapse into a single entry in `slow_tools`, so one piece of work does not
read as two separate problems.

### CI poll history (wait_for steps)

A `wait_for` step does not get a `step_runs` row — it parks the workflow
instance (`workflow_instances.state = 'waiting'`) and is re-evaluated each poll
cycle. Every one of those polls is appended to **`ci_poll_checks`**: one row per
poll with its `status` (passed/failed/pending/timeout/error/unknown), the PR
`pr_url`, the per-check `detail` (JSON), and `checked_at`. This makes a parked CI
wait auditable — how many times it polled, when, and what each poll returned —
which the dashboard surfaces in the task detail and history views.
