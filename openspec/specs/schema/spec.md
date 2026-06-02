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

## Environment Variable Interpolation

Any value in `apiary.yaml` can reference an environment variable with `${VAR_NAME}` syntax. Apiary resolves these at startup and fails with a descriptive error if a referenced variable is unset.
