# Config lint — detectar diretivas removidas e campos desconhecidos

## Motivação

Um pipeline real (project-erp) quebrou silenciosamente: só o primeiro agente
(investigator) rodava. A causa foi `on_complete.assign_from_output: true` — uma
diretiva **removida** quando o workflow engine virou o único caminho de dispatch.
O campo ainda existe na struct e faz parse, mas nada o consome, então o engine
nunca roteava a issue adiante (e ela re-triava em loop). `apiary validate` dizia
`✓ config is valid`.

A raiz foi misconfiguração, mas o problema mais profundo é que o apiary **aceita
diretivas removidas e campos desconhecidos em silêncio**. Quem edita o config não
tem como saber que uma diretiva está morta.

## O que muda

- **Registry de diretivas removidas** (`removedDirectives`): hoje cobre
  `on_complete.assign_from_output` e `on_complete.assign_label_prefix`. Cada
  entrada vira um **erro de validação** com a linha do YAML e uma mensagem que
  aponta o substituto (workflow `triage` com passo `split`).
- **Modo estrito (KnownFields)**: qualquer chave sem campo correspondente na struct
  (typo como `lables`, diretiva inexistente) vira erro, em vez de ser ignorada.
- Ambos rodam dentro de `(*Config).Validate()`, que `apiary validate` e `apiary run`
  já tratam como erro fatal — logo a diretiva morta passa a **bloquear o start**
  até o config ser migrado.
- `apiary run` passa a imprimir também os `WorkflowWarnings()` no startup (antes só
  apareciam no `validate`).
- Exemplos migrados: `example-apiary-full.yaml` deixa de usar `assign_from_output`
  (passa a usar `triage`/`split`); `example-with-recovery.yaml` troca o campo
  inexistente `preferred_models` pelo `model` correto.

## Fora de escopo

- `settings.retry_policy`: a máquina de retry está inerte (a fila nunca é
  populada; `ShouldRetry`/`IsRetriable` sem callers), mas há um exemplo dedicado
  (`example-with-recovery.yaml`). Decidir entre remover ou religar o retry é uma
  change à parte.
- Comando `apiary migrate` que reescreve o config legado automaticamente
  (possível follow-up).

## Impacto

- Mudança de comportamento **intencional**: configs com diretiva removida passam a
  falhar no `validate`/`run` até serem migrados. É exatamente o "forcing function"
  pedido.
- Blast radius pequeno: `Validate()` tem dois callers, ambos já falham nos seus
  erros. Risco principal são falsos positivos do modo estrito — coberto por teste
  que faz lint de todos os exemplos versionados.
