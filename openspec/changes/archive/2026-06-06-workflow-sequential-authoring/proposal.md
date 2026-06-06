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

Uma **árvore de steps aninhada** (inspirada no GitHub Actions para steps/`if:`/
`${{ … }}`, mas com **composição por aninhamento visual**, não por referência), que
**compila para o engine de DAG já existente**:

1. **Sequência implícita.** Steps numa lista `steps:` rodam **na ordem declarada**.
   Sem `depends_on` na autoria.
2. **Composição = aninhamento visual.** Se um step tem sub-steps, paralelo ou loop,
   esses steps ficam **dentro** dele. Sem `uses:`/referência e sem `depends_on`
   entre steps — a indentação *é* o fluxo. Mais fácil de ler.
3. **Ramificação = grupo guardado.** `if:` num grupo roda/pula a subárvore inteira
   (track complexo vs track de implementação) — a condição aparece **uma vez por
   track**, não repetida em cada step.
4. **Paralelo é explícito.** Só roda concorrente o que estiver aninhado num step
   `parallel:`, e **esse step decide o desfecho (join)**: `all` (default) / `any` /
   `${{ expr }}`. O sucesso do step paralelo = o join, alimentando o próximo step e
   `on_reject`.
5. **Loop sobre itens-filho:** `for_each:` + `as:` com **corpo aninhado** (`steps:`)
   rodando por item (GHA `strategy.matrix`).
6. **Gates de aprovação:** `reject_when: <expr>` + `on_reject: { restart_from: <step>, max: N }`.
   O agente emite um veredito estruturado; o step "reprova" logicamente e o fluxo
   volta para um step-irmão anterior (loop-back). (É o que falta no Actions.)
7. **Referência a saída por id:** `${{ steps.review.outputs.verdict }}` (abreviado
   `review.verdict`) — sem `memory.write` manual.
8. **Execução concorrente (em escopo):** scheduler concorrente + semáforo global de
   `settings.concurrency`. Aditivo: com `concurrency: 1` o comportamento é idêntico
   ao executor sequencial de hoje. Ver design.md §8e.

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

      # Track complexo: Staff documenta + quebra em sub-issues → Fim. (Grupo
      # guardado: o if: cobre a subárvore toda.)
      - id: complex-track
        if: ${{ classify.track == 'complex' }}
        steps:
          - { id: design, agent: staff }

      # Track de implementação: grupo aninhado, sequência real.
      - id: implement-track
        if: ${{ classify.track == 'implement' }}
        steps:
          - { id: implement, agent: engineer }     # implementa + abre PR

          - id: review
            agent: reviewer                        # aprova / reprova o PR
            output: { verdict: { enum: [approved, rejected] } }
            reject_when: ${{ review.verdict == 'rejected' }}
            on_reject: { restart_from: implement, max: 3 }

          - id: qa
            agent: qa                              # testa, aprova / reprova
            output: { verdict: { enum: [approved, rejected] } }
            reject_when: ${{ qa.verdict == 'rejected' }}
            on_reject: { restart_from: implement, max: 3 }

    on_complete: { set_state: closed }
    on_fail:     { add_labels: [needs-attention] }   # esgotou os retries → humano
```

Compare com a sintaxe atual (split + goto + depends_on + memory.write): o fluxo
acima é lido de cima para baixo, a ramificação é um **grupo guardado** por `if:`, e
o loop de reprovação é uma linha.

## Compila para o engine atual (sem reescrita)

| Superfície nova (aninhada)  | Lowering no DAG atual                                  |
|-----------------------------|-------------------------------------------------------|
| ordem dentro de `steps:`    | `depends_on` = step-irmão anterior                     |
| grupo (`steps:` aninhado)   | sub-cadeia inline (ou sub-workflow anônimo p/ isolar)  |
| `parallel:` + `join`        | filhos concorrentes; desfecho do step = `join` (`all`/`any`/`${{ }}`) |
| `for_each:` + `steps:`      | `type: foreach` com corpo aninhado (sub-workflow anônimo se >1 step) |
| `if:` em qualquer step/grupo| `condition` no step (pula a subárvore quando falso)    |
| `steps.x.outputs.y` / `x.y` | reescrito p/ `memory.y` (parser auto-adiciona ao `memory.write` do step x) |
| `reject_when` / `on_reject` | `fail_when` / `on_fail.goto` + `max_retries`           |

São **três** capacidades novas de engine: `fail_when` e `condition` (ambas pequenas)
e o **scheduler concorrente + semáforo global** (o esforço maior, §8e). Todo o resto
é açúcar de autoria que baixa para primitivas existentes (`depends_on`, `foreach`,
`type: workflow`, `on_fail.goto`).

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

## Não-objetivos (desta change)

- Reescrever o executor de DAG **do zero** — o scheduler concorrente é evolução do
  atual (mesmas estruturas/IR), não reescrita.
- Loops gerais / `while` arbitrário além de `for_each` e do loop-back de gate.
- Comando de migração automática da sintaxe antiga → v2.
- Cancelamento de steps in-flight no loop-back (default: drenar e então resetar).
