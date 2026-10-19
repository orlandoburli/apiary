# Proposal: Apiary 2.0 — workflows como grafo de nós, no estilo n8n

> **Visão + roadmap.** Esta change não descreve uma implementação única. Ela fixa a direção do
> 2.0 e divide o caminho em marcos (`tasks.md`). Cada marco vira sua própria change, com
> `design.md`, quando for começar.

## Why

Em outubro de 2026 o Apiary foi portado para cima do n8n (repo `n8n-agents`, o "hub"), para
ganhar a camada que o n8n tem e o Apiary não tem: um editor visual, execuções inspecionáveis nó a
nó e triggers de qualquer tipo. O experimento funcionou o bastante para rodar o `project-erp`.
Ele também mostrou o que custa *não* ter essa camada dentro do Apiary:

- **A lógica fica em três lugares.** JS em strings dentro de Code nodes do n8n
  (`workflows/src/build.mjs`, com o `40-implementation.json` chegando a mais de mil linhas),
  Bash com `jq` (`scripts/poll.sh`, `issue-finish.sh`, `stuck-scan.sh` com 549 linhas) e YAML
  (`project.yaml`). Um bug de roteamento pode estar em qualquer um deles.
- **O estado é frágil.** Locks são diretórios com TTL de 3 h, a fila são arquivos JSON, e a
  tabela de jobs do runner existe só em memória. Tudo isso já existe, durável, no SQLite do
  Apiary.
- **Um restart mata execuções longas.** Reiniciar o n8n durante um import deixou 15 execuções
  órfãs, com jobs e locks presos (`ed0a38f`). O `wait_for` estacionado do Apiary já sobrevive a
  restart.
- **Falsos negativos ficam invisíveis.** Um trap `EXIT` que devolvia 1 marcou como falha toda
  execução de agente com slot (`22e1a4d`). Um loop de `unblock` re-disparava a cada 5 min. Os
  dois só apareceram lendo logs, porque não havia input e output por passo para inspecionar.

Já o que o n8n trouxe de bom não depende do n8n:

- **Nós tipados**, com um descriptor que gera formulário.
- **Triggers como nós.**
- **runData por nó**: input, output, erro e tempo, com "executar a partir daqui" e dados fixados.
- **Canvas** para montar e depurar o fluxo.

O Apiary já tem o motor certo: DAG com `if`, `parallel`, `for_each`, `reject_when`,
sub-workflows tipados, approvals e `wait_for` estacionado, eventos versionados com SSE, fallback
de runners, plugins multi-linguagem, fila distribuída e replay imutável. O 2.0 coloca a camada do
n8n sobre esse motor, em vez de colocar o Apiary sobre o n8n.

**Licença.** O n8n usa a *Sustainable Use License*, que é fair-code e não é OSI. O Apiary é
BSD-3. Daqui pegamos **conceitos**, nunca código. O canvas usa xyflow/React Flow (MIT).

---

## What Changes

1. **Uma IR única de workflow (Workflow v2):** `nodes[]` + `connections` + `ui`. Todo YAML que
   existe hoje (sequencial v2 ou DAG) é rebaixado para ela. O engine executa só a IR.
2. **Um registry de NodeTypes.** Cada passo vira um nó de um tipo versionado, com um descriptor
   declarativo. Os tipos de passo de hoje (agent, approval, wait_for, subworkflow) são os
   primeiros nós.
3. **`node_runs`:** input e output persistidos por nó e por tentativa, junto com tokens e custo.
4. **Triggers como nós,** com ciclo de ativação e estado persistido.
5. **Uma UI web servida pelo daemon:** primeiro as execuções (somente leitura, mais re-run),
   depois o editor canvas. A TUI continua existindo.
6. **Plugins ganham a capability `node`,** além de `source`.
7. **Credentials** saem das variáveis de ambiente e vão para um store tipado.

---

## Ideias do n8n e como entram

| Conceito do n8n | No Apiary 2.0 | Base existente |
|---|---|---|
| `INodeType` + descriptor (`properties`, inputs/outputs, `version`) | Interface `NodeType` em Go. O descriptor traz um JSON Schema dos parâmetros, as portas e um `typeVersion`. A UI gera o formulário a partir dele. Um workflow fixa a versão do tipo, então tipos novos não quebram workflows antigos. | Os tipos de passo em `src/internal/workflow` |
| Workflow = `nodes` (com `position`) + `connections` | Workflow v2 como IR. O YAML continua sendo uma forma de escrever: é rebaixado, como o sequencial v2 já é. O layout fica em `ui`. | `dag.go`, a change `workflow-sequential-authoring` |
| Cluster nodes (AI Agent + model, memory, tools) | Nó **Agent** com sub-portas `runner` (com a cadeia de fallbacks), `memory`, `tools` (MCP, skills) e `output` (schema). | Agents, fallbacks, memória tiered |
| `runData` por nó | Tabela `node_runs` (evolução de `step_runs`): input e output JSON, tentativa, erro, duração, tokens, custo, `pinned`. | `step_runs`, `task_executions`, `immutable-execution-replay` |
| Expressões `{{ $json }}`, `$node["X"]` | `${{ }}` estendido com `nodes.<id>.output`, `trigger.*`, `env.*` e `memory.*`. O editor dá autocomplete a partir da última execução. | `expr.go` (lint e fail-loud) |
| Pin data, execução parcial | "Re-executar a partir deste nó" reaproveitando as saídas a montante que já estão persistidas. Dados fixados por nó, para testar sem gastar tokens. | `resume.go`, replay |
| Wait node com `waitTill` | Nó **Wait** com os modos `time`, `ci`, `dependency`, `webhook` e `approval`, todos estacionados. | `wait_park.go`, `approval.go` |
| Trigger nodes e ativação | Nós de trigger com `Activate`/`Deactivate`: Source match, Schedule, Webhook, GitHub event, Queue, Slack. Estado persistido por trigger (o *static data* do n8n). | `trigger.match`, `on:` (PR events), plugin `dev.apiary.routines` |
| Retry on fail, `continueOnFail`, error output, Error Workflow | Política por nó: `retry {max, backoff}`, `timeout`, `on_error: fail \| continue \| error_output`. Mais um `error_workflow` por projeto. A escalada de runner do hub vira uma política de retry do nó Agent (subir de tier a cada falha). | `docs/resilience.md` |
| Credentials | Um store tipado e criptografado (GitHub, Slack, API keys), referenciado pelos nós. Substitui os `GITHUB_TOKEN_*` por agente. | Seção "Trust and secrets" em `docs/plugins.md` |
| Queue mode (main, worker, webhook) | Já existe. | `persistent-distributed-queue`, `docs/distributed-workers.md` |
| Execute Workflow | Nó **Sub-workflow**. | `reusable-typed-subworkflows` |
| Draft, publicado e histórico | O arquivo no git é a fonte da verdade. O canvas salva um draft e "publicar" roda `apiary validate` e grava. A versão publicada é a que o daemon carrega. | `apiary validate` |

### O que não adotamos

- **Item arrays em todo nó.** Agentes trabalham com um documento por execução. Iteração continua
  explícita (`for_each`), em vez de implícita em toda conexão.
- **Code node com JS arbitrário.** Transformações simples ficam num nó **Set/Transform**, feito
  só com expressões. Lógica de verdade vai para o nó **Shell/Script** (ver abaixo) ou para um
  plugin.
- **Workflows guardados só no banco.** O git continua sendo a fonte da verdade, e o banco guarda
  execuções.

---

## Revendo decisões aposentadas

O roadmap diz que um item em **Retired** só volta "com evidência nova". O 2.0 toca em três deles.
A evidência é o mesmo usuário concreto, o `project-erp`, que precisou de cada um no hub:

| Item aposentado | Evidência nova | Proposta |
|---|---|---|
| **Webhook / push ingestion** (polling-only, PR #364 fechado) | O hub usou um webhook (`/webhook/agents-poll`) para disparar o dispatcher na hora em vez de esperar o próximo ciclo de 1 min. O trigger Webhook é uma das quatro exigências do MVP. | O polling continua sendo o padrão e o caminho garantido. Um listener HTTP **opcional**, desligado por padrão, só em localhost ou atrás de um túnel, com segredo HMAC por rota. Um webhook nunca é a única forma de descobrir trabalho: ele só antecipa o poll. Antes do design de M3, reler o motivo de o PR #364 ter sido fechado. |
| **Scheduled routines in core** (virou o plugin `dev.apiary.routines`) | O hub tem routines cron (`stuck-check` a cada 5 min), e o parser de cron do hub não fazia catch-up. | **Não** trazer de volta para o core. O nó Schedule é um trigger de plugin que fala com `dev.apiary.routines`, e esse plugin ganha `catch_up`. A decisão original continua de pé: o que muda é só a forma de nó. |
| **Shell `script` runner** | `stuck-scan.sh`, `gate.sh` e `issue-finish.sh` são passos determinísticos que hoje dependem de um agente ou de Bash solto. | Um nó **Shell/Script**, que não é um runner novo. Ele roda um comando com o mesmo sandbox, `working_dir` e env dos passos de agente, e devolve JSON. |

---

## Arquitetura-alvo

```
 git: apiary.yaml + workflows/*.yaml|json ──(load/validate)──► Daemon (apiary run)
                                                      │
          ┌───────────────────────────┬───────────────┼───────────────────┐
          ▼                           ▼               ▼                   ▼
   Trigger nodes               Engine (IR → DAG)  NodeType registry    HTTP API
   (source, schedule*,         wait/park/resume   ├ nativos (Go)       unix socket +
    webhook*, gh event,        node_runs, events  └ plugins (stdio,    TCP localhost/token
    queue, slack*)                    │              capability node)  ├ /node-types
          *via plugin                 ▼                                ├ /workflows (r/w)
                                 SQLite (.apiary/apiary.db)            ├ /executions
                                                                       ├ /events/stream (SSE)
                                                                       └ /  → UI web (go:embed)
```

- **Daemon.** A API HTTP de hoje (unix socket, `events_http.go`, `approval_http.go`,
  `manual_run.go`) ganha um listener TCP opcional em localhost, protegido por token. A UI e os
  webhooks usam esse listener.
- **Nós nativos** ficam em `src/internal/nodes/<tipo>`. **Nós de plugin** usam o protocolo 1
  (JSON sobre stdio). O manifest declara a capability `node` e expõe `describe` (o descriptor) e
  `execute` (input → output), validados pelo conformance kit.
- **UI** em `web/` (React + xyflow), compilada para o `go:embed` e entregue no binário único.
  Nenhum serviço a mais para operar.
- **Fonte da verdade.** O canvas lê e escreve os arquivos de workflow através da API, e o diff
  aparece no git. O banco guarda execuções, triggers e credenciais.

---

## Migração do hub `n8n-agents`

| No hub | No Apiary 2.0 |
|---|---|
| `ag-dispatcher` (schedule de 1 min + webhook → `poll.sh` → Switch) | Triggers Source match e Webhook. O roteamento por prioridade continua igual. |
| `ag-implementation` (implement → wait-ci → gate review+QA → merge) | Um workflow com os nós Agent → Wait(ci) → parallel(Agent review, Agent qa) → If → GitHub merge. |
| `pipelines:` (variantes como `implementation-mobile`) | Workflows separados, ou sub-workflows tipados. |
| `tasks:` (label, fila ou Slack → um agente) | Workflows de um nó Agent, com trigger Source match, Queue ou Slack. |
| `routines:` + `stuck-scan.sh` | Trigger Schedule → nó Shell → nó Sub-workflow para cada item. |
| Escalada sem humano (`escalation` tiers + backoff linear) | Política de retry do nó Agent com `escalate: [tier…]`. |
| `.data/` (locks, fila, slots, paused) | SQLite: instâncias, fila persistente, pausa de runner. |
| `usage.jsonl` | `node_runs` e `task_executions`, que já registram tokens e custo. |

---

## Riscos

- **O canvas sai caro.** Por isso M5 vem depois de M4. A UI de execuções sozinha já resolve a dor
  maior (ver o que cada passo recebeu e devolveu).
- **Ida e volta entre YAML e IR.** Um YAML escrito à mão, editado no canvas e salvo de novo pode
  perder comentários e formatação. A decisão vai para o design de M5. A opção de partida: o canvas
  grava a IR em JSON, e arquivos YAML abrem como somente leitura ou passam por uma conversão
  explícita, feita uma vez.
- **Superfície HTTP.** O listener TCP e os webhooks ampliam a superfície de ataque. Padrões:
  desligado, só localhost, token, HMAC.
- **Escopo.** Isto é um 2.0. Cada marco precisa entregar valor sozinho e manter `apiary.yaml` 1.x
  funcionando.

## Out of Scope

- Hospedagem multi-tenant ou SaaS.
- Um marketplace de templates de workflow.
- Execução de código JS arbitrário.
- Trocar o SQLite por Postgres (continua aposentado; existe o `apiary-pgsink`).
