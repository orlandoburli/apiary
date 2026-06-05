# Workflow authoring v2 — sequência + if/else + gates de aprovação

> Status: **proposed (design only)** — sem implementação. Documento para revisão.

## Motivação

O modelo atual de definição de workflow ainda "cheira a router": na prática um
`triage` classifica e manda a issue para **um** agente, e acabou. Além disso, a
forma de *escrever* o workflow é de baixo nível demais:

- **`depends_on` é verboso e indireto.** O autor tem que declarar arestas de um
  grafo para expressar algo que quase sempre é uma sequência simples. Se eu listo
  vários steps, a ordem em que escrevi **já deveria ser** a ordem de execução.
- **`split` + `branches` + `goto` é mecânica de labels/jump.** Para ramificar eu
  pulo para steps soltos por id; o fluxo fica difícil de ler e fácil de errar.
- **Não existe gate de aprovação/rejeição limpo.** Hoje um step só "falha" se o
  processo do agente sai com código ≠ 0 — o que confunde *rejeição deliberada* (o
  reviewer reprovou) com *erro* (o agente quebrou). Sem isso, não dá para modelar
  "reviewer reprova → engenheiro refaz".

O resultado é que pipelines reais — Investigator → Engineer → Reviewer → QA, com
loop-back em reprovação — não são expressáveis de forma natural.

## O que muda (superfície de autoria)

Uma camada de autoria **sequencial e estruturada**, que **compila para o engine de
DAG já existente** (sem reescrever o executor — ele já faz sequência via ordem,
ramificação via split e loop via `on_fail.goto`):

1. **Sequência implícita.** Steps numa lista rodam **na ordem declarada**. Sem
   `depends_on` para o caso comum.
2. **`if` / `then` / `else`** com blocos aninhados de steps, no lugar de
   `split`/`goto`.
3. **Referência a saída por id de step:** `classify.track`, `review.verdict` —
   sem `memory.write` manual.
4. **Gates de aprovação:** `reject_when: <expr>` + `on_reject: { restart_from: <step>, max: N }`.
   O agente emite um veredito estruturado; o step "reprova" logicamente e o fluxo
   volta para um step anterior (loop-back), reaproveitando o mecanismo de retry do
   engine.

### Os dois fluxos-alvo, na sintaxe nova

```yaml
workflows:
  - id: triage
    trigger:
      match: { source: project-erp, exclude_label_prefix: "agent:" }
    steps:
      - id: classify
        agent: investigator
        output: { track: { enum: [implement, complex] } }   # decisão estruturada

      - if: 'classify.track == "complex"'
        then:
          # Complexo: Staff documenta a solução e quebra em sub-issues → Fim.
          # (as sub-issues são issues novas e voltam pelo `triage`.)
          - agent: staff

        else:
          # Implementação: sequência real, não um único agente.
          - id: implement
            agent: engineer                      # implementa + abre PR

          - id: review
            agent: reviewer                      # aprova / reprova o PR
            output: { verdict: { enum: [approved, rejected] } }
            reject_when: 'review.verdict == "rejected"'
            on_reject: { restart_from: implement, max: 3 }

          - id: qa
            agent: qa                            # testa, aprova / reprova
            output: { verdict: { enum: [approved, rejected] } }
            reject_when: 'qa.verdict == "rejected"'
            on_reject: { restart_from: implement, max: 3 }

    on_complete: { set_state: closed }
    on_fail:     { add_labels: [needs-attention] }   # esgotou os retries → humano
```

Compare com a sintaxe atual (split + goto + depends_on + memory.write): o fluxo
acima é lido de cima para baixo, a ramificação é um `if/else`, e o loop de
reprovação é uma linha.

## Compila para o engine atual (sem reescrita)

| Superfície nova            | Lowering no DAG atual                                  |
|----------------------------|-------------------------------------------------------|
| ordem dos steps (sequência)| `depends_on` = step anterior                           |
| `if/then/else`             | `split` com `branches` + `goto`; `else` = fallback     |
| `step.field`               | contexto de avaliação (`memory`/`steps`) já existente   |
| `reject_when`              | `fail_when` (Success derivado de expressão sobre a saída)|
| `on_reject.restart_from`   | `on_fail.goto` + `max_retries` (já reseta e re-roda)   |

A **única** capacidade nova de engine é `fail_when`: hoje `Success = (exit == 0)`
(cli.go:197); precisamos derivar o resultado do step de uma expressão sobre a
saída estruturada. Todo o resto é açúcar de autoria que baixa para primitivas
existentes (`depends_on`, `split`, `on_fail.goto`).

## Escopo / decisões em aberto

- **Compatibilidade:** manter `depends_on`/`split` funcionando (são a forma
  "baixa" para a qual a v2 compila) ou deprecá-los? Proposta: manter, documentar a
  v2 como recomendada.
- **Feedback de reprovação no loop:** `on_fail.goto` reseta o step alvo e seus
  dependentes e **apaga** a contribuição de memória deles — então o feedback do
  reviewer não chega ao engenheiro via memória no retry. Proposta: o feedback vive
  no **PR** (review real do GitHub); o engenheiro relê o PR no retry. Memória só
  carrega o veredito do gate. (Ver design.md.)
- `settings.retry_policy` continua fora (inerte; decisão à parte).

## Não-objetivos

- Reescrever o executor de DAG.
- Loops gerais / `while` arbitrário além do loop-back de gate.
- Comando de migração automática da sintaxe antiga → v2.
