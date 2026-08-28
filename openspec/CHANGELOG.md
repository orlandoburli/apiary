# Apiary — Changelog

## Ativas

- **plugin-registry** — Plugin registry and command-line installs: a static, PR-reviewed index (`registry/plugins/*.yaml` compiled to signed `index.json`, artifacts stay with their publishers) plus `apiary plugins search|info|install|upgrade|uninstall`. Pre-download resolution (host semver, protocol, os/arch, yanks), staged installs with digest verification and safe unpacking, checksum pin injection from the index — turning the manifest pin from drift detection into a supply-chain check — an explicit `security:` trust summary before commit, registry CI that re-derives every digest and runs the conformance kit, and mirrors/`--offline` for air-gapped installs. Installed still never means enabled; the daemon never contacts the registry. Four phases.
- **step-wallclock-attribution** — Atribuição de wall-clock por step (thinking / writing / esperas de tool / tarefas em background) gravada em `task_executions` e `step_runs` ao lado das colunas de token, lista das chamadas mais lentas, payload do evento `system:task_started` no log, e comando `apiary profile <instance-id> [--json]`. GitHub issue #399.

## Arquivadas

### 2026-08-07

- **self-improvement-advisor** — `apiary improve`: standalone command that mines Apiary's own execution history and proposes configuration changes. Deterministic evidence pack computed in Go (step/workflow/agent metrics, rework loops, wait polling, normalised failure clusters, dead paths, wall-clock split, sampled transcripts) exposed via `--dump-evidence`; advisor resolved as an ordinary agent (`--advisor`, ad-hoc `--runner`/`--model`, `settings.improve.agent`, or an agent named `improver`) with profiles, fallbacks and per-effort models; config workspace discovery over `apiary.yaml`, workflow files, souls and skills with secrets redacted; five-stage validation gate (path → apply → `cfg.Validate` → expr lint → new warnings) with strict patch parsing and no fuzzy matching; critic pass at deep effort; `--apply` writing in place with git as the undo story; and an improvement ledger with `improve history|show|effect` measuring the before/after delta of applied changes. Six phases, GitHub issues #401-#407 (PRs #408, #412, #413, #414, #416, #417).

### 2026-07-22

- **persistent-distributed-queue** — Durable SQLite queue and versioned worker protocol with leased at-least-once delivery, capability matching, scoped concurrency, workspace affinity, cancellation, drain, health, and an embedded local worker. GitHub issue #214.
- **versioned-plugin-sdk** — Versioned out-of-process plugin manifest and JSON protocol with discovery, compatibility/capability checks, schema-backed configuration validation, isolated execution, CLI inspection, and a reference plugin. GitHub issue #213.
- **multi-channel-human-approvals** — First-class human-in-the-loop approval requests with structured fields, authorized quorum/delegation, dashboard and signed webhook responses, idempotent resolution, escalation, sensitive-action policies, and rejection feedback. GitHub issue #212.
- **structured-execution-events** — Versioned, redacted execution events with persisted queries, live SSE subscriptions, route/fallback reasons, retention, CLI history, and a dashboard task timeline. GitHub issue #211.
- **reusable-typed-subworkflows** — Reusable local workflow files with typed inputs and outputs, cycle-safe resolution, linked nested execution history, and explicit failure/cancellation semantics. GitHub issue #210.
- **immutable-execution-replay** — Immutable workflow replay with descendant attempt lineage, selectable resume points, secret-safe workflow-definition snapshots, and CLI comparison of attempts. GitHub issue #209.

### 2026-07-07

- **credit-aware-fallback** — Credit-aware runner fallback: out-of-credits detection beyond rate-limit-only with `FailureDetector` interface, failure-kind-specific cooldowns (5m rate-limit / 24h credit / 0 abort), `settings.default_fallbacks` global fallback chain, per-agent `fallback_strategy` (ordered/random/least_cost/fastest), named runner profiles via `profiles.<name>` activated with `--profile=<name>`, `credit_exhausted` and `failure_kind` columns on `task_executions`, config validation for all new fields, and full documentation updates.

### 2026-06-10

- **binary-distribution** — Publicação dos binários por release: GoReleaser dirigido por push de tag `v*` produz archives + checksums (GitHub Releases), Homebrew tap (cask), Scoop bucket, pacotes Linux (`.deb`/`.rpm`) e imagem OCI multi-arch (`ghcr.io`, pull anônimo). cgo eliminado via migração `mattn/go-sqlite3` → `modernc.org/sqlite` (cross-compile puro-Go). winget/AUR dropados; assinatura/notarização e SLSA/cosign deferidos para changes futuras. Releases estáveis v0.1.0–v0.4.0 publicados em todos os canais.
- **internal-task-model** — InternalTask como unidade canônica de trabalho: sources viram binders (registram items, recebem output); fan-out de múltiplos workflows por task (`RouteAll` + `trigger.exclusive`); write-back por step via marker `APIARY_PUBLISH`; spawn de sub-tasks via `APIARY_SPAWN` (com `spawn: await`); hooks agregados `tasks.on_complete`/`on_fail` por contador de outstanding; dashboard com lineage e bindings. Todas as 9 fases + docs cross-cutting mergeadas.
- **agent-memory** — Memória persistente em camadas para agentes: tier global (fatos duráveis, daemon-wide) + tier de task (notas que sobrevivem a instâncias e seguem a linhagem via `parent_task_id`), gravadas pelo marker `APIARY_MEMORIZE` e armazenadas como markdown em `<data-dir>/memory` (índice `MEMORY.md`). Recall injetado no prompt (índice + notas, com budget) e leitura direta via `APIARY_MEMORY_DIR`; curadoria via `apiary memory` CLI; sweep de retenção para notas de task. Opt-in (`settings.memory.enabled: false` por padrão). PRs #162 (feature) e #163 (docs).

### 2026-06-06

- **workflow-env-vars** — Variáveis de ambiente opcionais por escopo: `agents[].env`, `workflows[].env` e `workflows[].steps[].env`, mescladas por step com precedência STEP > WORKFLOW > AGENT, sobre a camada base de identidade (git + `source_token` → `GITHUB_TOKEN`/`GH_TOKEN`). Merge no executor do daemon (`stepEnv`); engine repassa o env de workflow via `StepRequest.WorkflowEnv`.
- **apiary-agent-config-redesign** — Redesenho da configuração de agentes: nova seção `agents:` no config, soul files, `preferred_models`, skills metadata; rotas passam a referenciar agentes por ID.
- **foundation** — Especificação inicial do projeto: specs de visão geral, arquitetura, schema, plugin-api, CLI, roadmap; setup do repositório.
- **workflow-mode** — Substituição do pool/router flat por pipelines multi-step declarativos: engine de workflows com DAG, split, foreach, sub-workflows, approvals, resume, TUI step-panel.
- **config-lint-removed-directives** — `validate`/`run` rejeitam diretivas removidas (ex.: `assign_from_output`) e campos desconhecidos, com mensagem de migração.
- **workflow-sequential-authoring** — Autoria v2: sequência implícita + `if/else` + gates de aprovação (`reject_when`/`on_reject`), lowering para o DAG atual; scheduler concorrente com semáforo global.

### 2026-06-03

- **github-source-adapter** — GitHub Issues source adapter (`type: github`) com suporte a poll, labels, estados, comentários, `on_complete` e filtros. Implementado diretamente sem proposal formal.
