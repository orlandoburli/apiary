---
name: e2e-before-pr
description: Testes locais obrigatórios antes de abrir PR — unitários, lint, UI e E2E (Playwright). Use sempre antes de rodar `gh pr create` ou de pedir merge; também antes de tocar em `deploy/docker-compose.dev.yml` (smoke da stack dev) ou em qualquer change que afete `apps/`, `database/migrations`, `Taskfile.yaml`.
---

# Testes locais antes de abrir PR

**REGRA ABSOLUTA: ZERO push sem teste passar localmente primeiro.** Isso vale para unitários, lint, UI e E2E. A sequência é: (1) fazer o código no worktree, (2) rodar testes localmente, (3) só então dar `git push`. Não use o CI como debugger — GitHub Actions custa dinheiro por minuto de execução.

**Não abra PR "pra ver se o CI passa".** Se o CI falhar após push, corrija localmente no worktree, faça novo push, e só então abra ou atualize o PR. Cada push com teste quebrado é dinheiro desperdiçado em GitHub Actions.

**Só faça merge se todos os testes passarem.** Se algum teste falhar no CI após abrir o PR, corrija antes de mergear. Nunca force merge com testes vermelhos. A regra "pre-existing failure" não existe — todo bug deve ser corrigido antes do merge.

## Gate de verificação pré-push (obrigatório)

Antes de qualquer `git push` no worktree, confirmar:

```bash
# Backend (Go)
task api:test && task api:lint   # vet, build, test — 74 pacotes

# Frontend (Next.js)
task web:test && task web:lint   # jest 1907+, lint 0 errors

# DB (se mudou migrations)
task db:lint
```

Só dar `git push` quando todos os comandos acima retornarem `0`. Se qualquer um falhar, corrigir no worktree até passar — zero exceções. O `task dev:rebuild` NÃO substitui os testes; rebuild é rebuild de imagem/container, não validação de código.

## Subagentes: isolação estrita de worktree

Subagentes criados via `delegate_task` ou `cronjob` (worktree) **DEVEM operar exclusivamente dentro do worktree**. Regras:

- **Nunca escrever no checkout principal** (`/repos/project-erp`) — apenas no worktree (`/repos/project-erp--fix-xyz`).
- **Subagentes não devem criar/atualizar arquivos fora do worktree.** Se o subagente tentar modificar `database/`, `.hermes-skills/`, ou qualquer arquivo fora do worktree, o agente pai deve intervir e corrigir imediatamente.
- **Verificar `git status` no checkout principal antes de setiap push** — se o main estiver sujo com mudanças do subagente, limpar com `git checkout -- <path>` antes de continuar.
- Antes de cada `git push`, confirmar que `git status` no main está limpo e que as mudanças estão no worktree.

## Quando é obrigatório

## Quando é obrigatório

Sempre que o PR toca em qualquer um destes caminhos:

- `apps/api/**`, `apps/admin-api/**` → `task api:test`, `task api:lint`
- `apps/web/**`, `apps/admin/**` → `task web:test`, `task web:lint`
- `database/migrations/**`, `database/queries/**`, `database/queries_admin/**`, `database/sqlc.yaml` → `task db:lint`
- `deploy/docker-compose.*.yml`, `Taskfile.yaml` → E2E obrigatório

## Quando pode pular

É permitido pular os testes locais **apenas** quando o PR mexe exclusivamente em:

- `docs/**` ou arquivos `.md` soltos
- `.cursor/rules/**`, `.cursor/skills/**`
- `.github/**` sem alterar a matriz do workflow `CI`
- `README.md`, `openspec/**`

Em qualquer outro caso, execute os testes.

## Procedimento: testes rápidos (unitários + lint)

Rodar **antes** dos E2E — são rápidos e pegam a maioria dos problemas. A partir do worktree:

```bash
# Backend (Go)
task api:test
task api:lint

# Frontend (Next.js)
task web:test
task web:lint

# Banco (tenant isolation + migrations)
task db:lint
```

Rode apenas os testes da área afetada. Se o PR toca só em `apps/web/**`, basta `task web:test` e `task web:lint`.

## Procedimento: E2E (stack completa)

Obrigatório quando o PR toca em código que afeta a stack integrada. A partir do worktree (nunca no repo principal):

```bash
# 1. Libera portas 8080/8081/5432/6379/3000 se a stack dev estiver de pé
task dev:down

# 2. Sobe a stack E2E isolada (Postgres+Redis tmpfs, Wiremock, API+Worker em TEST_MODE, Web)
task e2e:up

# 3. Garante deps do webServer que o Playwright sobe no host
(cd apps/web && npm install)

# 4. Roda os specs reais (project=e2e-real)
task e2e:run

# 5. Derruba a stack e volta ao dev
task e2e:down
```

Só abra o PR depois de ver `XX passed` no final do `task e2e:run`. **Se algum spec falhar, corrija antes de push/PR.**

## Validação rápida de que a stack subiu certa

Se `task e2e:run` reclamar de 404 ou timeout no webServer, diagnostique antes de abrir PR:

```bash
curl -s http://localhost:18081/health
curl -s -X POST http://localhost:18081/test/tenants \
  -H "Authorization: Bearer *** \
  -H "Content-Type: application/json" \
  -d '{"profile":"minimal"}' | head -c 200
```

A primeira chamada deve retornar `200` com `"status":"ok"`. A segunda deve retornar um JSON com `tenant_id`. Se qualquer uma falhar, `task e2e:logs` e investigue antes de seguir.

## Smoke da stack dev (obrigatório quando tocar `docker-compose.dev.yml`)

A suíte E2E roda contra a stack `deploy/docker-compose.e2e.yml`, que é diferente da `deploy/docker-compose.dev.yml`. Mudanças em `docker-compose.dev.yml` (ex.: variáveis de ambiente, portas, targets de build) **não são cobertas pela suíte E2E** e já passaram batidas para `main` (ver #408 → #410).

Quando o PR tocar em `deploy/docker-compose.dev.yml`, rode este smoke adicional:

```bash
task dev:down && task dev
sleep 10

# Verifica que a URL embutida no bundle do browser é alcançável do host.
# Se for um nome interno do Compose (ex.: http://api:8080), o browser
# nunca vai resolver e o login quebra silenciosamente.
WEB_API_URL=$(docker exec erp-web printenv NEXT_PUBLIC_API_URL)
echo "Web aponta para: $WEB_API_URL"
echo "$WEB_API_URL" | grep -qE '^http://localhost:' \
  || { echo "ERRO: NEXT_PUBLIC_API_URL aponta para hostname não-roteável do browser"; exit 1; }

# Faz o mesmo POST que o botão "Entrar" faria, a partir do host.
# Aceita 200/400/401/422 (a rota existe e respondeu algo); rejeita
# connection refused, timeout ou HTML 404.
curl -fsS -m 5 -X POST "$WEB_API_URL/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"email":"smoke@test","senha":"x"}' -o /dev/null \
  -w 'HTTP %{http_code}\n' \
  || true
```

O primeiro `grep` falha fechado se a URL não for `http://localhost:*`. O `curl` deve imprimir `HTTP 4xx` (ou 200, conforme o caso). Se vier `Could not resolve host` ou `Connection refused`, a stack está mal configurada.

## No corpo do PR

O bloco `## Test plan` deve conter linhas confirmando quais testes foram executados. Incluir **apenas** os testes relevantes à área afetada:

```markdown
- [x] `task api:test` passou localmente
- [x] `task api:lint` passou localmente
- [x] `task web:test` passou localmente
- [x] `task web:lint` passou localmente
- [x] `task db:lint` passou localmente
- [x] `task e2e:run` passou localmente (XX/XX specs)
```

E quando o PR tocar em `deploy/docker-compose.dev.yml`, adicione também:

```markdown
- [x] Smoke da stack dev: `NEXT_PUBLIC_API_URL` aponta para `http://localhost:*` e o POST em `/auth/login` responde do host.
```

**Não abra PR sem essas marcações.** Se algum teste falhar após abrir o PR (flake, race condition), corrija e confirme verde antes de mergear.

## Validando o estado real da main (CI completo)

O CI do projeto usa **path filtering** — cada push só roda os jobs relevantes às pastas alteradas. Isso significa que um push em `docs/` executa apenas ~2 jobs (9-14s), enquanto um push em `apps/api/` + `apps/web/` executa todos (~25min). **A main pode parecer verde por meses só porque ninguém está fazendo push grandes nela.**

Para saber o estado real da main:

```bash
# Forçar CI completo em main (todos os jobs, não só os afetados pelo último push)
gh workflow run ci.yml --ref main --field force=true
# Returns: https://github.com/orlandoburli-enterprise/project-erp/actions/runs/<id>

# Aguardar resultado
gh run view <run-id> --json status,conclusion
# Status: queued → in_progress → completed
# conclusion: success / failure / neutral (neutral = skipped)
```

**Quando fazer isso:**
- Antes de iniciar uma mudança sistêmica (migration, refactor multi-pasta)
- Após identificar que múltiplos PRs estão falhando no CI com falhas diferentes
- Quando o "CI rápido" passou mas você suspeita que a stack completa quebraria

**Como interpretar o resultado:**
- Se `conclusion: success` → main está validada, pode confiar
- Se `conclusion: failure` → ver seção de diagnóstico de CI abaixo

### Diagnosticando falhas no CI

Quando um job falha no CI (seja em PR ou em workflow_dispatch), usar:

```bash
# 1. Listar jobs com falha
gh run view <run-id> --json jobs | python3 -c "
import sys,json
d=json.load(sys.stdin)
for j in d.get('jobs',[]):
    if j.get('conclusion') == 'failure':
        print(f'FAIL: {j[\"name\"]} id={j[\"id\"]}')
"

# 2. Identificar o passo que falhou (não só o job)
gh api repos/orlandoburli-enterprise/project-erp/actions/jobs/<job-id> | python3 -c "
import sys,json
d=json.load(sys.stdin)
for s in d.get('steps',[]):
    if s.get('conclusion') == 'failure':
        print(f'FAILED STEP: {s[\"name\"]} number={s[\"number\"]}')
"

# 3. Ver logs do job
gh api repos/orlandoburli-enterprise/project-erp/actions/jobs/<job-id>/logs | grep -E "FAIL|Error|panic|exit" | grep -v "retry\|Download\|Extract\|upload\|cache" | head -20
```

**Padrões comuns de falha:**
- `API checks`: golangci-lint (errcheck, staticcheck, unused) — corrigir os lint issues localmente
- `API test`: migrations faltando ou testes quebrados — rodar `go test ./...` localmente
- `Web (lint, test, build)`: testes quebrados ou erros de Typescript — rodar `npx next lint` e `npx jest` localmente
- `Web E2E Real`: timeouts ou falhas funcionais — verificar se o teste é pré-existente antes de mexer
- `Migration collision check`: duas migrations com a mesma data/hora — renomear a mais recente

**Regra absoluta: não fazer merge em main se o CI completo (via workflow_dispatch) estiver vermelho.** Pelo menos 3 failures concretos para resolver antes de confiar na main.

## Correção massiva de testes pré-existentes

Para cenários onde 3+ grupos de falha interdependentes impedem PRs individuais de passarem no CI, veja `references/massive-ci-fix-workflow.md`. O workflow usa um branch consolidado com delegate_task paralelo, correção local completa, e push único — garantindo CI verde na primeira tentativa e zero gastos com GitHub Actions debug.
