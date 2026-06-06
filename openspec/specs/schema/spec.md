# Apiary — Config Schema

The `apiary.yaml` file is the single source of truth for an Apiary instance.

## Full Example

```yaml
version: "1"

# ── Sources ────────────────────────────────────────────────────
sources:
  - id: main-plane
    type: plane
    config:
      workspace: my-workspace
      project: my-project
      api_key: ${PLANE_API_KEY}
    poll_interval: 60s
    filters:
      states: [backlog, todo]
      labels: [ai-ready]

  - id: main-jira
    type: jira
    config:
      host: https://myorg.atlassian.net
      email: ${JIRA_EMAIL}
      api_token: ${JIRA_TOKEN}
      project_key: MYPROJ
    filters:
      jql: 'status in ("To Do") AND labels = "ai-ready"'

# ── Workers ────────────────────────────────────────────────────
workers:
  - id: backend-dev
    description: "Go backend tasks — bugs, features, refactors"
    runner: opencode
    model: openai/gpt-4o
    config:
      working_dir: /workspace/my-project
      max_turns: 10
      system_prompt_append: |
        You are working in a Go + PostgreSQL backend.
        Run impact analysis before modifying any symbol.

  - id: frontend-dev
    description: "TypeScript/React frontend tasks"
    runner: opencode
    model: deepseek/deepseek-r1
    config:
      working_dir: /workspace/my-project
      max_turns: 8

  - id: docs-writer
    description: "Documentation, changelog, spec writing"
    runner: opencode
    model: mistral/mistral-large-2411
    config:
      working_dir: /workspace/my-project
      max_turns: 5

  - id: code-reviewer
    description: "PR review and security checks"
    runner: opencode
    model: openai/gpt-4o
    config:
      working_dir: /workspace/my-project
      max_turns: 3
      system_prompt_append: |
        Review for correctness, security, and performance. Be terse.

# ── Routes ─────────────────────────────────────────────────────
# Rules evaluated top-to-bottom; first match wins.
routes:
  - id: backend-bugs
    priority: 10
    match:
      source: main-plane
      labels: [backend, bug]
    worker: backend-dev
    on_complete:
      set_state: in_review

  - id: frontend-features
    priority: 20
    match:
      source: main-plane
      labels: [frontend]
      types: [feature, improvement]
    worker: frontend-dev
    on_complete:
      set_state: in_review

  - id: docs-tasks
    priority: 30
    match:
      labels: [docs, documentation]
    worker: docs-writer
    on_complete:
      set_state: done

  - id: pr-reviews
    priority: 40
    match:
      source: main-plane
      labels: [review]
    worker: code-reviewer

  - id: default-backend
    priority: 99
    match:
      source: main-plane
    worker: backend-dev

# ── Global Settings ────────────────────────────────────────────
settings:
  concurrency: 3
  log_level: info
  state_lock: true
  result_comment: true
  telemetry:
    enabled: false
    endpoint: ${OTEL_ENDPOINT}
```

## Schema Reference

### `sources[]`

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | ✓ | Unique identifier referenced in routes |
| `type` | enum | ✓ | `plane` \| `jira` \| `linear` \| `github` \| `custom` |
| `config` | object | ✓ | Adapter-specific connection config |
| `poll_interval` | duration | — | Polling cadence; omit if using webhooks only |
| `filters` | object | — | Pre-filter tasks before routing |

### `workers[]`

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | ✓ | Unique identifier referenced in routes |
| `runner` | string | ✓ | Runner adapter type: `opencode` \| `script` \| `custom` |
| `model` | string | ✓ | Model ID passed verbatim to the runner (opaque to Apiary) |
| `config` | object | — | Runner-specific configuration |
| `description` | string | — | Human-readable label shown in logs and status output |

### `workers[].config` (common fields)

| Field | Type | Default | Description |
|---|---|---|---|
| `working_dir` | string | `$PWD` | Directory where the runner is invoked |
| `max_turns` | int | `10` | Max agent turns before the runner is stopped |
| `system_prompt_append` | string | — | Extra text appended to the runner's system prompt |
| `env` | map | — | Additional environment variables for the runner process |
| `timeout` | duration | `30m` | Hard wall-clock limit per run |

### `routes[]`

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | ✓ | Unique route identifier |
| `priority` | int | ✓ | Evaluation order — lower number = evaluated first |
| `match` | object | ✓ | Matching conditions (all specified fields must match) |
| `match.source` | string | — | Restrict to a specific source id |
| `match.labels` | string[] | — | All listed labels must be present on the task |
| `match.types` | string[] | — | Task type must be one of these values |
| `match.title_regex` | string | — | Task title must match this regular expression |
| `match.priority` | string[] | — | Task priority must be one of these values |
| `worker` | string | ✓ | Worker id to invoke |
| `on_complete` | object | — | Side-effects triggered after the worker finishes |
| `on_complete.set_state` | string | — | Move the task to this state in the source system |
| `on_complete.add_labels` | string[] | — | Add these labels to the task |

### `settings`

| Field | Type | Default | Description |
|---|---|---|---|
| `concurrency` | int | `2` | Max simultaneous worker runs |
| `log_level` | string | `info` | `debug` \| `info` \| `warn` \| `error` |
| `state_lock` | bool | `true` | Move task to "in progress" in the source before running |
| `result_comment` | bool | `true` | Post agent output as a comment on the source task |
| `telemetry.enabled` | bool | `false` | Emit OTLP traces |
| `telemetry.endpoint` | string | — | OTLP collector endpoint |

### `workflows[]`

The workflow engine is **the** dispatch path: a task that matches a workflow's
`trigger` runs through its `steps` in dependency order, threading a small
enrichable **memory** baton between them. Routes and workflows share an id
namespace; a plain `route` is treated as a single-step workflow, so the two
coexist. See the full annotated reference in the `workflow-mode` change specs
(`specs/workflow/spec.md`).

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | ✓ | Unique across all workflows and routes |
| `description` | string | — | Shown in the TUI and logs |
| `resume` | string | — | `allowed` \| `forbidden` \| `auto` (resume eligibility) |
| `trigger` | object | ✓ | Same matcher as `routes[].match`, plus `priority` and `exclusive` |
| `trigger.priority` | int | — | Evaluation order among matching workflows — lower number first |
| `trigger.exclusive` | bool | — | When this workflow matches, stop evaluating lower-priority triggers (it claims the task alone). Default `false`: a task **fans out** to every matching workflow (each runs as its own instance) |
| `steps[]` | object[] | ✓ | Ordered steps forming the DAG |
| `on_complete` / `on_fail` | object | — | Per-workflow side-effects on terminal success/failure (`set_state`, `add_labels`) |
| `env` | map | — | Workflow-scope environment variables applied to every step. Overrides `agents[].env`; overridden by `steps[].env`. See **Environment variables** below |

**Step types** (`steps[].type`, default `agent`):

| Type | Purpose | Key fields |
|---|---|---|
| `agent` | Invoke one agent | `agent`, `model`, `prompt`, `summary_prompt`, `output_schema`, `memory`, `depends_on`, `on_pass.next`, `on_fail.goto`/`max_retries`, `publish`, `spawn` |
| `split` | Conditional routing | `branches[].if` (expression) / `else`, `branches[].goto`, `multi` |
| `approval` | Human checkpoint (suspends the instance) | `message`, `resume_on`, `abort_on`, `timeout` |
| `foreach` | Fan-out over a prior step's array | `items`, `as`, `agent`, `max_items`, `fail_fast` |
| `workflow` | Delegate to a child workflow | `workflow` (child id) |

Split/approval conditions use a small expression language over `cell.*`,
`memory.*`, and `steps.*` with `==`/`!=`/`contains`/`matches` and `and`/`or`/`not`.

**Agent step write-back / fan-out fields:**

| Field | Type | Default | Description |
|---|---|---|---|
| `publish` | enum | `auto` | `auto`: write the step's `APIARY_PUBLISH` payload back to the task's source bindings as a comment. `off`: never write back, even if a payload is emitted. |
| `spawn` | enum | `auto` | Controls an `APIARY_SPAWN` request the step emits. `auto`: fire-and-forget — the child task is created and dispatched, the step does not wait. `await`: block until the spawned task is terminal; a child failure fails this step. |
| `env` | map | — | Step-scope environment variables. Highest-precedence explicit scope — overrides `workflows[].env` and `agents[].env`. See **Environment variables** below |

### Environment variables

Agent subprocesses receive `os.Environ()` (daemon-inherited) plus an overlay. The
overlay is composed lowest-precedence first:

```
identity overlay (git author/committer + source_token → GITHUB_TOKEN/GH_TOKEN)
  ← agents[].env        (agent scope)
    ← workflows[].env   (workflow scope)
      ← steps[].env     (step scope, highest precedence)
```

So explicit-scope precedence is **STEP > WORKFLOW > AGENT**, all of which sit above
the identity overlay (a deliberate `env` value can override `GITHUB_TOKEN`). Each
`env` is a `{ KEY: VALUE }` string map; values are subject to the usual `${VAR}`
expansion at config-load time (see **Environment Variable Interpolation**). The merge is
performed by the daemon's step executor; the workflow engine threads the
workflow-scope map to the executor via `StepRequest.WorkflowEnv`.

### `tasks` (top-level completion hook)

The optional top-level `tasks:` block fires **once per InternalTask** — when the
**last** of the task's fanned-out workflows reaches a terminal state — as opposed
to the per-workflow `on_complete`/`on_fail` hooks which fire once per workflow
instance. It applies to every `SourceBinding` on the task.

```yaml
tasks:
  on_complete:          # applied when ALL of the task's workflows succeeded
    set_state: done
    add_labels: [ai-done]
  on_fail:              # applied when ANY of the task's workflows failed
    set_state: blocked
    add_labels: [ai-failed]
```

| Field | Type | Description |
|---|---|---|
| `on_complete` | object | Hook (`set_state`, `add_labels`) applied to every source binding when all workflows succeeded |
| `on_fail` | object | Hook applied when any workflow failed |

## Internal Task Model

The **InternalTask** is the canonical, source-independent unit of work the engine
operates on. A source item (a GitHub issue, a Plane work item) is normalised into
an InternalTask by the **SourceBinder** (see the plugin-api spec), which records a
**SourceBinding** linking the two. One task may fan out to several workflows and
may **spawn** child tasks (lineage via `parent_task_id`), forming a tree that is
independent of any source system. The dashboard surfaces the InternalTask as the
primary unit, with its bindings, lineage, and workflow instances.

`internal_tasks`

| Column | Type | Description |
|---|---|---|
| `id` | text (pk) | Sortable id |
| `parent_task_id` | text | Set for spawned tasks (lineage); empty/NULL for root tasks |
| `title` / `description` | text | Task content (kept in sync with the live source item for bound tasks) |
| `input` | text (JSON) | Structured input passed by the spawner; NULL for source-bound tasks |
| `state` | text | `registered` \| `running` \| `approval_waiting` \| `done` \| `failed` |
| `metadata` | text (JSON) | Labels, priority, type, source, live source state (for trigger matching) |
| `outstanding_workflows` | int | Workflows still running for the task; the completion hook fires when it reaches 0 |
| `created_at` / `updated_at` | timestamp | |

`source_bindings`

| Column | Type | Description |
|---|---|---|
| `id` | text (pk) | |
| `task_id` | text | References `internal_tasks(id)` (one task → many bindings) |
| `source_id` | text | e.g. `github`, `plane` |
| `source_item_id` | text | Source-native item id |
| `source_item_url` | text | Deep link for display |
| `source_item_number` | text | Human reference, e.g. `#42`, `ERP-42` |
| | | `UNIQUE(source_id, source_item_id)` |

## Agent Output Markers

Agents communicate structured results back to the engine by emitting marker
blocks in their output. Markers are stripped from the visible output before it is
displayed or written back.

| Marker | Form | Purpose |
|---|---|---|
| `APIARY_OUTPUT:` | single line of JSON | Structured output validated against the step's `output_schema`; the last valid line wins |
| `APIARY_SUMMARY_START` … `APIARY_SUMMARY_END` | block | The agent's short handoff note (the memory baton between steps) |
| `APIARY_PUBLISH_BEGIN` … `APIARY_PUBLISH_END` | block | Write-back payload posted as a comment to the task's source bindings (gated by `step.publish`; ignored for binding-less tasks) |
| `APIARY_SPAWN_BEGIN` … `APIARY_SPAWN_END` | block of JSON `{"workflow","title","input"}` | Requests a child InternalTask running the named workflow (internal fan-out; `parent_task_id` is set by the engine, never by the agent) |

## Environment Variable Interpolation

Any value in `apiary.yaml` can reference an environment variable with `${VAR_NAME}` syntax. Apiary resolves these at startup and fails with a descriptive error if a referenced variable is unset.
