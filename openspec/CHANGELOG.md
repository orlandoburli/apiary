# Apiary — Changelog

## Ativas

- **step-wallclock-attribution** — Atribuição de wall-clock por step (thinking / writing / esperas de tool / tarefas em background) gravada em `task_executions` e `step_runs` ao lado das colunas de token, lista das chamadas mais lentas, payload do evento `system:task_started` no log, e comando `apiary profile <instance-id> [--json]`. GitHub issue #399.

## Arquivadas

### 2026-08-28

- **plugin-registry** — Plugin registry and command-line installs: a static, PR-reviewed index (`registry/plugins/*.yaml` compiled by CI into `docs/registry/v1/index.json`, artifacts hosted by their publishers) plus `apiary plugins search|info|install|upgrade|uninstall`. Resolution runs every compatibility check before a download (host semver, protocol, platform, yanks); installs stage outside every searched directory, verify the archive and executable digests, validate the manifest, print the declared access, and commit with one atomic rename, never enabling anything. An unpinned manifest is pinned to the registry's digest — a value the publisher does not control — and `Installed.VerifyPin` re-derives it in `apiary validate`. The index is minisign-verified in-process (fail-closed once a key is pinned, cache verified on read); signing is wired but dormant until a keypair exists, and until then commands report the index as unverified. `plugin_registries` accepts a URL or a mapping pinning a mirror's key; `[]` disables the registry. Registry CI re-derives every digest, cross-checks the embedded manifest, and runs the conformance kit — publishing the verdict rather than blocking the listing. Four phases (PRs #450, #453, #454); found three protocol failures in `dev.apiary.routines` 0.1.0 (orlandoburli/apiary-routines#5).

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
