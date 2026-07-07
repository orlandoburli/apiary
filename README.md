# Apiary

<p align="center">
  <img src="docs/logo.svg" alt="Apiary logo" width="600">
</p>

**Task-driven agent orchestration. Route work from any task system to the right AI agent and model.**

Apiary is an open-source harness that connects project management tools (Jira, Plane, GitHub Issues, Linear) to AI agent runners (Claude, OpenCode, Codex, Cursor) and routes each task to the right agent profile and LLM model based on declarative rules.

```
Task System ──► Apiary ──► Agent ──► LLM Model
  (GitHub)      (router)  (backend-dev)  (claude-sonnet-4-6)
```

## Why Apiary?

Most AI coding agents require a human to manually pick a task, paste context, and trigger a run. Apiary closes the loop: tasks flow in from your PM tool, Apiary decides which agent and model handles each one, and work gets done without a human dispatcher in the middle.

## Key Concepts

| Term | Meaning |
|---|---|
| **Hive** | A configured Apiary instance (`apiary.yaml`) |
| **Source** | A task system integration that polls work items and writes results back (GitHub, Plane, Jira, Linear…) |
| **Runner** | How an agent executes — a CLI subprocess (`cli`) or API call (`api`) |
| **Agent** | A named LLM persona: runner + model + soul/skills, with optional fallbacks |
| **Workflow** | A pipeline of steps, fired by a `trigger` that matches tasks; each step runs an agent |
| **Task** | A unit of work flowing through the system (an issue or item from a Source) |
| **Agent memory** | Opt-in persistent memory — task notes + durable global facts agents save via `APIARY_MEMORIZE` and recall on later runs |

## Quick Example

```yaml
# apiary.yaml
version: "1"

runners:
  - id: claude
    type: cli
    provider: claude

sources:
  - id: main-repo
    type: github
    config:
      repo: my-org/my-repo
      api_key: ${GITHUB_TOKEN}
    poll_interval: 120s
    filters:
      labels: [ai-ready]

agents:
  - id: backend-dev
    description: "Implements backend tasks"
    runner: claude
    model: claude-sonnet-4-6

# A workflow fires when its trigger matches a task, then runs its steps.
workflows:
  - id: backend-bugs
    trigger:
      priority: 10        # lower number = evaluated first
      match:
        labels: [backend, bug]
    steps:
      - id: run
        agent: backend-dev
    on_complete:
      set_state: closed
```

```sh
apiary run
```

New here? The [Quickstart](docs/quickstart.md) walks this whole loop end to end.

## What's in the beta

Apiary runs end-to-end today and ships with the safeguards that make unattended agent dispatch safe to leave running:

- **Rate-limit failover** — when a provider rejects a run on a usage limit, Apiary pauses that runner type until it resets and fails over to the agent's next `fallbacks` entry, instead of burning the task on a pre-failed call.
- **Re-dispatch cap** (`settings.max_attempts`) — a task whose workflow keeps failing stops being re-dispatched after N consecutive failures, so a broken task can't loop forever.
- **Non-blocking dispatch** — a busy agent parks its own runs without stalling polling or dispatch for any other source or agent.
- **Approval rehydration** — runs parked on a manual-approval gate survive a daemon restart.
- **Scoped env vars** — set environment variables per agent, workflow, or step, merged with precedence.
- **Schema-validated config** — `apiary.yaml` is validated against a JSON Schema, with live validation in the VS Code extension.
- **Agent memory** — agents persist lessons (`APIARY_MEMORIZE`) as plain markdown: per-task working notes that survive retries and fan-out, plus a curated store of durable project facts recalled into every prompt.

See [Rate Limits & Resilience](docs/resilience.md) for details.

## Documentation

| Page | What's there |
|---|---|
| [Installation](docs/installation.md) | Homebrew, Scoop, Docker, deb/rpm, direct download |
| [Quickstart](docs/quickstart.md) | From install to your first autonomously handled issue |
| [Concepts](docs/concepts.md) | Vocabulary, architecture, lifecycle of a task |
| [Configuration](docs/configuration.md) | Every field of `apiary.yaml` — sources, agents, settings |
| [Runners](docs/runners.md) | CLI and API execution engines, provider presets, MCP servers |
| [Workflows](docs/workflows.md) | Triggers, steps, approval gates, CI waits, retry loops |
| [Tasks & Fan-out](docs/tasks-and-fanout.md) | `APIARY_PUBLISH`, `APIARY_SPAWN`, task-level hooks |
| [Agent Memory](docs/memory.md) | `APIARY_MEMORIZE`, task/global memory tiers, recall, curation |
| [Rate Limits & Resilience](docs/resilience.md) | Failover chains and unattended-dispatch safeguards |
| [CLI Reference](docs/cli.md) | Every command and flag |
| [Dashboard](docs/dashboard.md) | The terminal UI, tab by tab |

## Installation

> **Public beta.** Every channel below is live — Homebrew, Scoop, Docker, `.deb`/`.rpm`, and direct downloads are published on each release ([Releases](https://github.com/orlandoburli/apiary/releases)).

**macOS / Linux — Homebrew**

```sh
brew install --cask orlandoburli/tap/apiary
```

**Windows — Scoop**

```sh
scoop bucket add orlandoburli https://github.com/orlandoburli/scoop-bucket
scoop install apiary
```

**Linux — Debian/Ubuntu (`.deb`) or Fedora/RHEL (`.rpm`)**

Download the package for your architecture from the [latest release](https://github.com/orlandoburli/apiary/releases/latest), then:

```sh
sudo dpkg -i apiary_*_linux_amd64.deb      # Debian/Ubuntu
sudo rpm -i apiary_*_linux_amd64.rpm       # Fedora/RHEL
```

**Docker**

```sh
docker run --rm -v apiary-data:/data ghcr.io/orlandoburli/apiary:latest version
```

Multi-arch image (`linux/amd64`, `linux/arm64`). Persist state by mounting a volume at `/data`.

**Direct download**

Grab the archive for your OS/arch from the [latest release](https://github.com/orlandoburli/apiary/releases/latest) (`.tar.gz` for macOS/Linux, `.zip` for Windows), verify it against `checksums.txt`, extract, and put `apiary` on your `PATH`.

> **Unsigned binaries.** Releases are not yet code-signed/notarized. On **macOS**, a direct-download binary is quarantined by Gatekeeper — clear it with `xattr -dr com.apple.quarantine ./apiary` (the Homebrew cask does this automatically). On **Windows**, SmartScreen may warn "unknown publisher" — choose *More info → Run anyway*. Signing is on the roadmap.

**From source**

See the [Development guide](DEVELOPMENT.md#clone-and-build) for building with the Go toolchain.

## Specifications

All specs live in [`openspec/`](openspec/):

| Spec | Description |
|---|---|
| [Vision](openspec/specs/visao-geral/spec.md) | Overview, key concepts, workflow |
| [Architecture](openspec/specs/arquitetura/spec.md) | Technology choices and ADRs |
| [Config Schema](openspec/specs/schema/spec.md) | Full `apiary.yaml` reference |
| [Plugin API](openspec/specs/plugin-api/spec.md) | Source and Runner adapter interfaces |
| [CLI](openspec/specs/cli/spec.md) | Commands, flags, exit codes |
| [Roadmap](openspec/specs/roadmap/spec.md) | Milestone plan |

## CLI Runner — Personal Use

Apiary supports a `cli` runner adapter that invokes agent CLI tools (such as `opencode`, `gemini`, or similar) as subprocesses on your local machine.

```yaml
runners:
  - id: opencode
    type: cli
    provider: opencode        # presets: binary, prompt passing, MCP format

agents:
  - id: my-agent
    runner: opencode          # references the runner above
    model: opencode-go/deepseek-v4-pro
```

**Important:** Apiary never handles, stores, intercepts, or transmits authentication credentials of any kind. CLI tools manage their own authentication independently. The `cli` runner simply invokes the binary — it has no knowledge of how the tool authenticates.

This runner is intended for **personal use on your own machine**, where you have already set up and authenticated the CLI tool yourself. For shared or team deployments, use an API-key-based runner instead. See [Runners](docs/runners.md) for all providers and engine options.

## Dashboard

Apiary ships a terminal dashboard for monitoring task execution, agent
performance, and logs in real time:

```sh
apiary dashboard
```

See [`docs/dashboard.md`](docs/dashboard.md) for a per-tab data reference —
what each tab shows, the source table and query behind every field, the
refresh model, and current data gaps.

## VS Code extension

**Apiary Workflow Visualizer** renders a live diagram of your `apiary.yaml`
while you edit it — one flowchart per workflow, showing sources, triggers,
steps, splits, and retry loops. Install it from the Marketplace:

```
ext install orlandoburli.vscode-apiary
```

See [`docs/vscode-extension.md`](docs/vscode-extension.md) for usage and what
the diagram shows. The extension source lives under
[`tools/vscode-apiary`](tools/vscode-apiary).

## Status

> **Public beta.** Apiary works end-to-end and is in active use. Config and adapter
> APIs may still change before `v1.0` — pin a version for anything you depend on, and
> [file issues](https://github.com/orlandoburli/apiary/issues) for anything rough.

## License

Apiary is dual-licensed:

- **Open source** — [AGPLv3](LICENSE) for open-source and internal use.
- **Commercial** — a commercial license is available for proprietary products and SaaS deployments. See [COMMERCIAL.md](COMMERCIAL.md).
