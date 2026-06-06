# Apiary — Changelog

## Ativas

| Change | Descrição | Status |
|---|---|---|
| [foundation](changes/foundation/) | Especificação inicial do projeto | in-progress |
| [workflow-mode](changes/workflow-mode/) | Substituir pool/router flat por pipelines multi-step declarativos | proposed |
| [config-lint-removed-directives](changes/config-lint-removed-directives/) | `validate`/`run` rejeitam diretivas removidas (ex.: `assign_from_output`) e campos desconhecidos, com mensagem de migração | proposed |
| [workflow-sequential-authoring](changes/workflow-sequential-authoring/) | Autoria v2: sequência implícita + `if/else` + gates de aprovação (`reject_when`/`on_reject`), compilando para o DAG atual (design only) | proposed |

## Arquivadas

### 2026-06-03

- **github-source-adapter** — GitHub Issues source adapter (`type: github`) com suporte a poll, labels, estados, comentários, `on_complete` e filtros. Implementado diretamente sem proposal formal.
