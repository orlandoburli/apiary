---
name: apiary-guide
description: "Guia completo de uso e configuração do Apiary — sistema de orquestração de agentes via GitHub Issues, Plane, e outras fontes. Use ao configurar um novo projeto, adicionar agentes/sources/runners, ou quando precisar de referência rápida sobre o sistema."
---

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

routes:
  - id: implement
    priority: 10
    match:
      source: my-repo
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
                           │  │ (routes.*)  │  │
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

| Flag | Usage |
|------|-------|
| `model_flag` | `--model` (default) |
| `prompt_flag` | `--prompt` (default) |
| `turns_flag` | `--max-turns` (default) |
| `agent_flag` | `--agent` (default) |

OpenCode CLI requires `run` subcommand + positional prompt. `prompt_positional: true` is the default.

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
- Poll returns ALL matching issues every cycle. `inFlight` map prevents re-dispatch of already-running tasks.
- PRs are NOT filtered out — they become Cells with `Type: "pull_request"`.
- To route only PRs: `match.types: ["pull_request"]` in the route.

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

    # Rate-limit failover: retry on the next non-paused runner/model when the
    # primary is rejected by a provider usage limit. See Rate limits & resilience.
    fallbacks:
      - {runner: opencode-go, model: opencode-go/deepseek-v4-pro}
      - {runner: cursor, model: composer-2.5-fast}

    # Per-agent route overrides
    match:
      source: my-repo
      labels: [agent:engineer]
```

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
| `fallbacks` | no | Ordered `{runner, model}` chain to fail over to on a provider rate limit. `runner` must be defined; `model` optional. See Rate limits & resilience |

### `routes`

Match rules that decide which agent handles each task. Lower `priority` is evaluated first.

```yaml
routes:
  - id: complex-design
    priority: 10
    match:
      source: project-erp
      labels: [agent:staff]
    agent: staff
    on_complete:
      set_state: in review
```

| Field | Description |
|---|---|
| `priority` | Lower number = evaluated first |
| `match.source` | Source ID to match |
| `match.labels` | Cell must have ALL these labels |
| `match.types` | Cell types to match (e.g. `[pull_request]`) |
| `match.states` | Cell states to match |
| `match.exclude_label_prefix` | Exclude cells with labels starting with prefix (e.g., `agent:`) |
| `agent` | Target agent ID |
| `on_complete.set_state` | State to set on source when done |
| `on_complete.add_labels` | Labels to add on source when done |
| `on_complete.assign_from_output` | Parse `APIARY-ASSIGN: <agent>` from agent output and add label |

#### `on_complete` directives

Agents can output directives that Apiary parses from their final response:

| Directive | Action |
|---|---|
| `APIARY-ASSIGN: <agent-id>` | Adds `agent:<agent-id>` label to source, reassigns task |
| `APIARY-REVIEW: approve \| request-changes \| comment` | Submits a PR review (GitHub only) |

PR review is standalone in the dispatcher (not via source adapter) — sources are for issue tracking, PRs are code review. Can have Jira source + GitHub PRs.

### `settings`

```yaml
settings:
  concurrency: 2          # Global worker pool size
  log_level: info          # debug | info | warn | error
  state_lock: true         # Add "in-progress" label on acknowledge
  result_comment: true     # Post agent output as comment
  max_attempts: 3          # Re-dispatch failure cap per (task, workflow); <=0 disables
  memory:                  # Persistent agent memory — see "Agent Memory" section
    enabled: false         # opt-in; default false
```

### Rate limits & resilience

Safeguards that stop a saturated provider or a failing task from becoming a
runaway loop:

- **Failover on rate limits.** When a runner is rejected by a provider usage
  limit (the Claude CLI emits `rate_limit_event` `status: rejected`), Apiary pauses
  that runner type until it resets and retries the step on the agent's next
  `fallbacks` entry — instead of recording the empty "you've hit your limit" run
  as a success. Pause is keyed by runner type (all Claude agents share one
  account). `fallbacks` load at startup.
- **Re-dispatch cap (`settings.max_attempts`, default 3).** After N consecutive
  *failed* instances for a `(task, workflow)`, the dispatcher stops re-dispatching
  it (and applies the workflow's `on_fail` if set). Internal backstop independent
  of source labels — covers workflows with no `on_fail`. Rate-limited runs fail
  over and don't count; a success resets the count. `<=0` disables.
- **Non-blocking dispatch.** A busy agent's `max_workers` slot is acquired inside
  the dispatch goroutine, so a saturated agent never stalls polling/dispatch for
  other sources or agents.
- **Escalation notifications (`notifications:`).** When any hook (workflow
  `on_fail`/`on_complete`, task-level `tasks:` hooks, failure-cap park) adds a
  label listed in `on_labels` (e.g. `needs-attention`), every configured channel
  fires — so a parked flow pings a human instead of freezing silently. Only
  `type: command` (arbitrary shell hook: ntfy, Slack webhook, osascript).
  Placeholders `{{task_id}} {{cell_id}} {{number}} {{title}} {{url}} {{label}}
  {{summary}}` are shell-quoted; raw values exported as `APIARY_*` env vars.
  Channels run async (60s timeout) and never block the hook.

  ```yaml
  notifications:
    on_labels: [needs-attention]
    channels:
      - type: command
        run: curl -s -d "{{number}} escalated ({{label}}): {{summary}}" ntfy.sh/my-alerts
  ```

## Agent Memory

Persistent, tiered memory so agents stop relearning the same lessons
(full reference: `docs/memory.md`). Three tiers:

| Tier | Lifetime | Scope |
|---|---|---|
| Instance (built-in workflow memory) | One workflow instance | The instance's steps |
| **Task** | Task terminal + `task_retention` (default 720h) | The task, its retries/fan-out, and spawned descendants (lineage) |
| **Global** | Forever — human/agent curated | Daemon-wide: every agent and workflow of the project |

Opt-in via `settings.memory.enabled: true`. Storage is plain markdown under
`<data-dir>/memory/` (beside `apiary.db`): `MEMORY.md` index, `global/<slug>.md`
one fact per file, `tasks/<task_id>.md` append-only notes. Hand-editable; the
index self-heals. Gitignored by default.

**Write — `APIARY_MEMORIZE` marker** (sibling of `APIARY_PUBLISH`/`APIARY_SPAWN`;
single object or JSON array):

```
APIARY_MEMORIZE_BEGIN
{"scope": "global", "name": "kebab-slug", "description": "one line for the index",
 "content": "markdown body"}
APIARY_MEMORIZE_END
```

- `scope: task` (default) appends a working note to the current task;
  `scope: global` upserts a durable fact by `name` (same name = update).
- `name` + `description` required for global only. Malformed blocks are
  warnings — a memorize never fails the step.

**Recall.** Each step prompt gets `[Long-term Memory]` (the index, not full
bodies) and `[Task Memory]` (own + ancestor notes), capped by
`settings.memory.max_inject_chars` (default 4000). Agents read full entries
from `$APIARY_MEMORY_DIR/global/<name>.md` (env var set on every step).

**Step controls** (mirror `publish: off`):

```yaml
steps:
  - id: analyze
    agent: analyzer
    memory:
      memorize: off        # drop APIARY_MEMORIZE from this step (default: auto)
      recall: [task]       # inject only these tiers (default: both)
      # read: false        # still suppresses the entire memory doc, recall included
```

**Curation:** `apiary memory path | list | show <name> | rm <name> | prune [--dry-run]`.
Task notes are also pruned automatically by a daemon sweep; global entries never are.

**Soul-file snippet** — teach agents the protocol by adding something like this
to their soul file:

```markdown
## Memory

You have persistent memory at $APIARY_MEMORY_DIR. Your prompt includes the
long-term index and this task's notes — read a full entry from
$APIARY_MEMORY_DIR/global/<name>.md before re-deriving anything it covers.

When you learn something durable (a project gotcha, a tooling quirk, a
convention), save it:

APIARY_MEMORIZE_BEGIN
{"scope": "global", "name": "short-kebab-slug", "description": "one-line summary",
 "content": "the fact, with enough context to act on it cold"}
APIARY_MEMORIZE_END

For decisions and findings the NEXT step or a retry of THIS task needs, use
{"content": "..."} alone (task scope, the default). Update a stale fact by
re-emitting its name. NEVER memorize secrets, tokens, or credentials.
```

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
| `/` | Open filter bar (type query, `Esc` to exit, `Backspace` to delete) — runs in SQL over every task, not just the loaded page |
| `↓` / `PgDn` / `End` on the last row | Load the next 100 older tasks (list opens with the newest 100) |
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

## PR Review

When an agent returns a response containing `APIARY-REVIEW: approve | request-changes | comment`, Apiary calls the GitHub API to submit a PR review on the pull request associated with the Cell.

- Parsed from agent output via `reviewDirectiveRe` regex
- Calls `POST /repos/{owner}/{repo}/pulls/{number}/reviews`
- Uses agent's `source_token` if set
- Independent of source adapter — works with any issue tracker GitHub source

## Key Concepts

| Term | Description |
|---|---|
| **Cell** | Unit of work from a source (issue, PR, etc.) |
| **Source** | Adapter that polls an external issue tracker and writes results back |
| **Agent** | An LLM persona that processes tasks |
| **Runner** | How an agent executes (CLI subprocess or API call) |
| **Route** | Rule that matches Cells to Agents |
| **Dispatcher** | Core loop that polls sources, routes Cells, dispatches to agents |
| **inFlight** | Map of task IDs currently being processed — prevents double-dispatch |
| **Soul file** | System prompt / persona definition for an agent |
| **Agent memory** | Persistent markdown store (task notes + global facts) written via `APIARY_MEMORIZE`, recalled into step prompts |
| **IPC Socket** | Unix socket (`apiary.sock`) for dashboard ↔ dispatcher communication |

## Agent Identity

Each agent can have its own GitHub identity for write operations:

- `source_token` → overrides source `api_key` for Acknowledge/WriteResult/SetState/AddLabels
- `source_email` + `source_name` → injected as `GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_NAME`, `GIT_COMMITTER_EMAIL` in the runner's environment

Passed through Go context via `context.WithValue(ctx, source.SourceTokenCtxKey, token)`.

## Troubleshooting

| Problem | Check |
|---|---|
| Dashboard shows no data | Ensure `apiary run` is running in another terminal |
| GitHub poll not picking up issues | Verify token has correct permissions, check `filters.labels` |
| Tasks stuck "running" | They may have crashed without cleanup — use `R` (Shift+R) to force restart |
| Socket connection refused | Dashboard and dispatcher must share the same config (same `.apiary/` directory) |
| OpenCode runner fails | Ensure `opencode` binary is on PATH and `prompt_positional: true` is set |
