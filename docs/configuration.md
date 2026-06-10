# Configuration

Everything Apiary does is declared in one YAML file. This page is the field
reference for every top-level section except `workflows`, which has its
[own page](workflows.md).

## File location & loading

Apiary looks for the config in two places, in order:

1. `apiary.yaml` in the current directory
2. `.apiary/apiary.yaml` in the current directory

Or pass a path explicitly: every command accepts `--config <path>`.

Two things happen at load time:

- **`.env` auto-load.** A `.env` file in the config directory is read and
  applied (`--env-file` overrides the path). Variables already set in the
  shell take priority — they are never overwritten.
- **`${VAR}` expansion.** String values containing `${VAR}` are replaced from
  the environment, so secrets stay out of the file:
  `api_key: ${GITHUB_TOKEN}`.

The config is validated against a schema on every load; `apiary validate`
runs the same checks standalone, and the
[VS Code extension](vscode-extension.md) and the
[JSON Schema](https://github.com/orlandoburli/apiary/blob/main/schema/apiary.json)
give you live validation and autocomplete while editing.

```yaml
version: "1"        # currently the only accepted value
```

## `runners`

Runners define **how** agents execute — a CLI subprocess (`claude`,
`opencode`, the Cursor `agent` CLI) or a direct API call. They have a
[dedicated page](runners.md) covering each provider, the engine options,
prompt delivery, MCP wiring, and API runners; this section is the short
version.

```yaml
runners:
  # Claude CLI — requires the `claude` binary on PATH
  - id: claude
    type: cli
    provider: claude

  # OpenCode CLI — requires the `opencode` binary on PATH
  - id: opencode
    type: cli
    provider: opencode

  # OpenCode API
  - id: opencode-api
    type: opencode-api
    config:
      api_key: ${OPENCODE_API_KEY}
    models:
      - opencode-go/deepseek-v4-pro

default_runner: claude    # used by agents that don't name a runner
```

| Field | Description |
|---|---|
| `id` | Unique runner identifier, referenced by `agents[].runner` and `agents[].fallbacks` |
| `type` | `cli` (subprocess) or a self-contained adapter like `opencode-api` |
| `provider` | Which CLI the `cli` engine drives: `claude`, `opencode`, `cursor` |
| `config` | Engine options — binary, flags, API key; see [Runners](runners.md#cli-engine-configuration) |
| `models` | Models this runner supports (informational) |
| `mcps` | [MCP servers](runners.md#mcp-servers) exposed to every agent on this runner |

!!! tip "Presets cover the CLI flags"
    Each provider preset already passes the right flags — the Claude preset
    runs `claude --output-format stream-json --verbose`, so Apiary parses the
    structured events into the readable `[assistant]` / `[tool→ …]`
    conversation in the
    [task logs](dashboard.md#watching-the-live-conversation-debug-mode),
    plus accurate token and cost figures. `config` is only for overrides.

## `sources`

Sources are where work comes from. Each source polls its tracker on
`poll_interval` and returns the items passing `filters`.

```yaml
sources:
  - id: my-repo
    type: github
    config:
      repo: my-org/my-repo
      api_key: ${GITHUB_TOKEN}
    poll_interval: 120s
    filters:
      states: [open]
      labels: [ai-ready]

  - id: project-erp
    type: plane
    config:
      workspace: my-workspace
      project: 6ee18c57-7f88-414c-bad5-44ef4afd979b
      api_key: ${PLANE_API_KEY}
      base_url: https://plane.example.com
    poll_interval: 60s
    filters:
      states: [backlog, todo, in progress]
```

| Field | Description |
|---|---|
| `id` | Unique source identifier, referenced by `trigger.match.source` |
| `type` | `github` or `plane` |
| `config` | Adapter-specific — see the per-source pages |
| `poll_interval` | How often to poll, e.g. `30s`, `2m` (default `60s`) |
| `filters.states` | Only ingest items in these states |
| `filters.labels` | Only ingest items carrying these labels |

Adapter details, API operations, and token permissions:

- [GitHub source](github-source.md)
- [Plane source](plane-source.md)

!!! tip "Use a label as the opt-in switch"
    Filtering on a label like `ai-ready` means nothing reaches the agents
    unless a human deliberately marked it. It's the simplest safety gate.

## `agents`

Agents are the personas that execute steps. Each links a runner to a model,
and optionally carries a persona, skills, its own source identity, and a
failover chain.

```yaml
agents:
  - id: engineer
    description: "Implements tasks following project conventions"
    soul_file: .apiary/souls/engineer.md
    runner: claude
    model: claude-sonnet-4-6
    skills: [git-workflow]
    max_workers: 2

    # Per-agent GitHub identity for write-backs and commits
    source_token: ${GITHUB_TOKEN_ENGINEER}
    source_email: engineer@company.com
    source_name: Engineer Bot

    # Rate-limit failover chain — see Rate limits & resilience
    fallbacks:
      - {runner: opencode, model: opencode-go/deepseek-v4-pro}

    # Agent-scope environment variables
    env:
      REVIEW_PROFILE: strict
```

| Field | Required | Description |
|---|---|---|
| `id` | yes | Unique agent identifier |
| `description` | no | Human-readable description (included in routing context) |
| `model` | yes | Model name passed to the runner |
| `runner` | no | Runner ID (falls back to `default_runner`) |
| `soul_file` | no | Path to the agent's system prompt / persona file |
| `skills` | no | Skill names injected into the agent's context |
| `max_workers` | no | Per-agent concurrency cap (default 1) — see [concurrency](#concurrency) |
| `max_turns` | no | Max agent turns per step run (default 0 = unlimited). CLI runners pass it as the provider's turns flag (e.g. claude's `--max-turns`); 0 omits the flag |
| `source_token` | no | Source API token for this agent's write operations — see [Agent identity](#agent-identity) |
| `source_email` / `source_name` | no | Git author identity exported to the runner environment |
| `fallbacks` | no | Ordered `{runner, model}` list for [rate-limit failover](resilience.md#provider-rate-limits-failover); `model` optional (empty = that runner's default) |
| `mcps` | no | Agent-scope [MCP servers](runners.md#mcp-servers), layered over the runner's |
| `env` | no | Agent-scope environment variables — see [Environment variables](#environment-variables) |

Which tasks reach an agent is decided by a
[workflow `trigger`](workflows.md#triggers), never by the agent itself.

### Agent identity

Each agent can act as its own account rather than a shared bot:

- **`source_token`** — overrides the source-level `api_key` for this agent's
  *write* operations (comments, labels, state changes). It is also exported to
  the agent subprocess as `GITHUB_TOKEN` / `GH_TOKEN`, so `gh` commands the
  agent runs authenticate as the agent's own account. Polling always uses the
  source-level key.
- **`source_email` / `source_name`** — injected as `GIT_AUTHOR_NAME`,
  `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_NAME`, `GIT_COMMITTER_EMAIL`, so commits
  carry the right author.

This makes multi-agent traces readable: the reviewer's comments come from the
reviewer bot, the engineer's commits from the engineer bot.

## `settings`

```yaml
settings:
  concurrency: 2
  log_level: info
  state_lock: true
  result_comment: true
  task_timeout: 30m
  max_attempts: 3
  log_max_size_mb: 50
  log_max_backups: 5
  log_max_age_days: 30
```

| Field | Default | Description |
|---|---|---|
| `concurrency` | 2 | Global worker-pool size. **Informational** — real dispatch concurrency is the sum of per-agent `max_workers` |
| `log_level` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `state_lock` | `false` | Add an `in-progress` label on the source item when work starts |
| `result_comment` | `false` | Post the agent's final output back as a comment |
| `task_timeout` | `30m` | Default per-run timeout (e.g. `2h`) |
| `max_attempts` | 3 | Stop re-dispatching a `(task, workflow)` after this many **consecutive failed** instances; `<=0` disables — see [resilience](resilience.md#re-dispatch-failure-cap) |
| `log_max_size_mb` | 50 | Rotate `apiary.log` past this size (MB); negative disables rotation |
| `log_max_backups` | 5 | Rotated files kept as `apiary.log.1` … `.N`, oldest dropped; negative keeps none |
| `log_max_age_days` | 30 | Prune rotated backups **and** per-task logs (`logs/tasks/*.log`) older than this, at startup and after each rotation; negative disables |

### Log rotation

The daemon writes its service log to `<config-dir>/.apiary/logs/apiary.log`
and one file per task under `logs/tasks/`. Without limits these grow
unbounded, so rotation is on by default: when a write would push `apiary.log`
past `log_max_size_mb`, the file is renamed to `apiary.log.1` (older backups
shift up, the one past `log_max_backups` is dropped) and a fresh file is
started. Backups and task-log files untouched for `log_max_age_days` are
deleted. Task history shown in the dashboard lives in SQLite and is not
affected by file pruning.

### Concurrency

Each agent has its own semaphore sized by its `max_workers` (default 1), so
one agent's long runs never starve another. The effective parallelism of the
whole hive is the **sum of `max_workers` across agents** —
`settings.concurrency` does not cap dispatch. Source polls are serialized
(one source at a time) regardless.

!!! warning "Shared provider limits"
    All agents on the same CLI runner typically share one provider account
    (e.g. one Claude subscription). Raising `max_workers` burns through a
    shared rate limit faster — it cannot raise the provider's ceiling. Pair
    higher concurrency with [`fallbacks`](resilience.md).

## `tasks`

Top-level hooks that fire **once per task** when the last of its fanned-out
workflow instances reaches a terminal state — as opposed to per-workflow
`on_complete`/`on_fail`, which fire per instance. See
[Tasks & fan-out](tasks-and-fanout.md#task-level-hooks).

```yaml
tasks:
  on_complete:
    add_labels: [ai-complete]
  on_fail:
    set_state: blocked
    add_labels: [ai-failed]
```

## Environment variables

Agent subprocesses inherit the daemon's environment plus an overlay you
control at three scopes, each an `env: {KEY: VALUE}` map:

| Scope | Field | Applies to |
|---|---|---|
| **Agent** | `agents[].env` | every step this agent runs, in any workflow |
| **Workflow** | `workflows[].env` | every step of that workflow |
| **Step** | `workflows[].steps[].env` | only that step |

**Precedence (highest wins): step > workflow > agent.**

All three sit *above* the identity overlay (the git identity and the
`source_token` → `GITHUB_TOKEN`/`GH_TOKEN` mapping), so the agent's own token
wins by default — but an explicit `env` value at any scope can deliberately
override it.

Values pass through `${VAR}` expansion at load time, so you can forward a
daemon variable explicitly: `env: { DEPLOY_URL: "${DEPLOY_URL}" }`.

```yaml
agents:
  - id: reviewer
    env:
      REVIEW_PROFILE: strict       # agent-scope default

workflows:
  - id: code-review
    env:
      REVIEW_PROFILE: relaxed      # overrides the agent value
      CI_TARGET: staging
    steps:
      - id: run
        agent: reviewer
        env:
          CI_TARGET: production    # overrides the workflow value, this step only
```

Effective environment for `run`: `REVIEW_PROFILE=relaxed`,
`CI_TARGET=production`, plus the identity overlay.

## A complete example

The repository ships a fully commented reference config covering every
feature on this page and the workflow pages:
[`.apiary/example-apiary-full.yaml`](https://github.com/orlandoburli/apiary/blob/main/.apiary/example-apiary-full.yaml).
