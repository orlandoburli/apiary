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

Uma camada de autoria **inspirada no GitHub Actions** (`steps:` plano e ordenado,
`if:` por step, expressões `${{ … }}`, `steps.<id>.outputs.<field>`), que **compila
para o engine de DAG já existente** (ele já faz sequência via ordem e loop via
`on_fail.goto`):

1. **Sequência implícita.** Steps numa lista rodam **na ordem declarada**. Sem
   `depends_on` para o caso comum.
2. **`if:` por step (estilo GitHub Actions)**, no lugar de `split`/`goto`. Step com
   `if:` falso é pulado; ramificação = steps com condições complementares. Sem
   `then/else`, sem join explícito (o step seguinte sem `if:` simplesmente roda).
3. **Referência a saída por id de step:** `${{ steps.review.outputs.verdict }}`
   (abreviado `review.verdict`) — sem `memory.write` manual.
4. **Gates de aprovação:** `reject_when: <expr>` + `on_reject: { restart_from: <step>, max: N }`.
   O agente emite um veredito estruturado; o step "reprova" logicamente e o fluxo
   volta para um step anterior (loop-back), reaproveitando o mecanismo de retry do
   engine. (É o que falta no Actions: "reprovou → refaz um step anterior".)
5. **Composição (já no spec original):** `for_each:` (loop sobre itens-filho — GHA
   `strategy.matrix`), `uses:` (sub-workflow reutilizável — GHA reusable workflows)
   e `parallel:` (fan-out + join — GHA jobs paralelos). Status honesto: foreach e
   sub-workflow já existem no engine (foreach roda **serial**); **parallel não
   existe** (o executor roda um step por vez). Ver design.md §8.

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

      # Complexo: Staff documenta a solução e quebra em sub-issues → Fim.
      # (as sub-issues são issues novas e voltam pelo `triage`.)
      - id: design
        agent: staff
        if: ${{ classify.track == 'complex' }}

      # Implementação: sequência real, não um único agente.
      - id: implement
        agent: engineer                          # implementa + abre PR
        if: ${{ classify.track == 'implement' }}

      - id: review
        agent: reviewer                          # aprova / reprova o PR
        if: ${{ classify.track == 'implement' }}
        output: { verdict: { enum: [approved, rejected] } }
        reject_when: ${{ review.verdict == 'rejected' }}
        on_reject: { restart_from: implement, max: 3 }

      - id: qa
        agent: qa                                # testa, aprova / reprova
        if: ${{ classify.track == 'implement' }}
        output: { verdict: { enum: [approved, rejected] } }
        reject_when: ${{ qa.verdict == 'rejected' }}
        on_reject: { restart_from: implement, max: 3 }

    on_complete: { set_state: closed }
    on_fail:     { add_labels: [needs-attention] }   # esgotou os retries → humano
```

Compare com a sintaxe atual (split + goto + depends_on + memory.write): o fluxo
acima é lido de cima para baixo como um job do GitHub Actions, a ramificação é
`if:` por step, e o loop de reprovação é uma linha.

## Compila para o engine atual (sem reescrita)

| Superfície nova            | Lowering no DAG atual                                  |
|----------------------------|-------------------------------------------------------|
| ordem dos steps (sequência)| `depends_on` = step anterior                           |
| `if:` por step             | `condition` no step (pulado quando falso)              |
| `steps.x.outputs.y` / `x.y`| reescrito p/ `memory.y` (parser auto-adiciona ao `memory.write` do step x) |
| `reject_when`              | `fail_when` (Success derivado de expressão sobre a saída)|
| `on_reject.restart_from`   | `on_fail.goto` + `max_retries` (já reseta e re-roda)   |

São **duas** capacidades novas de engine, ambas pequenas: `fail_when` (hoje
`Success = (exit == 0)`, cli.go:197 — derivar o resultado da saída estruturada) e
`condition` por step (pular quando o `if:` é falso, estilo GitHub Actions). Todo o
resto é açúcar de autoria que baixa para primitivas existentes (`depends_on`,
`on_fail.goto`).

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

- Reescrever o executor de DAG.
- **Execução paralela real** (`parallel:` concorrente + semáforo global de
  `concurrency`) e `for_each.concurrency` honrado — é o `concurrency-model` original,
  o maior esforço; vira change própria **depois** da v2 sequencial + gates. Nesta
  change `parallel:` é aceito mas roda serial (sinalizado, não é no-op silencioso).
- Loops gerais / `while` arbitrário além de `for_each` e do loop-back de gate.
- Comando de migração automática da sintaxe antiga → v2.
