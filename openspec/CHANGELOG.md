# Apiary — Changelog

## Ativas

### internal-task-model

InternalTask como unidade canônica de trabalho: sources viram binders (registram items, recebem output); fan-out de múltiplos workflows por task; write-back por step controlado pelo agente via `APIARY_PUBLISH`.

## Arquivadas

### 2026-06-06

- **apiary-agent-config-redesign** — Redesenho da configuração de agentes: nova seção `agents:` no config, soul files, `preferred_models`, skills metadata; rotas passam a referenciar agentes por ID.
- **foundation** — Especificação inicial do projeto: specs de visão geral, arquitetura, schema, plugin-api, CLI, roadmap; setup do repositório.
- **workflow-mode** — Substituição do pool/router flat por pipelines multi-step declarativos: engine de workflows com DAG, split, foreach, sub-workflows, approvals, resume, TUI step-panel.
- **config-lint-removed-directives** — `validate`/`run` rejeitam diretivas removidas (ex.: `assign_from_output`) e campos desconhecidos, com mensagem de migração.
- **workflow-sequential-authoring** — Autoria v2: sequência implícita + `if/else` + gates de aprovação (`reject_when`/`on_reject`), lowering para o DAG atual; scheduler concorrente com semáforo global.

### 2026-06-03

- **github-source-adapter** — GitHub Issues source adapter (`type: github`) com suporte a poll, labels, estados, comentários, `on_complete` e filtros. Implementado diretamente sem proposal formal.
