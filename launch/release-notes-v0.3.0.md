# Apiary v0.3.0 — CI-aware pipelines, full cost visibility 🐝

Apiary routes work from your issue tracker (GitHub Issues, Plane, Jira, Linear) to the
right AI agent and model — declaratively — and leaves it running. v0.3.0 makes the
pipeline **CI-aware** and makes every token it spends **visible**.

## Why this release matters

v0.2.0 made Apiary safe to run unattended. v0.3.0 closes the two gaps you hit next when
agents ship real PRs: waiting on CI without burning a worker, and knowing what each step
actually cost.

- **`wait_for` steps — native CI waits.** A workflow can park itself while CI runs:
  active polling against the GitHub API (no webhooks), configurable interval and timeout,
  and the park **survives daemon restarts**. Failed CI loops back to the step you choose
  (`on_reject`); per-poll history is visible in `apiary instances` and `apiary task`.
  Check-runs are aggregated, so Actions-only repos report correctly.
- **Merge-conflict routing.** When the PR under a `wait_for` develops a merge conflict,
  `on_conflict` stops the CI wait immediately and routes back (e.g. to the implement
  step for a rebase) — with its own retry budget, separate from the CI-failure budget.
- **Per-step cost accounting.** Every step run records token usage (with
  `cache_creation`/`cache_read` tracked separately), cost, and the prompts that produced
  it. Captured for Claude CLI and Cursor runners. The dashboard rolls it up: daily usage
  bars, share-of-total per agent, and a timings rollup per task.
- **MCP servers for CLI agents.** Expose MCP servers to `claude`, `opencode` and
  `cursor` runners straight from `apiary.yaml` — agents get your tooling, not just a
  prompt.
- **Self-healing daemon.** Orphaned workflow instances *and* step runs are reconciled at
  startup; parked approvals and CI waits are re-checked concurrently (one slow task no
  longer delays the rest); a finished task reopens when a later stage is dispatched; and
  the `APIARY_OUTPUT` sentinel is parsed even when an agent wraps it in markdown fences —
  no more verdicts silently lost to formatting.
- **GitHub source hardening.** The PR behind an issue is found via agent branch name,
  issue-timeline cross-references, and search — not just number matching. PR-fetch auth
  errors surface immediately instead of masquerading as an endless "pending".
- **Dashboard quality-of-life.** Live-refreshing task detail and logs, much faster first
  log load, `p` opens the task's latest PR, scrollable detail view, accurate
  failed-vs-completed states, clearer usage charts.

The headline feature in one block:

```yaml
steps:
  - id: implement
    agent: engineer

  - id: check-ci
    type: wait_for
    wait_for:
      kind: ci
      check_interval: 30s
      max_duration: 2h
      fail_if_not_passed: true
    on_reject:            # CI failed → fix and re-push
      restart_from: implement
      max: 5
    on_conflict:          # PR conflicted → rebase, separate budget
      goto: implement
      max_retries: 5
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

Upgrading from v0.2.0 needs no config changes — everything in this release is additive.
The SQLite store migrates itself on first start (timestamps are normalized and
backfilled automatically).

> **Beta caveats.** Apiary works end-to-end and is in active use, but config and adapter
> APIs may still change before `v1.0` — pin a version for anything you depend on. Binaries
> are not yet code-signed/notarized (the Homebrew cask clears the macOS quarantine flag for
> you). [File issues](https://github.com/orlandoburli/apiary/issues) for anything rough.

Dual-licensed: [AGPLv3](https://github.com/orlandoburli/apiary/blob/main/LICENSE) for
open-source/internal use; a [commercial license](https://github.com/orlandoburli/apiary/blob/main/COMMERCIAL.md)
is available for proprietary/SaaS deployments.
