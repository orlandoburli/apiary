# Apiary — Roadmap

## v0.1 — Foundation ✅

- [x] Project structure and Go module scaffold
- [x] `apiary.yaml` schema + validation
- [x] Core interfaces: `SourceAdapter`, `RunnerAdapter`, `Cell`, `RunResult`
- [x] Router engine (priority-based rule matching)
- [x] CLI skeleton: `run`, `validate`, `status`, `init`
- [x] Plane source adapter (poll mode)
- [x] OpenCode runner adapter (CLI + API modes)
- [x] Shell script runner adapter (`script` runner)
- [x] Structured JSON logging

## v0.2 — More Sources & Runners

- [ ] Jira Cloud source adapter
- [ ] Linear source adapter
- [x] GitHub Issues source adapter
- [ ] Webhook server (receive push events from sources)
- [~] `apiary cells` command (CLI registered, but stub — returns nothing)
- [~] `apiary dispatch` command (CLI registered, but stub — "not yet implemented")

## v0.3 — Observability & Reliability

- [x] State locking (mark task "in progress" before run)
- [x] Result write-back (post runner output as task comment)
- [x] Run history store (SQLite)
- [ ] `apiary logs` command with run-id filtering (not implemented)
- [~] OTLP trace export (opt-in) (config parsed, no actual exporter)
- [x] Retry logic with configurable backoff
- [x] `--dry-run` mode
- [x] Watch mode (`apiary status --watch`)

## v0.4 — Developer Experience

- [x] `apiary init` interactive scaffolding
- [~] `apiary validate --connectivity` (flag exists, but stub — "not yet implemented")
- [ ] Homebrew tap
- [ ] Docker image (`ghcr.io/orlandoburli/apiary`)
- [x] Documentation site (mkdocs-material + GitHub Pages)

## v1.0 — Stable & Extensible

- [ ] Stable config schema (no breaking changes after this point)
- [ ] gRPC plugin protocol for external source/runner adapters
- [ ] Web UI (read-only dashboard: active runs, history, routing decisions)
- [ ] GitHub Actions integration (`apiary-action`)
- [ ] Comprehensive documentation

## Future (post-v1)

- Cost tracking per run (token usage by worker/model)
- Approval gates (pause before invoking a worker; require human confirm)
- Multi-repo support (workers operating across multiple repositories)
- Trello, Asana, Notion source adapters
- Cursor, Windsurf runner adapters
- Agent profiles stored as code (version-controlled worker definitions)
