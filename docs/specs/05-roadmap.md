# Apiary — Roadmap

## v0.1 — Foundation (specification + skeleton)

- [ ] Project structure and Go module scaffold
- [ ] `apiary.yaml` schema + validation
- [ ] Core interfaces: `SourceAdapter`, `RunnerAdapter`, `Cell`, `RunResult`
- [ ] Router engine (priority-based rule matching)
- [ ] CLI skeleton: `run`, `validate`, `status`, `init`
- [ ] Plane source adapter (poll mode)
- [ ] Claude Code runner adapter
- [ ] Structured JSON logging

## v0.2 — More Sources & Runners

- [ ] Jira Cloud source adapter
- [ ] Linear source adapter
- [ ] GitHub Issues source adapter
- [ ] OpenCode runner adapter
- [ ] Shell script runner adapter
- [ ] Webhook server (receive push events from sources)

## v0.3 — Observability & Reliability

- [ ] State locking (mark task "in progress" before run)
- [ ] Result write-back (post output as task comment)
- [ ] Run history store (SQLite)
- [ ] `apiary logs` command with run-id filtering
- [ ] `apiary cells` command
- [ ] `apiary dispatch` manual override
- [ ] OTLP trace export (opt-in)
- [ ] Retry logic with configurable backoff

## v0.4 — Developer Experience

- [ ] `apiary init` interactive scaffolding
- [ ] `apiary validate` with source connectivity check
- [ ] `--dry-run` mode
- [ ] Watch mode (`apiary status --watch`)
- [ ] Homebrew tap
- [ ] Docker image

## v1.0 — Stable & Extensible

- [ ] Stable config schema (no breaking changes after this)
- [ ] gRPC plugin protocol for external source/runner adapters
- [ ] Web UI (read-only dashboard: active runs, history, routing decisions)
- [ ] GitHub Actions integration (trigger Apiary runs from CI)
- [ ] Documentation site

## Future (post-v1)

- Agent profiles as code (store worker definitions in git, not just YAML)
- Cost tracking per run (token usage by worker/model)
- Approval gates (pause before invoking a worker; require human confirm)
- Multi-repo support (workers that operate across repos)
- Trello, Asana, Notion source adapters
- Cursor, Windsurf runner adapters
