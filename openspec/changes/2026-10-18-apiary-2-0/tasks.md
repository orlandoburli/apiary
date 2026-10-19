# Tasks: Apiary 2.0

Cada marco vira uma change própria (`proposal.md` + `design.md`) quando começar. O MVP é M0–M4.
`apiary.yaml` 1.x continua funcionando em todos os marcos.

## M0. IR e registry de nós

- [ ] 0.1 Definir o schema Workflow v2 (`nodes[]` com `id`, `type`, `typeVersion`, `parameters`, `ui.position`; `connections` por porta), publicado em `schema/`
- [ ] 0.2 Rebaixar YAML sequencial e DAG para a IR; o engine passa a executar só a IR
- [ ] 0.3 Testes de equivalência: toda a suíte `src/internal/workflow/dag_*_test.go` passa executando via IR
- [ ] 0.4 Interface `NodeType` (descriptor com JSON Schema de parâmetros, portas, versão; `Execute(ctx, input) → output`)
- [ ] 0.5 Portar `agent`, `approval`, `wait_for`, `subworkflow` para nós
- [ ] 0.6 `apiary nodes list|describe` e `GET /node-types`
- **Aceite:** um `apiary.yaml` existente (o hive do project-erp) roda sem mudanças, e `apiary workflow export --ir` mostra o grafo.

## M1. Execuções duráveis por nó

- [ ] 1.1 Tabela `node_runs` (instance, node, attempt, input/output JSON, erro, duração, tokens, custo, `pinned`), com migração a partir de `step_runs`
- [ ] 1.2 Política por nó: `retry {max, backoff: linear|exponential, step, cap}`, `timeout`, `on_error: fail|continue|error_output`
- [ ] 1.3 `escalate: [tier…]` no nó Agent (sobe de tier a cada falha, sem humano)
- [ ] 1.4 `error_workflow` por projeto, disparado com o contexto da falha
- [ ] 1.5 Nós declaram `idempotent: true|false`; a retomada não re-executa efeitos não idempotentes que já tiveram sucesso
- [ ] 1.6 Eventos `node.*` no timeline de `execution-events` (schema v1, aditivo)
- **Aceite:** matar o daemon (`kill -9`) durante um Wait(ci) e durante um nó Agent, reiniciar e ver a execução retomar sem duplicar comentário, push ou merge.

## M2. Catálogo de nós core

- [ ] 2.1 Agent (sub-portas `runner`, `memory`, `tools`, `output`)
- [ ] 2.2 Shell/Script (mesmo sandbox, `working_dir` e env dos agentes, e saída em JSON)
- [ ] 2.3 HTTP Request
- [ ] 2.4 If, Switch, Merge, Set/Transform (só expressões)
- [ ] 2.5 Wait (`time`, `ci`, `dependency`, `webhook`, `approval`)
- [ ] 2.6 Sub-workflow
- [ ] 2.7 GitHub (issue, comment, label, PR, merge) e Slack (post, reply)
- [ ] 2.8 Capability `node` no protocolo de plugin 1 (`describe`, `execute`), no SDK Go e no SDK Python, e fixtures no conformance kit
- **Aceite:** o fluxo `ag-implementation` do hub reescrito só com nós core, rodando num issue real.

## M3. Triggers

- [ ] 3.1 Contrato `TriggerNode` (`Activate`/`Deactivate`, estado persistido por trigger)
- [ ] 3.2 Source match (o `trigger.match` de hoje, com `priority`, `exclusive`, `once`)
- [ ] 3.3 Schedule via plugin `dev.apiary.routines`, com `catch_up`
- [ ] 3.4 Queue (`APIARY_SPAWN`) e GitHub event (o `on:` de hoje)
- [ ] 3.5 Reler o PR #364; listener Webhook opcional (desligado por padrão, localhost, HMAC por rota), que só antecipa o poll
- [ ] 3.6 Slack (thread → task), como plugin
- **Aceite:** um webhook do GitHub dispara o workflow certo em menos de 5 s, e o mesmo evento perdido é pego pelo poll seguinte.

## M4. UI de execuções (somente leitura + re-run)

- [ ] 4.1 Listener TCP opcional em localhost, com token; `apiary ui` abre o navegador
- [ ] 4.2 App `web/` (React + xyflow) embutido via `go:embed` no build do GoReleaser
- [ ] 4.3 Lista de execuções (filtros por workflow, estado, task, período)
- [ ] 4.4 Execução desenhada sobre o grafo, com status por nó ao vivo via `/events/stream`
- [ ] 4.5 Painel do nó: input, output, erro, logs e transcript, tokens e custo, tentativas
- [ ] 4.6 "Re-executar a partir deste nó" (reaproveita saídas a montante)
- [ ] 4.7 Responder approvals pela UI (mais um canal de `multi-channel-human-approvals`)
- **Aceite:** abrir uma execução do project-erp e achar, sem ler logs, em qual nó e com qual input ela falhou.

## M5. Editor canvas

- [ ] 5.1 Design da ida e volta YAML ↔ IR (ver Riscos no proposal)
- [ ] 5.2 Paleta de nós a partir do `/node-types`, formulários gerados pelo descriptor
- [ ] 5.3 Editor de expressões com autocomplete a partir da última execução
- [ ] 5.4 Pin data e "testar este nó"
- [ ] 5.5 Salvar draft → validar → publicar, com diff contra a versão publicada
- [ ] 5.6 Sticky notes
- **Aceite:** criar pelo canvas um workflow novo (trigger Schedule → Shell → Agent) e ver o arquivo no `git diff`.

## M6. Credentials e migração do hub

- [ ] 6.1 Credentials store tipado e criptografado (chave fora do repo), referenciado por nós
- [ ] 6.2 Migrar o `project-erp` do `n8n-agents`: pipelines, tasks, routines, `stuck-check`, escalada (ver a tabela "Migração" no proposal)
- [ ] 6.3 Desligar o hub n8n e arquivar o repo `n8n-agents`
- **Aceite:** o project-erp roda uma semana só no Apiary 2.0, sem nenhum falso negativo que só apareça lendo log.
