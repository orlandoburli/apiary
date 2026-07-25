---
name: git-workflow
description: Git workflow obrigatório do projeto — toda change via feature branch + PR usando `git worktree`, PR aberto para review humano (sem auto-merge por agente), sync da `main`/rebuild do dev/`gitnexus analyze` após merge. Use ao iniciar qualquer change, abrir PR, ou após merge de PR.
---

# Git Workflow — Projeto ERP

## Kanban Worker Integration

**Todo kanban task do engineer DEVE carregar este skill e usar workspace worktree:**
```
hermes kanban create "..." --assignee engineer --skill git-workflow --workspace worktree
```

Ou via CLI:
```
hermes kanban create "fix: ..." --assignee engineer --skill git-workflow
```

**Regra fundamental (SEC-08):** Agentes NUNCA ativam auto-merge. O engineer cria o PR e para — um revisor humano (ou o reviewer council) deve aprovar explicitamente antes do merge. `gh pr merge --auto --squash` é proibido para agentes engineer.

**Pipeline completo de uma task engineer:**
Kanban cria task → dispatcher spawna worker → worker carrega git-workflow → cria worktree → faz o código → commit → push → PR aberto → aguarda review/aprovação humana → merge realizado pelo revisor → pull main → task dev:rebuild → gitnexus reindex → remove worktree → complete task

**GH_TOKEN:** o profile engineer tem GH_TOKEN no `.env`. Se o worker não conseguir usar `gh`, verificar se o token ainda é válido.

**Pitfalls:**
- **Skill must be in each target profile's skills directory.** The kanban worker loads skills from `~/.hermes/profiles/<profile>/skills/`, NOT from `~/.hermes/skills/`. If `git-workflow` is absent from any profile's skills dir, the worker crashes immediately with `Error: Unknown skill(s): git-workflow`. This affects ALL profiles (engineer, po, qa) that run tasks with `--skill git-workflow`. Fix: `ln -s ~/.hermes/skills/git-workflow ~/.hermes/profiles/<profile>/skills/git-workflow` for each profile.
- Worker sem GH_TOKEN → não consegue criar PR nem ativar auto-merge. Solução: `hermes config set terminal.env_passthrough '["GH_TOKEN"]'` no profile, ou adicionar ao `.env` do profile.
- Worktree deletado entre runs → se a task for re-spawnada após um merge, o worktree original sumiu. O worker deve criar um novo worktree ou clonar fresco.
- CI lento/flaky → `gh pr checks --watch` + `gh run rerun --failed` em flake conhecido. Só desistir após 2-3 reruns do mesmo padrão.
- Diverging branches no git pull → usar `git reset --hard origin/main` quando o histórico local divergir (worktree worker não deve ter commits locais em main).

## Git worktrees (obrigatório — sem exceção)

**Toda change DEVE usar `git worktree`.** Isso é inegociável — múltiplos agentes podem estar trabalhando em paralelo, e commits diretos no checkout principal corrompem o estado de todos.

- **Create worktree**: `git worktree add ../project-erp--<branch-name> -b <branch-name>` (from the main repo).
- **Work inside the worktree directory**, not the main repo.
- **Remove after merge**: `git worktree remove ../project-erp--<branch-name>`.
- **Never** work directly in the main repo checkout when creating or switching branches. Always create a worktree.
- **Mesmo para changes triviais** (1 arquivo, docs-only, fix de typo): worktree. A regra existe para proteger o workspace compartilhado, não para proteger a complexidade da change.
- **Artefatos OpenSpec** (proposal, design, specs, tasks, CHANGELOG): devem ser criados **dentro do worktree** do branch da implementação, commitados junto com o código, e entregues no mesmo PR. Nunca criar specs no checkout principal — elas ficam órfãs e não entram no git.
- **Kanban workers:** se o workspace for `worktree` (recomendado), o worker já inicia dentro do worktree. Se for `scratch`, o worker precisa clonar e criar manualmente.

## Branch + PR flow

1. Create a worktree with a feature branch from `main` (e.g. `git worktree add ../project-erp--feat/financeiro -b feat/financeiro`).
2. Work and make commits inside the worktree directory.
3. Push the branch and open a PR via `gh pr create`.
4. **Link issue and project**: after creating the PR:
   - The PR body MUST contain `Closes #<issue>` (or `Fixes #<issue>`) to link the issue.
   - Add the PR to the relevant GitHub Project: `gh project item-add 1 --owner orlandoburli-enterprise --url <pr-url>`.
5. **Stop here — do NOT enable auto-merge (SEC-08).** An engineer agent MUST NOT run `gh pr merge --auto --squash`. Leave the PR open for a reviewer. A human or the reviewer council will approve and enable merge after inspecting the diff.
6. CI must pass (lint, tests, build, sqlc-check) before merge — the reviewer verifies this before approving.
7. After a reviewer approves and enables merge, confirm the merge completed:
   - `gh pr view <number> --json state,mergedAt` that `state == "MERGED"`.
8. **Sync da `main` local + rebuild do dev — executar imediatamente após confirmar o merge.** Não esperar o usuário pedir. No checkout principal (não no worktree):
   ```bash
   cd <repo-root> && git checkout main && git pull --ff-only
   ```
   Rebuildar a aplicação que está rodando:
   ```bash
   task dev:rebuild
   ```
   Sem isso, o container continua servindo o bundle/binário da build anterior — sintoma típico: "o fix não chegou no meu dev local" mesmo após o merge na `main`. `task dev` usa `up -d` puro (não `watch`), então o source do container é cópia estática do momento do build.
   **Sempre rodar `task dev:rebuild`** após `git pull --ff-only`, sem exceção. Não tentar adivinhar quais serviços foram afetados — o comando cuida disso.
9. **Re-indexar o GitNexus** — executar imediatamente após `task dev:rebuild`:
   ```bash
   cd <repo-root> && gitnexus analyze --skip-agents-md
   ```
   O knowledge graph do GitNexus precisa refletir o código atualizado da `main`. Sem isso, queries MCP (`query`, `context`, `impact`) retornam dados stale. **Sempre rodar** após pull, sem exceção. Usa `--skip-agents-md` porque os arquivos de contexto (`AGENTS.md`/`CLAUDE.md`) já foram atualizados pelo PR (ver seção abaixo).
10. Remove o worktree: `git worktree remove ../project-erp--<branch>` e (opcional) `git branch -d <branch>`.
11. Never `git push` directly to `main`.

**Resumo: o ciclo completo de uma change é** `worktree → commit → push → PR aberto → aguardar review humano → merge pelo revisor → pull main → task dev:rebuild → gitnexus analyze --skip-agents-md → remover worktree`. Pular qualquer passo (especialmente 8-9) é proibido. O agente engineer NUNCA ativa auto-merge.

## AGENTS.md / CLAUDE.md — manter atualizados via PR

`gitnexus analyze` regenera `AGENTS.md` e `CLAUDE.md` na raiz do repo com estatísticas do knowledge graph. A branch protection de `main` impede push direto, então esses arquivos devem ser atualizados **dentro do worktree**, como parte de cada PR.

### Procedimento

Antes do push final no worktree (entre o passo 2 e o passo 3 acima):

```bash
cd <worktree>
gitnexus analyze
git diff --quiet AGENTS.md CLAUDE.md || \
  git add AGENTS.md CLAUDE.md && \
  git commit -m "chore: update AGENTS.md and CLAUDE.md (gitnexus reindex)"
```

Isso garante que cada PR traz os arquivos de contexto atualizados. Após o merge, o passo 9 usa `--skip-agents-md` porque o PR já atualizou os arquivos.

## PR requirements

- **Language: write ALL commit messages, PR titles, and PR descriptions in English.** This is mandatory and non-negotiable — the repo's history and PRs are English-only. Never write git commit messages or PR text in Portuguese.
- Title: concise summary in English.
- Body: `## Summary` with bullet points + `## Test plan`.
- Body MUST include `Closes #<issue>` to auto-link and auto-close the issue on merge.
- CI required status check: **CI** (gate job that aggregates all per-area checks, including E2E).

## Naming conventions

- Branches: `feat/`, `fix/`, `refactor/`, `chore/`, `docs/` prefixes.
- Commits: conventional commits **in English** (e.g. `feat:`, `fix:`, `ci:`).
