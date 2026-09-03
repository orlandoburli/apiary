# Apiary — Roadmap

Status as of 2026-09-03 (latest release v0.21.0). Milestones below are
retrospective for shipped work and forward-looking from **Next** on. Anything
under **Retired** was deliberately dropped and should not be re-proposed
without new evidence.

Legend: `[x]` shipped · `[~]` partial / stub · `[ ]` open · `[-]` retired

## Shipped

### Foundation (v0.1–v0.4)

- [x] Go module, `apiary.yaml` schema + validation, structured JSON logging
- [x] Core adapter interfaces (source, runner), CLI skeleton (`run`, `validate`, `status`, `init`)
- [x] Plane, GitHub Issues, Jira Cloud, Prometheus and Dynatrace source adapters
- [x] CLI runner adapter (claude-cli, opencode-cli, cursor-cli) and opencode-api
- [x] SQLite run history, result write-back, state locking, retry with backoff, `--dry-run`
- [x] Binary distribution: GoReleaser, Homebrew tap, Scoop, `.deb`/`.rpm`, `ghcr.io` image
- [x] Documentation site (mkdocs-material + GitHub Pages)
- [x] `apiary update` self-update with daily notice

### Workflows (v0.5–v0.12)

- [x] Workflow engine: DAG, split/foreach/parallel, sub-workflows, `if/else`, approval gates
- [x] Sequential authoring v2 (implicit ordering) lowering to the DAG
- [x] `wait_for` step (CI, dependency) parked like an approval, survives restarts
- [x] InternalTask as the canonical unit of work; `APIARY_PUBLISH`, `APIARY_SPAWN`, aggregate `tasks.on_complete/on_fail`
- [x] Tiered agent memory (`APIARY_MEMORIZE`, `apiary memory`)
- [x] Per-run cost tracking (tokens, `cost_usd`, cache split), Cursor cost back-fill
- [x] Rate-limit and credit-aware fallback chains, runner profiles
- [x] Escalation notifications, session transcripts, PR event triggers
- [x] Log rotation and retention

### Platform (v0.13–v0.21)

- [x] Persistent distributed queue with versioned worker protocol
- [x] Versioned out-of-process plugin protocol (JSON over stdio) + Go SDK as its own module
- [x] Plugin registry and `apiary plugins search|info|install|upgrade|uninstall`
- [x] Multi-channel human approvals, answerable operator gates, `apiary approvals`
- [x] Structured execution events, live SSE, dashboard task timeline
- [x] Immutable execution replay, attempt lineage, `apiary instances compare`
- [x] Self-improvement advisor (`apiary improve`) with evidence pack and ledger
- [x] Per-step wall-clock attribution, `apiary profile`
- [x] Unified task state vocabulary (`queued/running/blocked/done/failed/canceled/skipped`)
- [x] Dashboard: Tasks tab, step-progress column, kanban board, approvals-only filter
- [x] Git hook enforcement, SEC triage gate for `ai-ready`

## Next

Ordered by intended sequence.

1. [ ] **Usage/cost export** — `apiary export usage` over `task_executions` joined
   to workflow/step context, CSV/JSON, transcripts opt-in. GitHub issue #485.
2. [ ] **Code signing and notarization** — macOS notarization and Windows
   signing in the GoReleaser pipeline. Blocked on obtaining certificates.
3. [ ] **Plugin SDKs beyond Go and Python** — `sdk/python` and the
   conformance kit in `sdk/conformance` already exist; TypeScript and Rust
   packages follow on demand, validated against the same fixtures. GitHub
   issue #367.
4. [ ] **Stable config schema (1.0)** — freeze `apiary.yaml` and the plugin
   protocol; breaking changes require a major version afterwards.

## Open, undecided

Stubs and promises still visible to users. Each needs either an implementation
or removal; leaving them is not an option.

- [~] `apiary validate --connectivity` — flag exists, prints "not yet implemented"
- [~] `apiary cells` / `apiary dispatch` — registered but thin
- [ ] OTLP trace export
- [ ] GitHub Action wrapper (`apiary-action`)
- [ ] Additional sources: Linear, GitLab, Codeberg/Forgejo (PR-URL parsing exists; no adapter)
- [ ] Multi-repo agents (one agent operating across several repositories)

## Retired

- [-] **Webhook / push ingestion** — Apiary is polling-only by design (2026-08-05).
  Inbound listeners were built once (PR #364) and closed unmerged.
- [-] **gRPC plugin protocol** — superseded by the JSON-over-stdio protocol 1,
  which already works from any language without generated stubs.
- [-] **Postgres port of `internal/db`** — replication into Postgres is handled
  by the external `apiary-pgsink`; the core store stays SQLite.
- [-] **Scheduled routines in core** — shipped as the `dev.apiary.routines`
  source plugin instead.
- [-] **Shell `script` runner**, Trello/Asana/Notion sources, Windsurf runner —
  no demand; reopen only with a concrete user.
