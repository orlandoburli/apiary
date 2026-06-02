---
name: changelog-contexto
description: Toda change OpenSpec deve constar em `openspec/CHANGELOG.md` (ativa ao propor, arquivada ao concluir). Use ao propor uma nova change, arquivar uma change, ou consultar histórico do projeto antes de iniciar trabalho que pode depender de changes anteriores.
---

# Histórico de changes do projeto

O arquivo `openspec/CHANGELOG.md` contém o índice cronológico de todas as changes do projeto — arquivadas (implementadas) e ativas (em andamento). Cada entrada tem um resumo de 1-2 linhas; para detalhes completos, consulte a `proposal.md` da change.

## Regra: toda change deve constar no CHANGELOG

Toda change — ao ser **proposta** ou **arquivada** — **deve** ter entrada correspondente em `openspec/CHANGELOG.md`. Não é opcional.

- **Nova change**: adicionar entrada na seção `## Ativas` com `### <nome>` e resumo de 1-2 linhas.
- **Change arquivada**: mover a entrada de "Ativas" para `## Arquivadas`, sob o heading de data `### YYYY-MM-DD`.
- Os skills `openspec-propose` e `openspec-archive-change` já incluem esse passo. Se a change for criada ou arquivada manualmente (sem skill), o agente deve atualizar o CHANGELOG na mesma operação.

## Quando consultar

- Ao iniciar trabalho que pode ser afetado por changes anteriores (dependências, tabelas alteradas, capabilities criadas).
- Quando o usuário perguntar sobre o histórico do projeto, decisões passadas ou estado atual.
- Antes de propor uma nova change, para verificar se algo semelhante já foi feito ou está em andamento.

## Como usar

1. Leia `openspec/CHANGELOG.md` para visão geral.
2. Se precisar de detalhes de uma change específica, leia `openspec/changes/<nome>/proposal.md` (ativa) ou `openspec/changes/archive/<data>-<nome>/proposal.md` (arquivada).
3. Para entender o design técnico, leia o `design.md` da change. Para tasks pendentes, leia `tasks.md`.
