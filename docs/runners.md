# Runners

A runner defines **how** agents execute. Agents decide *what* to do (model,
persona, skills); the runner is the engine that actually launches the LLM —
either as a **CLI subprocess** on the host or as a direct **API call**.

```yaml
runners:
  - id: claude
    type: cli
    provider: claude

default_runner: claude

agents:
  - id: engineer
    runner: claude            # ← references the runner above
    model: claude-sonnet-4-6
```

Several agents can share one runner — each gets its own configured instance,
so per-agent MCP servers and identity don't leak between them.

| Field | Description |
|---|---|
| `id` | Unique identifier, referenced by `agents[].runner` and `agents[].fallbacks` |
| `type` | Execution engine: `cli` (subprocess) — or a self-contained adapter name like `opencode-api` |
| `provider` | Which CLI the `cli` engine drives: `claude`, `opencode`, `codex`, `cursor` |
| `config` | Engine options — see [CLI engine configuration](#cli-engine-configuration) |
| `models` | Models this runner supports (informational) |
| `mcps` | MCP servers exposed to every agent on this runner — see [MCP servers](#mcp-servers) |

**How `type` and `provider` resolve.** Apiary picks the adapter by combining
them as `{provider}-{type}` (or just `type` when `provider` is empty). The
registered adapters are:

| Adapter | YAML | Launches |
|---|---|---|
| `claude-cli` | `type: cli`, `provider: claude` | the `claude` binary |
| `opencode-cli` | `type: cli`, `provider: opencode` | the `opencode` binary |
| `codex-cli` | `type: cli`, `provider: codex` | the `codex` binary |
| `cursor-cli` | `type: cli`, `provider: cursor` | the `agent` binary (Cursor CLI) |
| `opencode-api` | `type: opencode-api` | HTTP calls to the OpenCode API |

Any other combination is rejected by `apiary validate` (and by the daemon's
config check at startup) with an error listing the valid combinations.

## CLI runners

A `cli` runner invokes the agent tool as a subprocess for each step, streams
its stdout/stderr into the task logs, tracks its PID and heartbeats, and
enforces the run timeout (`settings.task_timeout`). The subprocess inherits
the daemon's environment plus the
[env overlay](configuration.md#environment-variables) and the agent's
[git/source identity](configuration.md#agent-identity).

!!! note "Credentials stay with the tool"
    Apiary never handles, stores, or transmits provider credentials for CLI
    runners. The `claude` / `opencode` / `agent` binaries authenticate exactly
    as they do when you run them by hand — Apiary just invokes them. This is
    what makes the `cli` type a good fit for personal machines where the
    tools are already logged in.

### Providers

Each provider preconfigures the engine — binary name, default args, how the
prompt and model are passed, and the MCP format. `config` is only for
overrides; a bare `provider:` line is a fully working runner.

#### Claude (`provider: claude`)

Requires the `claude` binary on `PATH`.

```yaml
runners:
  - id: claude
    type: cli
    provider: claude
```

The preset runs `claude --output-format stream-json --verbose`: Apiary parses
Claude's structured event stream into

- readable `[assistant]` / `[tool→ …]` / `[tool← result]` lines in the
  [task logs](dashboard.md#watching-the-live-conversation-debug-mode),
- exact token counts and `cost_usd` per run (reported, not estimated),
- `rate_limit_event` detection, which drives
  [failover](resilience.md#runner-failures--failover).

#### OpenCode (`provider: opencode`)

Requires the `opencode` binary on `PATH`. The preset invokes
`opencode run …` with the prompt as a positional argument.

```yaml
runners:
  - id: opencode
    type: cli
    provider: opencode
```

#### Codex (`provider: codex`)

Requires the OpenAI Codex CLI (`codex` binary) on `PATH`. The preset invokes
`codex exec --sandbox workspace-write …` with the prompt as a positional
argument.

```yaml
runners:
  - id: codex
    type: cli
    provider: codex
```

Codex discovers repository skills from `.agents/skills`. If you already keep
skills under `.claude/skills`, create `.agents/skills` as a symlink to reuse the
same `SKILL.md` files without copying them.

#### Cursor (`provider: cursor`)

Requires the Cursor CLI (`agent` binary, from
[cursor.com/install](https://cursor.com/install)). The preset runs it
headlessly: `agent -p --output-format stream-json --force` (with `--force`
auto-approving file changes) and passes the prompt positionally.

```yaml
runners:
  - id: cursor
    type: cli
    provider: cursor
```

!!! note "Cost reporting"
    The Cursor CLI streams token counts but no dollar cost, so cursor runs
    show `$0.00` out of the box. On usage-based Cursor plans the daemon can
    back-fill real billed amounts from the Cursor dashboard API — see
    [`settings.cursor_cost`](configuration.md#cursor-cost-back-fill-cursor_cost).

### CLI engine configuration

All `config` keys, with each provider's preset defaults:

| Key | Description | claude | opencode | codex | cursor |
|---|---|---|---|---|---|
| `command` | Binary to invoke (override to use a wrapper or absolute path) | `claude` | `opencode` | `codex` | `agent` |
| `args` | Extra argv appended after the preset's args | `--output-format stream-json --verbose` | `run` | `exec --sandbox workspace-write` | `-p --output-format stream-json --force` |
| `model_flag` | Flag used to pass the step's model | `--model` | `--model` | `--model` | `--model` |
| `prompt_flag` | Flag used to pass the prompt | `-p` | *(positional)* | *(positional)* | *(positional)* |
| `prompt_positional` | Pass the prompt as the last positional argument | `false` | `true` | `true` | `true` |
| `turns_flag` | Flag used to pass a max-turns limit | — | — | — | — |

Prompt delivery: via `prompt_flag` when set, as the last positional argument
when `prompt_positional: true`, otherwise on **stdin**.

## API runners

API runners skip the subprocess and POST to a chat-completions endpoint
directly. The built-in adapter is `opencode-api`:

```yaml
runners:
  - id: opencode-api
    type: opencode-api
    config:
      api_key: ${OPENCODE_API_KEY}
      # base_url: https://api.opencode.ai/v1   # optional override
    models:
      - opencode-go/deepseek-v4-pro
      - opencode-go/minimax-m3
```

| Key | Description |
|---|---|
| `api_key` | Sent as `Authorization: Bearer <key>` |
| `base_url` | Override the API base; `/chat/completions` is appended |
| `endpoint` | Full URL override (takes precedence over `base_url`) |

The request/response shape is OpenAI-compatible chat completions. API runners
are the right choice for shared or headless deployments where no
pre-authenticated CLI tool exists on the host.

!!! warning
    An API runner sends the task prompt to the provider directly. CLI tools
    that do their own context-gathering (file reads, shell, MCP) are far more
    capable for implementation work — prefer `cli` runners for coding agents
    and API runners for lightweight classification or as fallback capacity.

## MCP servers

Runners (and individual agents) can expose
[Model Context Protocol](https://modelcontextprotocol.io) servers to the CLI
tools they launch:

```yaml
runners:
  - id: claude
    type: cli
    provider: claude
    mcps:
      - name: gitnexus
        command: npx
        args: ["-y", "gitnexus@latest", "mcp"]
        # env:                       # optional; ${VAR} expanded at load
        #   GITNEXUS_TOKEN: ${GITNEXUS_TOKEN}

agents:
  - id: qa
    runner: claude
    model: claude-sonnet-4-6
    mcps:                            # agent-scope: merged over the runner's
      - name: playwright
        command: npx
        args: ["-y", "@playwright/mcp@latest"]
```

Runner-level `mcps` apply to every agent on the runner; agent-level `mcps`
are layered on top — same `name` overrides, new names are appended. Each
provider receives the merged list in its own native format:

| Provider | Mechanism |
|---|---|
| claude | temp `.mcp.json` passed with `--mcp-config <path>` (trusted, no approval prompt, no workdir mutation) |
| cursor | merged into `~/.cursor/mcp.json`, activated with `--approve-mcps` |
| opencode | merged into the global `opencode.json` `mcp` block |
| codex | merged into `~/.codex/config.toml` under Apiary-managed `[mcp_servers.*]` tables |

The MCP configs are materialized once at daemon startup, not per run.

## Fallbacks: runners as failover capacity

An agent's `fallbacks` chain lists alternative `{runner, model}` pairs to try
when its primary runner is rejected by a provider rate limit:

```yaml
agents:
  - id: engineer
    runner: claude
    model: claude-sonnet-4-6
    fallbacks:
      - {runner: opencode, model: opencode-go/deepseek-v4-pro}
      - {runner: cursor, model: composer-2.5-fast}
```

Every entry must reference a defined runner; `model` is optional (empty uses
that runner's default). Fallback adapters are instantiated and configured at
startup — including their MCP setup — so failover never pays an
initialization cost on the hot path. The full behavior is described in
[Rate Limits & Resilience](resilience.md#runner-failures--failover).

## Adding a provider

Runner adapters are small: a provider registers a factory that preconfigures
one of the generic engines (subprocess or HTTP). See the
[Plugin API spec](https://github.com/orlandoburli/apiary/blob/main/openspec/specs/plugin-api/spec.md)
and `src/internal/runner/providers/` for the current registrations —
contributions are welcome.
