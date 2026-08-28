# Supported Integrations

Apiary works with multiple source systems (where work comes from) and runner providers (how agents execute).

## Source Adapters

Sources poll for tasks and write results back. Apiary reads issue status, comments, and metadata, then updates labels, states, and posts comments after agents complete work.

### GitHub Issues

**Status:** ✅ Stable | **Auth:** Personal access token

Polls open issues from a GitHub repository. Apiary reads pull request status for CI waits, cross-references issues and PRs via the issue timeline, and writes back via commits and pull requests.

- **Requirements:** Repository access via GitHub personal access token
- **Blocking relationships:** Uses GitHub's `blocked_by` issue link type
- **Comment rendering:** Markdown comments with embedded logs and cost summaries
- **Setup:** See [GitHub Source configuration](github-source.md)

### Plane

**Status:** ✅ Stable | **Auth:** API key

Polls issues from a Plane project. Supports custom states, labels, and relationships.

- **Requirements:** Plane workspace with project access via API key
- **Blocking relationships:** Uses Plane's native blocking link type
- **Comment rendering:** Markdown comments
- **Setup:** See [Plane Source configuration](plane-source.md)

### Jira Cloud

**Status:** ✅ Stable | **Auth:** API token

Polls Jira Cloud via JQL search. Supports status transitions, blocking relationships, and labels.

!!! note
    Jira Server and Data Center are not supported — only Jira Cloud.

- **Requirements:** Jira Cloud site with user access via API token
- **Blocking relationships:** Uses Jira's `Blocks` link type (reads the inward "is blocked by" side)
- **Comment rendering:** ADF (Atlassian Document Format) for rich formatting
- **Setup:** See [Jira Source configuration](jira-source.md)

### Prometheus Alertmanager

**Status:** ✅ Stable | **Auth:** Bearer token or Basic auth (optional)

Polls firing alerts from Prometheus Alertmanager and maps them to tasks, so
workflows can be triggered by operational signals (an alert fires → an
investigation agent dispatches). Read-only: alerts have no state, labels, or
comments to write back — publish findings to a ticket source instead.

- **Requirements:** Reachable Alertmanager `/api/v2/alerts` endpoint
- **Guardrails:** Alert-storm dispatch cap and flap dampening built in
- **Write-back:** None (read-only source) — `apiary validate` rejects incompatible workflow features
- **Setup:** See [Prometheus Source configuration](prometheus-source.md)

### Dynatrace

**Status:** ✅ Stable | **Auth:** API token (`problems.read` scope)

Polls open problems from the Dynatrace problems API (`/api/v2/problems`) and
maps them to tasks, following the same read-only monitoring-source shape as
the Prometheus adapter: an AI-detected problem opens → an investigation
workflow dispatches.

- **Requirements:** Dynatrace SaaS or Managed environment with an access token
  holding the `problems.read` scope
- **Guardrails:** Problem-storm dispatch cap and flap dampening built in
- **Write-back:** None (read-only source) — `apiary validate` rejects incompatible workflow features
- **Setup:** See [Dynatrace Source configuration](dynatrace-source.md)

### Custom sources via plugins

**Status:** ✅ Stable | **Auth:** Plugin-defined

Any system can become a poll source without a fork: ship an out-of-process
`source`-capability plugin (protocol 1, any language) and bridge it with
`type: plugin`. The daemon invokes the plugin on the source's poll interval;
items flow through normal workflow trigger matching.

- **Requirements:** Plugin installed in `plugin_dirs` and enabled under `plugins:`
- **Write-back:** None (read-only source) — `apiary validate` rejects incompatible workflow features
- **Setup:** `apiary plugins search --capability source` for published ones, or see [Source plugins](plugins.md#source-plugins) and the `source-file` reference plugin

### Linear

**Status:** 🔄 Planned | **Auth:** API token

Support for Linear is in development.

---

## Runners

Runners define **how** agents execute: either as a CLI subprocess on your machine or via a direct API call.

### CLI Runners

CLI runners invoke the agent tool as a subprocess. The tool runs on your machine with full access to your credentials, so Apiary never handles auth secrets — they stay with the tool.

#### Claude CLI

**Status:** ✅ Stable | **Provider:** `claude`

Runs the `claude` binary as a subprocess for each step.

- **Requirements:** `claude` CLI installed and authenticated
- **Structured parsing:** Apiary parses Claude's event stream into readable `[assistant]` / `[tool→ …]` logs
- **Exact costs:** Token counts and costs come from Claude's structured response, not estimates
- **Setup:** `type: cli`, `provider: claude` in `runners`
- **Docs:** [Runners configuration](runners.md#claude-provider-claude)

#### OpenCode CLI

**Status:** ✅ Stable | **Provider:** `opencode`

Runs the `opencode` binary as a subprocess.

- **Requirements:** `opencode` CLI installed and authenticated
- **Setup:** `type: cli`, `provider: opencode` in `runners`
- **Docs:** [Runners configuration](runners.md#opencode-provider-opencode)

#### Codex CLI

**Status:** ✅ Stable | **Provider:** `codex`

Runs the OpenAI `codex` binary as a subprocess using `codex exec`.

- **Requirements:** `codex` CLI installed and authenticated
- **Skills:** Codex reads checked-in skills from `.agents/skills`
- **Setup:** `type: cli`, `provider: codex` in `runners`
- **Docs:** [Runners configuration](runners.md#codex-provider-codex)

#### Cursor CLI

**Status:** ✅ Stable | **Provider:** `cursor`

Runs the Cursor `agent` binary as a subprocess.

- **Requirements:** `agent` binary on PATH (installed with Cursor)
- **Setup:** `type: cli`, `provider: cursor` in `runners`
- **Docs:** [Runners configuration](runners.md#cursor-provider-cursor)

### API Runners

API runners call a provider's API directly, without requiring the tool to be installed locally.

#### OpenCode API

**Status:** ✅ Stable | **Type:** `opencode-api`

Calls the OpenCode Cloud API directly.

- **Requirements:** OpenCode API key
- **Setup:** `type: opencode-api` in `runners` with `config.api_key`
- **Supported models:** `opencode-go/deepseek-v4-pro` and others
- **Docs:** [Runners configuration](runners.md#opencode-api)

### Anthropic API

**Status:** 🔄 Planned

Direct API runner for Claude models via the Anthropic API is in development.

---

## Feature support matrix

| Feature | GitHub | Plane | Jira | OpenCode API | Claude CLI | OpenCode CLI | Codex CLI | Cursor CLI |
|---------|--------|-------|------|---|---|---|---|---|
| Polling | ✅ | ✅ | ✅ | — | N/A | N/A | N/A | N/A |
| State transitions | ✅ | ✅ | ✅ | — | N/A | N/A | N/A | N/A |
| Blocking relationships | ✅ | ✅ | ✅ | — | N/A | N/A | N/A | N/A |
| Comment rendering | ✅ | ✅ | ✅ | — | N/A | N/A | N/A | N/A |
| CI waits | ✅ | — | — | — | N/A | N/A | N/A | N/A |
| Structured logs | — | — | — | ✅ | ✅ | ✅ | ✅ | ✅ |
| Exact cost tracking | — | — | — | ✅ | ✅ | — | — | — |

---

## Using multiple adapters

You can configure multiple sources and runners in the same `apiary.yaml`:

```yaml
sources:
  - id: github-issues
    type: github
    config:
      repo: my-org/my-repo
      api_key: ${GITHUB_TOKEN}

  - id: jira-backlog
    type: jira
    config:
      base_url: https://company.atlassian.net
      email: bot@company.com
      api_token: ${JIRA_API_TOKEN}

runners:
  - id: claude
    type: cli
    provider: claude

  - id: opencode
    type: opencode-api
    config:
      api_key: ${OPENCODE_API_KEY}

agents:
  - id: engineer
    runner: claude
    model: claude-sonnet-4-6

  - id: fallback
    runner: opencode
    model: opencode-go/deepseek-v4-pro
```

Workflows can route to different agents, which run on different runners. Tasks from any source can be dispatched to any agent.

---

## Companion tools

Separate projects that work with Apiary without extending it. Unlike
[plugins](plugin-directory.md), these are not invoked by the daemon and do not
speak protocol 1 — they are their own services, with their own releases.

### apiary-pgsink

**Status:** ✅ Stable | **Runs:** beside the daemon, as its user

Replicates Apiary's SQLite database into PostgreSQL: `backfill` loads the
history, `sync` follows it. Per-table filters and injected columns, so a shared
target can hold several Apiary installations and carry your own tenant or
environment labels.

- **Why:** reporting and BI. The sink also keeps rows past Apiary's own log
  retention, which makes it an archive as well as a mirror.
- **Requirements:** the same host as the daemon, running as its user — SQLite in
  WAL mode has no true read-only reader
- **Install:** [releases](https://github.com/orlandoburli/apiary-pgsink/releases),
  or `ghcr.io/orlandoburli/apiary-pgsink`
- **Home:** [apiary-pgsink](https://github.com/orlandoburli/apiary-pgsink) ·
  [docs](https://orlandoburli.com.br/apiary-pgsink/)

---

## Planned integrations

| System | Type | Status |
|--------|------|--------|
| Linear | Source | 🔄 In development |
| Anthropic API | Runner | 🔄 In development |
| Mistral | Runner | 📝 Planned |
| Ollama | Runner | 📝 Planned |

Have a request? [Open an issue](https://github.com/orlandoburli/apiary/issues).
