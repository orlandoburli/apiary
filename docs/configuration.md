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
`opencode`, `codex`, the Cursor `agent` CLI) or a direct API call. They have a
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

  # Codex CLI — requires the `codex` binary on PATH
  - id: codex
    type: cli
    provider: codex

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
| `provider` | Which CLI the `cli` engine drives: `claude`, `opencode`, `codex`, `cursor` |
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
    Codex runs as `codex exec --sandbox workspace-write` and reads repo skills
    from `.agents/skills`.

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
| `fallbacks` | no | Ordered `{runner, model}` list for [runner failover](resilience.md#runner-failures--failover); `model` optional (empty = that runner's default) |
| `fallback_strategy` | no | Ordering policy for the failover chain: `ordered` (default), `random`, `least_cost`, `fastest` |
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

## `profiles`

Named profiles let you switch runner assignments across the entire fleet at
startup without editing individual agent configs. Each profile maps agent IDs
to overrides for `runner`, `model`, `fallbacks`, and `fallback_strategy`.

```yaml
profiles:
  opencode:
    engineer:  {runner: opencode-go, model: opencode-go/deepseek-v4-flash}
    backend:   {runner: opencode-go, model: opencode-go/deepseek-v4-flash}
    frontend:  {runner: opencode-go, model: opencode-go/deepseek-v4-flash}
    reviewer:  {runner: opencode-go, model: opencode-go/deepseek-v4-flash}
    qa:        {runner: opencode-go, model: opencode-go/deepseek-v4-pro}

  hybrid:
    engineer:  {runner: codex, fallbacks: [{runner: opencode-go, model: opencode-go/deepseek-v4-flash}]}
    reviewer:  {runner: opencode-go, model: opencode-go/deepseek-v4-flash}
```

Activate at startup:

```sh
apiary run --profile=opencode
```

Only the agents listed in the profile are overridden — unknown agents are
skipped with a warning. If the named profile does not exist, the daemon logs
a warning and continues with the base config.

| Field | Description |
|---|---|
| `runner` | Override the agent's runner (must exist in `runners[]`) |
| `model` | Override the agent's model |
| `fallbacks` | Replace the agent's fallback chain entirely |
| `fallback_strategy` | Override the agent's fallback strategy |

Profile overrides are applied **after** the base config is loaded and **before**
runner instantiation, so the dispatcher builds runners from the overridden
values. Rate-limit and budget pause states are shared across all profiles.

### Use cases

| Scenario | Profile |
|---|---|
| Primary provider (Codex) out of credits | `--profile=opencode` — move all agents to opencode-go |
| Budget-conscious maintenance | `--profile=cheap` — smaller/cheaper models for non-critical agents |
| Deep reasoning batch | `--profile=deep` — up-model to reasoning models for complex tasks |
| Emergency provider failover | `--profile=fallback` — instantly divert all agents to a secondary provider |

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
|---|---|---|---|
| `concurrency` | 2 | Global worker-pool size. **Informational** — real dispatch concurrency is the sum of per-agent `max_workers` |
| `log_level` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `state_lock` | `false` | Add an `in-progress` label on the source item when work starts |
| `result_comment` | `false` | Post the agent's final output back as a comment |
| `task_timeout` | `30m` | Default per-run timeout (e.g. `2h`) |
| `max_attempts` | 3 | Stop re-dispatching a `(task, workflow)` after this many **consecutive failed** instances; `<=0` disables — see [resilience](resilience.md#re-dispatch-failure-cap) |
| `default_fallbacks` | — | Global [fallback chain](resilience.md#fallback-chains) inherited by any agent without explicit `fallbacks[]` |
| `credit_exhausted_cooldown` | `24h` | How long a runner type is paused after a credit-exhausted failure — see [resilience](resilience.md#runner-failures--failover) |
| `log_max_size_mb` | 50 | Rotate `apiary.log` past this size (MB); negative disables rotation |
| `log_max_backups` | 5 | Rotated files kept as `apiary.log.1` … `.N`, oldest dropped; negative keeps none |
| `log_max_age_days` | 30 | Prune rotated backups, per-task logs (`logs/tasks/*.log`) **and** SQLite log rows (`service_logs`/`task_logs`) older than this; negative disables |

### Log rotation

The daemon writes its service log to `<config-dir>/.apiary/logs/apiary.log`
and one file per task under `logs/tasks/`. Without limits these grow
unbounded, so rotation is on by default: when a write would push `apiary.log`
past `log_max_size_mb`, the file is renamed to `apiary.log.1` (older backups
shift up, the one past `log_max_backups` is dropped) and a fresh file is
started. Backups and task-log files untouched for `log_max_age_days` are
deleted.

The same log stream is persisted to SQLite for the dashboard (`service_logs`
and `task_logs` in `apiary.db`), and those tables get the same retention: rows
older than `log_max_age_days` are deleted at daemon startup and then daily,
in small batches so log writes are never blocked for long. Older lines remain
readable in the rotated log files. Deleting rows lets SQLite reuse the freed
pages — the file stops growing but does not shrink; to compact a database
that grew before retention existed, stop the daemon and run
`sqlite3 .apiary/apiary.db 'VACUUM;'` once.

### Git hook enforcement (`git_hooks`)

Prompt-level rules like "always run the tests before pushing" are advisory —
under a long run an agent will eventually skip them, and the cost shows up
later as CI-failure retry loops. `git_hooks` makes the rule mechanical: at
startup the daemon points `core.hooksPath` of every repo matched by `repos`
at the shared `dir`, so a `pre-push` script there (running the project's
local lint/tests, or refusing pushes from a branch behind `main`) physically
rejects the push with a non-zero exit, whatever the agent decided.

```yaml
settings:
  git_hooks:
    dir: .apiary/hooks        # holds pre-push, pre-commit, ... scripts
    repos:
      - ../myproject--*       # the agents' per-role checkouts (glob)
```

| Field | Default | Description |
|---|---|---|
| `dir` | — | Directory with the hook scripts; absolute, `~`-prefixed, or relative to the daemon working directory. Scripts are made executable automatically |
| `repos` | — | Glob patterns of git checkouts to enforce on. Matches without a `.git` are skipped silently; both fields are required together |

Enforcement is re-applied on every daemon start, so newly-cloned checkouts
matching the glob pick the hooks up on the next restart. Hooks live in one
shared directory — editing a script updates every repo at once. Note that
`core.hooksPath` replaces the per-repo `.git/hooks` directory entirely for
the configured repos.

### Cursor cost back-fill (`cursor_cost`)

The Cursor agent CLI streams token counts but **no dollar cost** — unlike the
Claude CLI's `total_cost_usd`, cursor runs always record `$0.00`. If your
Cursor plan is usage-based, the daemon can recover the real billed amounts
from the same private API the [cursor.com dashboard](https://cursor.com/dashboard)
usage tab uses, and back-fill `cost_usd` on finished `cursor-cli` executions:

```yaml
settings:
  cursor_cost:
    enabled: true
    session_token: ${CURSOR_SESSION_TOKEN}  # from .env beside apiary.yaml
    interval: 5m   # sweep cadence (default)
    max_age: 72h   # how far back to back-fill (default)
```

| Field | Default | Description |
|---|---|---|
| `enabled` | `false` | Enable the back-fill sweep (runs at startup, then every `interval`) |
| `session_token` | — | `WorkosCursorSessionToken` cookie value; empty falls back to the `CURSOR_SESSION_TOKEN` env var |
| `interval` | `5m` | Time between sweeps (minimum `1m`) |
| `max_age` | `72h` | Unpriced executions older than this are left alone |
| `team_id` | `0` | **Required for team-billed accounts** (the usual per-usage setup) — without it the API returns no events. `0` = personal account |
| `user_id` | `0` | On team accounts, filter to your own events so teammates' activity cannot pollute attribution. Strongly recommended with `team_id` |

#### Getting the token

The bundled script does everything — extracts the CLI's long-lived session
token (macOS keychain, Linux secret-service, or the CLI's `auth.json`
fallback), discovers `team_id`/`user_id` from `cli-config.json`, verifies the
credentials live against the dashboard API, and writes `CURSOR_SESSION_TOKEN`
into your `.env` (idempotent — re-run it any time the token expires). It
prints the matching `settings.cursor_cost` block to paste into `apiary.yaml`.

Daemon on the same machine as the Cursor CLI:

```bash
scripts/cursor-cost-setup.sh /path/to/your/.env
```

Daemon on a **different machine** than the Cursor login — export there,
import here:

```bash
# on the machine where the Cursor CLI is logged in:
scripts/cursor-cost-setup.sh --export cursor-creds.env
scp cursor-creds.env daemon-host:

# on the daemon machine:
scripts/cursor-cost-setup.sh --import cursor-creds.env /path/to/daemon/.env
rm cursor-creds.env   # the file holds a live session token — delete after import
```

(`--import -` reads the payload from stdin, so a one-shot
`ssh cursor-host 'apiary/scripts/cursor-cost-setup.sh --export' | scripts/cursor-cost-setup.sh --import - .env`
also works.) Manual equivalent, if you prefer:

```bash
# macOS — build the cookie value from the CLI's stored session
TOKEN=$(security find-generic-password -s cursor-access-token -w)
AUTH_ID=$(python3 -c "import json;print(json.load(open('$HOME/.cursor/cli-config.json'))['authInfo']['authId'].split('|')[1])")
echo "CURSOR_SESSION_TOKEN=${AUTH_ID}%3A%3A${TOKEN}" >> .env

# team_id / user_id for apiary.yaml:
python3 -c "import json;a=json.load(open('$HOME/.cursor/cli-config.json'))['authInfo'];print('team_id:',a.get('teamId',0));print('user_id:',a.get('userId',0))"
```

Browser alternative: log into [cursor.com/dashboard](https://cursor.com/dashboard)
**with the same account the CLI uses**, open DevTools (F12) → Application →
Cookies → `https://cursor.com`, and copy the `WorkosCursorSessionToken` value.
(The cookie is HttpOnly — a console `document.cookie` snippet cannot read it.)
Browser cookies last ~60 days; when one expires the sweep logs an auth warning
and the daemon keeps running (costs simply stop back-filling until refreshed).

How it works, and the fine print:

- One cursor-agent invocation produces exactly **one** usage event whose token
  counts equal the CLI's reported usage digit for digit (verified live). So
  attribution is **fingerprint-first**: an event whose token tuple exactly
  matches a run's recorded usage belongs to that run — concurrent runs with
  fully overlapping time windows are disambiguated reliably.
- Runs without a usable fingerprint fall back to time-window matching
  (`started_at..completed_at` ±2min): an event inside exactly one window is
  attributed; an event inside several is attributed to *neither* (wrongly
  attributing money is worse than undercounting), logged as ambiguous. Runs
  that never resolve are abandoned after 10 sweeps.
- **Interactive IDE usage is indistinguishable from CLI runs** (Cursor's
  `isHeadless` flag is `false` for both — verified live). Fingerprint matches
  are immune to it; window-fallback attributions are cross-checked against the
  run's own token counts and skipped when they exceed them by more than 50%.
- The endpoint is **undocumented** and may change without notice — the whole
  feature is best-effort and degrades to "no cost recorded" on any failure.
- Plan-included requests (`INCLUDED_IN_PRO` etc.) and errored/aborted requests
  are counted as $0, which is the real marginal charge.

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
