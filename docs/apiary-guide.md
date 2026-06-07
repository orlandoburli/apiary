# Apiary Guide

Apiary is a **task-driven agent orchestration harness** — it polls issue trackers (GitHub, Plane, etc.), routes work to LLM agents (Claude, OpenCode), and tracks every execution attempt in a local SQLite database.

```sh
# Start the dispatcher
apiary run

# Open the dashboard (separate terminal)
apiary dashboard
```

## Quick Start

### 1. Install

```sh
# From source
git clone https://github.com/orlandoburli/apiary
cd apiary/src && go build -o apiary ./cmd/apiary
```

### 2. Create a config file

Minimal `apiary.yaml`:

```yaml
version: "1"

runners:
  - id: claude
    type: cli
    provider: claude
    config:
      args: ["--output-format", "stream-json", "--verbose"]

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

agents:
  - id: engineer
    description: "Implements tasks"
    runner: claude
    model: claude-sonnet-4-6

workflows:
  - id: implement
    trigger:
      priority: 10
      match:
        source: my-repo
    steps:
      - id: run
        agent: engineer
    on_complete:
      set_state: closed

settings:
  concurrency: 2
  log_level: info
```

### 3. Run

```sh
apiary run           # start the daemon
apiary dashboard     # watch in another terminal
```

## Architecture

```
┌──────────────┐   poll    ┌──────────────────┐   dispatch   ┌────────────┐
│  Issue Source │ ────────>│                   │ ───────────> │   Agent    │
│  (GitHub,     │          │   Dispatcher      │              │  (Claude,  │
│   Plane, ...) │ <────────│   (apiary run)    │ <─────────── │  OpenCode) │
│              │  write    │                   │   result     │            │
└──────────────┘           │  ┌─────────────┐  │              └────────────┘
                           │  │   Router    │  │
                           │  │ (triggers)  │  │
                           │  └─────────────┘  │
                           │  ┌─────────────┐  │
                           │  │ SQLite store │  │
                           │  └─────────────┘  │
                           └──────────────────┘
                                   │
                                   ▼
                           ┌──────────────┐
                           │  Dashboard   │
                           │ (read-only)  │
                           └──────────────┘
```

## Configuration Reference

Config lookup order:

1. `apiary.yaml` in current directory
2. `.apiary/apiary.yaml` in current directory

State lives alongside the config: `.apiary/apiary.db` (SQLite), `.apiary/logs/`, `.apiary/apiary.sock` (IPC).

### `version`

```yaml
version: "1"
```

Currently only `"1"`.

### `runners`

Define how agents execute code. Each runner has a `type` + `provider` pair.

```yaml
runners:
  # Claude CLI — requires `claude` binary on PATH
  - id: claude
    type: cli
    provider: claude
    config:
      args: ["--output-format", "stream-json", "--verbose"]

  # Anthropic API — requires ANTHROPIC_API_KEY
  - id: claude-api
    type: cli
    provider: anthropic
    config:
      provider: anthropic
      base_url: https://api.anthropic.com/v1
      api_key: ${ANTHROPIC_API_KEY}

  # OpenCode CLI — requires `opencode` binary on PATH
  - id: opencode-go-cli
    type: cli
    provider: opencode
    config:
      mode: cli
      subscription: go
      binary: opencode
      agent: backend-dev
      args: ["--output-format", "stream-json", "--verbose"]

  # OpenCode API
  - id: opencode-go-api
    type: opencode-api
    config:
      subscription: go
      api_key: ${OPENCODE_GO_API_KEY}
    models:
      - opencode-go/deepseek-v4-pro
      - opencode-go/minimax-m3
```

#### OpenCode-specific flags

| Flag     | Usage           |
| -------- | --------------- |
| `model_flag`  | `--model` (default) |
| `prompt_flag` | `--prompt` (default) |
| `turns_flag`  | `--max-turns` (default) |
| `agent_flag`  | `--agent` (default) |

OpenCode CLI requires `run` subcommand + positional prompt (`prompt_positional: true` is the default).

#### `default_runner`

```yaml
default_runner: claude
```

Runner used by agents that don't specify one.

### `sources`

Where Apiary gets its work items (Cells):

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
```

#### GitHub source

| Config field | Required | Default | Description |
|---|---|---|---|
| `repo` | yes | — | `owner/repo` |
| `api_key` | no | — | PAT (`repo` scope classic, `Issues: Read & Write` fine-grained) |
| `base_url` | no | `https://api.github.com` | GHES support |

**Operations:**

| Operation | API call |
|---|---|
| Poll | `GET /repos/{owner}/{repo}/issues?state=open` (all open, no `since` filter) |
| Acknowledge | `POST /repos/{owner}/{repo}/issues/{n}/labels` (adds `in-progress`) |
| WriteResult | `POST /repos/{owner}/{repo}/issues/{n}/comments` |
| SetState | `PATCH /repos/{owner}/{repo}/issues/{n}` |
| AddLabels | `PATCH /repos/{owner}/{repo}/issues/{n}` (replaces labels) |

**Key behavior:**

- Poll returns **ALL** matching issues every cycle. `inFlight` map prevents re-dispatch of already-running tasks.
- Pull requests are filtered out during polling (GitHub's `/issues` endpoint returns PRs too, but they are not work items). GitHub Cells are always `Type: "issue"`.

#### Plane source

```yaml
  - id: project-erp
    type: plane
    config:
      workspace: project-erp
      project: <uuid>
      api_key: ${PLANE_API_KEY}
      base_url: <instance-url>
    poll_interval: 60s
    filters:
      states: [backlog, todo, in progress]
```

#### `.env` auto-load

Apiary loads `.env` from the config file directory at startup via `loadDotEnv()`. Already-set env vars take priority (they are not overwritten).

### `agents`

Workers that execute tasks. Each agent links a runner to a model and optionally carries a soul file and skills.

```yaml
agents:
  - id: engineer
    description: "Implements tasks following project conventions"
    soul_file: .apiary/souls/engineer.md
    runner: claude
    model: claude-sonnet-4-6
    skills: [git-workflow, gitnexus-codebase]

    # Per-agent GitHub identity (overrides source-level api_key for write ops)
    source_token: ${GITHUB_TOKEN_ENGINEER}
    source_email: engineer@company.com
    source_name: Engineer Bot

    # Max concurrency for THIS agent (overrides global settings.concurrency)
    max_workers: 2

    # Rate-limit failover: if the primary runner is rejected by a provider
    # usage limit (e.g. Claude's 5-hour session limit), retry on the next
    # non-paused fallback runner/model instead of stalling. See Rate limits
    # & resilience below.
    fallbacks:
      - {runner: opencode-go, model: opencode-go/deepseek-v4-pro}
      - {runner: cursor, model: composer-2.5-fast}
```

Which tasks reach this agent is decided by a [workflow `trigger`](#workflows), not the
agent itself.

| Field | Required | Description |
|---|---|---|
| `id` | yes | Unique agent identifier |
| `description` | no | Human-readable description |
| `soul_file` | no | Path to agent system prompt / persona file |
| `runner` | no | Runner ID (uses `default_runner` if omitted) |
| `model` | no | Model name passed to the runner |
| `skills` | no | List of skill names for agent context |
| `source_token` | no | Override source API key for this agent's write operations |
| `source_email` | no | Git author email (set as `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_EMAIL` in runner env) |
| `source_name` | no | Git author name (set as `GIT_AUTHOR_NAME`, `GIT_COMMITTER_NAME` in runner env) |
| `max_workers` | no | Per-agent concurrency cap (default: global `settings.concurrency`) |
| `fallbacks` | no | Ordered list of `{runner, model}` to fail over to when the primary runner hits a provider rate limit. `runner` must be a defined runner id; `model` is optional (empty = that runner's default). See [Rate limits & resilience](#rate-limits--resilience) |
| `env` | no | Agent-scope environment variables (map). Lowest-precedence explicit scope — see [Environment variables](#environment-variables) |

### `workflows`

A workflow fires when its `trigger` matches a task, then runs its `steps` in order.
Lower trigger `priority` is evaluated first. The simplest workflow has a single step —
that one-step form replaces what used to be a `route`.

```yaml
workflows:
  - id: complex-design
    trigger:
      priority: 10
      match:
        source: project-erp
        labels: [agent:staff]
    steps:
      - id: run
        agent: staff
    on_complete:
      set_state: in review
```

| Field | Description |
|---|---|
| `trigger.priority` | Lower number = evaluated first |
| `trigger.match.source` | Source ID to match |
| `trigger.match.labels` | Task must have ALL these labels |
| `trigger.match.types` | Task types to match (GitHub tasks are always `issue`) |
| `trigger.match.states` | Task states to match |
| `trigger.match.exclude_label_prefix` | Exclude tasks with labels starting with prefix (e.g. `agent:`) |
| `steps[].agent` | Agent that runs this step |
| `on_complete.set_state` | State to set on source when the workflow succeeds |
| `on_complete.add_labels` | Labels to add on source when the workflow succeeds |
| `on_fail` | Hook applied when the workflow fails (same shape as `on_complete`) |

### `settings`

```yaml
settings:
  concurrency: 2          # Global worker pool size
  log_level: info          # debug | info | warn | error
  state_lock: true         # Add "in-progress" label on acknowledge
  result_comment: true     # Post agent output as comment
  max_attempts: 3          # Re-dispatch failure cap per (task, workflow); <=0 disables
```

| Field | Default | Description |
|---|---|---|
| `concurrency` | 2 | Global worker pool size (informational; real dispatch concurrency is the sum of per-agent `max_workers`) |
| `log_level` | info | `debug` \| `info` \| `warn` \| `error` |
| `state_lock` | false | Add an "in-progress" label on acknowledge |
| `result_comment` | false | Post agent output back as a comment |
| `max_attempts` | 3 | Stop re-dispatching a `(task, workflow)` after this many **consecutive failed** instances (rate-limited runs don't count). `<=0` disables the cap. See [Rate limits & resilience](#rate-limits--resilience) |

## Rate limits & resilience

Apiary is built around a single dispatch path and a small set of safeguards that
keep a saturated provider or a failing task from turning into a runaway,
money-burning loop.

### Provider rate limits → failover

When a runner is rejected by a provider usage limit — e.g. the Claude CLI emits a
`rate_limit_event` with `status: rejected` ("you've hit your session limit") —
Apiary does **not** treat the empty run as a success. Instead it:

1. **Pauses that runner type** until the limit resets (`resetsAt`). Because every
   Claude agent shares one account, pausing is keyed by runner type, so all Claude
   agents back off together.
2. **Fails over** to the agent's next `fallbacks` entry whose runner isn't paused,
   retrying the same step on that runner/model. While the primary is paused, new
   steps go straight to the fallback (no wasted, pre-failed call).

```yaml
agents:
  - id: engineer
    runner: claude
    model: claude-sonnet-4-6
    fallbacks:
      - {runner: opencode-go, model: opencode-go/deepseek-v4-pro}
      - {runner: cursor, model: composer-2.5-fast}
```

Each attempt is recorded as its own execution, so the dashboard shows the
failover (primary rate-limited → fallback ran). `fallbacks` load at startup —
changing the chain requires a restart.

### Re-dispatch failure cap

A task whose workflow keeps failing would otherwise be re-dispatched on every poll
forever (especially workflows with no `on_fail`, like escape-hatch routes). The
`settings.max_attempts` cap is an **internal** backstop, independent of
source-side labels: after N consecutive **failed** instances for the same
`(task, workflow)`, Apiary stops re-dispatching it and applies the workflow's
`on_fail` hook (if any). Rate-limited runs fail over and are not counted; a single
success resets the count. Default `3`; set `<=0` to disable.

### Non-blocking dispatch

Each agent's `max_workers` slot is acquired inside the dispatch goroutine, not on
the poll-loop thread. A fully-busy agent therefore parks its own runs without
stalling polling or dispatch for any other source or agent.

## Dashboard

Terminal UI for watching live state:

| Key | Action |
|---|---|
| `←` / `→` | Switch tabs |
| `Tab` / `Shift+Tab` | Next / previous tab |
| `↑` / `↓` | Move selection / scroll |
| `Home` / `End` | Jump to top / bottom |
| `PgUp` / `PgDn` / `Space` | Page up / down |
| `r` | Refresh |
| `q` / `Ctrl+C` | Quit |

### Tasks tab

| Key | Action |
|---|---|
| `s` | Toggle sort direction (asc/desc) |
| `S` | Cycle to next sort field |
| `/` | Open filter bar (type query, `Esc` to exit, `Backspace` to delete) |
| `d` | Task detail view |
| `Enter` / `l` | Task logs view |
| `o` | Open task URL in browser |
| `R` (Shift+R) | Force restart (with confirmation) |
| `C` | Clear logs (with confirmation) |

### Agents tab

| Key | Action |
|---|---|
| `d` | Agent detail view |
| `Enter` / `l` | Activity list → task logs drill-down |
| `o` | Open task URL in browser |

In detail view: `m` cycles model, `r` cycles runner, `w` cycles max_workers. Changes persist via IPC socket.

### Logs tab

| Key | Action |
|---|---|
| `w` | Toggle word wrap |
| `←` / `→` | Horizontal scroll (when wrap is off) |

## Key Concepts

| Term | Description |
|---|---|
| **Task** | Unit of work from a source (an issue or item) |
| **Source** | Adapter that polls an external issue tracker and writes results back |
| **Agent** | An LLM persona that processes tasks |
| **Runner** | How an agent executes (CLI subprocess or API call) |
| **Route** | Rule that matches Tasks to Agents |
| **Dispatcher** | Core loop that polls sources, routes Tasks, dispatches to agents |
| **inFlight** | Map of task IDs currently being processed — prevents double-dispatch |
| **Soul file** | System prompt / persona definition for an agent |
| **IPC Socket** | Unix socket (`apiary.sock`) for dashboard ↔ dispatcher communication |

## Agent Identity

Each agent can have its own GitHub identity for write operations:

- `source_token` — overrides source `api_key` for Acknowledge/WriteResult/SetState/AddLabels (passed through Go context via `context.WithValue(ctx, source.SourceTokenCtxKey, token)`), and is also exported to the agent subprocess as `GITHUB_TOKEN` / `GH_TOKEN` so `gh` commands the agent runs itself authenticate as the agent's own account
- `source_email` + `source_name` — injected as `GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_NAME`, `GIT_COMMITTER_EMAIL` in the runner's environment

## Environment variables

Agent subprocesses inherit the daemon's environment plus an overlay. You can set
arbitrary environment variables at three scopes via an `env: { KEY: VALUE }` map:

| Scope | Field | Applies to |
|---|---|---|
| **Agent** | `agents[].env` | every step that runs this agent, in any workflow |
| **Workflow** | `workflows[].env` | every step of this workflow |
| **Step** | `workflows[].steps[].env` | only that step |

**Precedence (highest wins): STEP > WORKFLOW > AGENT.** A key set at step scope
overrides the same key at workflow scope, which overrides agent scope.

These three explicit scopes sit **above the identity overlay** (the git identity
and the `source_token` → `GITHUB_TOKEN`/`GH_TOKEN` mapping described under
[Agent Identity](#agent-identity)), so by default the agent's own token wins — but
an explicit `env` value at any scope can deliberately override it (e.g. a single
step setting its own `GITHUB_TOKEN`).

Values pass through the config loader's `${VAR}` expansion (resolved at load
time), so you can forward a daemon var explicitly:
`env: { DEPLOY_URL: "${DEPLOY_URL}" }`.

```yaml
agents:
  - id: reviewer
    source_token: ${GITHUB_TOKEN_REVIEWER}
    env:
      REVIEW_PROFILE: strict     # agent-scope default

workflows:
  - id: code-review
    env:
      REVIEW_PROFILE: relaxed    # overrides the agent value for this workflow
      CI_TARGET: staging
    steps:
      - id: run
        agent: reviewer
        env:
          CI_TARGET: production  # overrides the workflow value for this step only
```

Effective environment for `run`: `REVIEW_PROFILE=relaxed`, `CI_TARGET=production`,
plus the identity overlay.

## Troubleshooting

| Problem | Check |
|---|---|
| Dashboard shows no data | Ensure `apiary run` is running in another terminal |
| GitHub poll not picking up issues | Verify token has correct permissions, check `filters.labels` |
| Tasks stuck "running" | They may have crashed without cleanup — use `R` (Shift+R) to force restart |
| Socket connection refused | Dashboard and dispatcher must share the same config (same `.apiary/` directory) |
| OpenCode runner fails | Ensure `opencode` binary is on PATH and `prompt_positional: true` is set |
