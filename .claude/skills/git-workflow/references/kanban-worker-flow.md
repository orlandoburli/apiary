# Kanban Worker Flow

## Pipeline de uma task engineer

1. Dispatcher spawna worker com `--skill git-workflow` (obrigatório)
2. Worker carrega este skill e cria worktree
3. Faz o código dentro do worktree
4. Commit + push
5. `gh pr create` com body contendo Closes #issue + Test plan
6. `gh project item-add` para vincular ao GitHub Project
7. `gh pr merge --auto --squash` — ativar auto-merge
8. `gh pr checks --watch` — aguardar CI
9. Se CI flaky: `gh run rerun <id> --failed` (até 2 tentativas)
10. Confirmar merge: `gh pr view --json state,mergedAt`
11. `git checkout main && git pull` (no repo principal)
12. `task dev:rebuild` — rebuildar containers dev
13. `gitnexus analyze --skip-agents-md` — reindexar
14. `git worktree remove ../project-erp--<branch>`
15. `kanban_complete()` com summary

## Pipeline de uma task QA

1. Dispatcher spawna worker com `--skill qa-regression-checklist`
2. Worker carrega o skill, consulta bugs-conhecidos.md
3. Executa checklist de regressão
4. Se encontrar bugs, criar cards filho com assignee engineer e título padronizado
5. Documentar resultados em relatório no body/comentário
6. `kanban_complete()` com summary + metadata com resultados

## Pipeline de uma task PO

1. Dispatcher spawna worker com perfil po
2. Validar aplicação RODANDO (não código fonte)
3. Foco em usabilidade, coerência visual, aderência a requisitos
4. Se gaps encontrados, criar cards filho para engineer
5. `kanban_complete()` com relatório

## Cron jobs ativos

- `kanban-status` (a cada 6h) — resumo do board no Telegram (script: kanban-status.sh)
- `kanban-report-semanal` (toda segunda 9h) — relatório completo com tokens, tempo, tasks (script: kanban-report.sh)
- `Daily OpenCode Cost Report` (todo dia 8h) — custo do dia anterior

## GH_TOKEN

O profile engineer tem GH_TOKEN no `.env` (`~/.hermes/profiles/engineer/.env`). Se expirar, renovar com `gh auth token` e atualizar o arquivo.