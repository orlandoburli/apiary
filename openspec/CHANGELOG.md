# Apiary — Changelog

## Ativas

### agent-memory

Memória persistente em camadas para agentes: tier global (fatos duráveis, daemon-wide) + tier de task (notas de trabalho que sobrevivem a instâncias e seguem a linhagem via `parent_task_id`), gravadas pelo marker `APIARY_MEMORIZE` e armazenadas como markdown em disco (`<data-dir>/memory`, índice `MEMORY.md`). Recall injetado no `SystemPrepend` (índice + notas, com budget) e leitura direta via `APIARY_MEMORY_DIR`; curadoria via `apiary memory` CLI. Opt-in (`settings.memory.enabled: false` por padrão).

### binary-distribution

Publicação dos binários por release: GoReleaser dirigido por push de tag `v*` produz archives + checksums (GitHub Releases), Homebrew tap, Scoop bucket, winget, pacotes Linux (`.deb`/`.rpm` + AUR) e imagem OCI multi-arch (`ghcr.io`). Assinatura/notarização (macOS/Windows) fica fora do escopo v1. Restrição dominante: `mattn/go-sqlite3` exige cgo, então cross-compile precisa de toolchains C.

### internal-task-model

InternalTask como unidade canônica de trabalho: sources viram binders (registram items, recebem output); fan-out de múltiplos workflows por task; write-back por step controlado pelo agente via `APIARY_PUBLISH`.

## Arquivadas

### 2026-06-06

- **workflow-env-vars** — Variáveis de ambiente opcionais por escopo: `agents[].env`, `workflows[].env` e `workflows[].steps[].env`, mescladas por step com precedência STEP > WORKFLOW > AGENT, sobre a camada base de identidade (git + `source_token` → `GITHUB_TOKEN`/`GH_TOKEN`). Merge no executor do daemon (`stepEnv`); engine repassa o env de workflow via `StepRequest.WorkflowEnv`.
- **apiary-agent-config-redesign** — Redesenho da configuração de agentes: nova seção `agents:` no config, soul files, `preferred_models`, skills metadata; rotas passam a referenciar agentes por ID.
- **foundation** — Especificação inicial do projeto: specs de visão geral, arquitetura, schema, plugin-api, CLI, roadmap; setup do repositório.
- **workflow-mode** — Substituição do pool/router flat por pipelines multi-step declarativos: engine de workflows com DAG, split, foreach, sub-workflows, approvals, resume, TUI step-panel.
- **config-lint-removed-directives** — `validate`/`run` rejeitam diretivas removidas (ex.: `assign_from_output`) e campos desconhecidos, com mensagem de migração.
- **workflow-sequential-authoring** — Autoria v2: sequência implícita + `if/else` + gates de aprovação (`reject_when`/`on_reject`), lowering para o DAG atual; scheduler concorrente com semáforo global.

### 2026-06-03

- **github-source-adapter** — GitHub Issues source adapter (`type: github`) com suporte a poll, labels, estados, comentários, `on_complete` e filtros. Implementado diretamente sem proposal formal.
