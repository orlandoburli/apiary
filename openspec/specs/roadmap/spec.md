# Apiary — Roadmap

## v0.1 — Foundation

- [ ] Project structure and Go module scaffold
- [ ] `apiary.yaml` schema + validation
- [ ] Core interfaces: `SourceAdapter`, `RunnerAdapter`, `Cell`, `RunResult`
- [ ] Router engine (priority-based rule matching)
- [ ] CLI skeleton: `run`, `validate`, `status`, `init`
- [ ] Plane source adapter (poll mode)
- [ ] OpenCode runner adapter
- [ ] Shell script runner adapter
- [ ] Structured JSON logging

## v0.2 — More Sources & Runners

- [ ] Jira Cloud source adapter
- [ ] Linear source adapter
- [ ] GitHub Issues source adapter
- [ ] Webhook server (receive push events from sources)
- [ ] `apiary cells` command
- [ ] `apiary dispatch` command

## v0.3 — Observability & Reliability

- [ ] State locking (mark task "in progress" before run)
- [ ] Result write-back (post runner output as task comment)
- [ ] Run history store (SQLite)
- [ ] `apiary logs` command with run-id filtering
- [ ] OTLP trace export (opt-in)
- [ ] Retry logic with configurable backoff
- [ ] `--dry-run` mode
- [ ] Watch mode (`apiary status --watch`)

## v0.4 — Developer Experience

- [ ] `apiary init` interactive scaffolding
- [ ] `apiary validate --connectivity`
- [ ] Homebrew tap
- [ ] Docker image (`ghcr.io/orlandoburli/apiary`)
- [ ] Documentation site

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
