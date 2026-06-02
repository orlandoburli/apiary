# Apiary — Configuration Schema

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
      project: project-erp
      api_key: ${PLANE_API_KEY}
    poll_interval: 60s          # optional; omit to use webhooks only
    filters:
      states: [backlog, todo]   # only pull tasks in these states
      labels: [ai-ready]        # optional label gate

  - id: main-jira
    type: jira
    config:
      host: https://myorg.atlassian.net
      email: ${JIRA_EMAIL}
      api_token: ${JIRA_TOKEN}
      project_key: ERP
    filters:
      jql: 'status in ("To Do") AND labels = "ai-ready"'

# ── Workers ────────────────────────────────────────────────────
workers:
  - id: backend-dev
    description: "Go backend tasks — bugs, features, refactors"
    runner: claude-code
    model: claude-opus-4-8
    config:
      working_dir: /workspace/project-erp
      max_turns: 10
      system_prompt_append: |
        You are working in a Go + PostgreSQL backend. Always run impact
        analysis before modifying any symbol. Follow the conventions in CLAUDE.md.

  - id: frontend-dev
    description: "React/Next.js frontend tasks"
    runner: claude-code
    model: claude-sonnet-4-6
    config:
      working_dir: /workspace/project-erp
      max_turns: 8
      system_prompt_append: |
        You are working on a Next.js 14 frontend with TypeScript.
        Follow the component conventions in CLAUDE.md.

  - id: docs-writer
    description: "Documentation, changelog, spec writing"
    runner: claude-code
    model: claude-haiku-4-5
    config:
      working_dir: /workspace/project-erp
      max_turns: 5

  - id: code-reviewer
    description: "PR review and security checks"
    runner: claude-code
    model: claude-opus-4-8
    config:
      working_dir: /workspace/project-erp
      max_turns: 3
      system_prompt_append: |
        Review for correctness, security, and performance. Be terse.

# ── Routes ─────────────────────────────────────────────────────
# Rules are evaluated top-to-bottom; first match wins.
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
      source: main-plane          # catch-all for this source
    worker: backend-dev

# ── Global Settings ────────────────────────────────────────────
settings:
  concurrency: 3                  # max simultaneous worker runs
  log_level: info
  state_lock: true                # mark task "in progress" before running
  result_comment: true            # post agent output as a task comment
  telemetry:
    enabled: false                # opt-in OTLP traces
    endpoint: ${OTEL_ENDPOINT}
```

## Schema Reference

### `sources[]`

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | ✓ | Unique identifier referenced in routes |
| `type` | enum | ✓ | `plane` \| `jira` \| `linear` \| `github` \| `custom` |
| `config` | object | ✓ | Adapter-specific connection config |
| `poll_interval` | duration | — | Polling cadence; omit if using webhooks |
| `filters` | object | — | Pre-filter tasks before routing |

### `workers[]`

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | ✓ | Unique identifier referenced in routes |
| `runner` | enum | ✓ | `claude-code` \| `opencode` \| `script` \| `custom` |
| `model` | string | ✓ | LLM model ID passed to the runner |
| `config` | object | — | Runner-specific configuration |
| `description` | string | — | Human-readable label shown in logs/UI |

### `routes[]`

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | ✓ | Unique route identifier |
| `priority` | int | ✓ | Lower = evaluated first |
| `match` | object | ✓ | Matching conditions |
| `match.source` | string | — | Restrict to a specific source id |
| `match.labels` | string[] | — | All listed labels must be present |
| `match.types` | string[] | — | Task type must be one of these |
| `match.title_regex` | string | — | Title must match regex |
| `worker` | string | ✓ | Worker id to invoke |
| `on_complete` | object | — | Side-effects after worker finishes |

### `settings`

| Field | Type | Default | Description |
|---|---|---|---|
| `concurrency` | int | `2` | Max parallel worker executions |
| `log_level` | string | `info` | `debug` \| `info` \| `warn` \| `error` |
| `state_lock` | bool | `true` | Move task to "in progress" before run |
| `result_comment` | bool | `true` | Post agent output as task comment |
