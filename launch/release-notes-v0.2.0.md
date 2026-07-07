# Apiary v0.2.0 — Public Beta 🐝

Apiary's first public beta. **Task-driven agent orchestration**: route work from your
issue tracker (GitHub Issues, Plane, Jira, Linear) to the right AI agent and model —
declaratively — and leave it running.

```
Task System ──► Apiary ──► Agent ──► LLM Model
  (GitHub)      (router)  (backend-dev)  (claude-sonnet-4-6)
```

## Why this release matters

v0.2.0 is the first build we're comfortable telling people to run unattended. It ships
the safeguards that keep a saturated provider or a failing task from turning into a
runaway, money-burning loop:

- **Rate-limit failover** — when a provider rejects a run on a usage limit, Apiary pauses
  that runner type until it resets and fails over to the agent's next `fallbacks` entry,
  instead of burning the task on a pre-failed call.
- **Re-dispatch cap** (`settings.max_attempts`) — a task whose workflow keeps failing stops
  being re-dispatched after N consecutive failures. No infinite retry loops.
- **Non-blocking dispatch** — a busy agent parks its own runs without stalling polling or
  dispatch for any other source or agent.
- **Approval rehydration** — runs parked on a manual-approval gate survive a daemon restart.
- **Scoped env vars** — set environment variables per agent, workflow, or step, merged with
  precedence.
- **Schema-validated config** — `apiary.yaml` is validated against a JSON Schema, with live
  validation in the VS Code extension (**Apiary Workflow Visualizer**).

## Heads-up: `routes:` → `workflows[].trigger`

Routing now lives entirely in workflows. The one-step workflow is the direct replacement
for the old `route`:

```yaml
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

## Install

```sh
# macOS / Linux — Homebrew
brew install --cask orlandoburli/tap/apiary

# Windows — Scoop
scoop bucket add orlandoburli https://github.com/orlandoburli/scoop-bucket
scoop install apiary

# Docker (multi-arch)
docker run --rm -v apiary-data:/data ghcr.io/orlandoburli/apiary:latest version
```

`.deb`/`.rpm` packages and direct downloads (with `checksums.txt`) are attached below.

> **Beta caveats.** Apiary works end-to-end and is in active use, but config and adapter
> APIs may still change before `v1.0` — pin a version for anything you depend on. Binaries
> are not yet code-signed/notarized (the Homebrew cask clears the macOS quarantine flag for
> you). [File issues](https://github.com/orlandoburli/apiary/issues) for anything rough.

Dual-licensed: [AGPLv3](https://github.com/orlandoburli/apiary/blob/main/LICENSE) for
open-source/internal use; a [commercial license](https://github.com/orlandoburli/apiary/blob/main/COMMERCIAL.md)
is available for proprietary/SaaS deployments.
