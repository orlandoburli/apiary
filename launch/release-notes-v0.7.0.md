# Apiary v0.7.0 — First Public Beta 🐝

**Workflows that wait their turn.**

Apiary routes work from your issue tracker to the right AI agent — declaratively, reliably,
and unattended. Today we're releasing the first public beta, v0.7.0. It's been running in
production internally for two months. Now it's ready for you.

## Why now

Apiary has been in private beta for months, used internally to dispatch real work. v0.7.0
is the first release we're confident shipping publicly. It includes all the reliability
work: dependency blocking, cost tracking, memory across runs, and a dashboard to see what's
happening.

- **`wait_for kind: dependency` — gate on upstream status.** A step can now wait for
  specific upstream tasks to succeed before it runs. Link tasks via GitHub's `blocked_by`
  relationship or Jira's `Blocks` link. The wait parks the task and auto-resumes when the
  blocker completes — no polling, no retries, just work in the right order. Survives daemon
  restarts.
- **Task logging clarity.** Every task log now includes a human-facing source reference
  (issue URL, PR, Jira key) at the top, so you know exactly which work item a run came from
  without digging through logs.
- **Expression language documentation.** The workflow condition language (`${{ expr }}`)
  now has full docs: syntax, operators, variable scope, and common patterns. Build
  sophisticated gating logic with confidence.

The headline feature in one block:

```yaml
workflows:
  - id: code-review-pipeline
    trigger:
      kind: github
      on: issues.opened

    steps:
      - id: design
        agent: architect

      - id: implement
        agent: engineer
        kind: dependency
        wait_for_task: design-task
        on_conflict:
          goto: design
          max_retries: 3

      - id: test
        agent: qa
        kind: dependency
        wait_for_task: implement-task
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

## What's included

v0.7.0 is the culmination of six months of work:

- **Dependency blocking** — `wait_for kind: dependency` gates downstream steps on upstream
  completion, with parked state that survives restarts.
- **Persistent agent memory** — agents remember facts across runs via `APIARY_MEMORIZE`.
- **Cost tracking** — see token usage, cache hits, and costs per step and per agent.
- **Resilience primitives** — rate-limit failover, re-dispatch caps, approval gates, CI
  waits.
- **Dashboard** — live task view, cost rollup, log streaming, instance history.
- **Multiple source adapters** — GitHub Issues, Plane, Jira, with full comment rendering and
  dependency tracking.

This is a real, working system. But APIs may evolve before v1.0 — we're marking it beta so
you know to pin versions if you depend on it.

## Supported integrations

### Sources (where work comes from)

| System | Status | Auth | Comments |
|--------|--------|------|----------|
| **GitHub Issues** | ✅ Stable | Personal access token | Polls issues, reads PR status for CI waits, writes back via commits/PRs |
| **Plane** | ✅ Stable | API key | Full integration with states, labels, and custom fields |
| **Jira Cloud** | ✅ Stable | API token | Supports JQL search, status transitions, blocking relationships |

### Runners (how agents execute)

| Provider | Type | Status | Notes |
|----------|------|--------|-------|
| **Claude CLI** | Local subprocess | ✅ Stable | Requires `claude` binary; structured event parsing for logs & costs |
| **OpenCode CLI** | Local subprocess | ✅ Stable | Requires `opencode` binary; fallback-chain support |
| **Cursor CLI** | Local subprocess | ✅ Stable | Via the `agent` binary; integrates with Cursor's auth |
| **OpenCode API** | Direct HTTP API | ✅ Stable | Calls OpenCode Cloud API directly; requires API key |

### Adapters roadmap

Coming soon:
- **Linear** — source adapter (in development)
- **Anthropic API** — direct runner (in development)

> **Beta caveats.** Apiary works end-to-end and is in active use, but config and adapter
> APIs may still change before `v1.0` — pin a version for anything you depend on. Binaries
> are not yet code-signed/notarized (the Homebrew cask clears the macOS quarantine flag for
> you). [File issues](https://github.com/orlandoburli/apiary/issues) for anything rough.

Dual-licensed: [AGPLv3](https://github.com/orlandoburli/apiary/blob/main/LICENSE) for
open-source/internal use; a [commercial license](https://github.com/orlandoburli/apiary/blob/main/COMMERCIAL.md)
is available for proprietary/SaaS deployments.
