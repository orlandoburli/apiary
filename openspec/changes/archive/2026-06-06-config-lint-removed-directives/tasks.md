# Tasks — config-lint-removed-directives

- [x] `src/internal/config/lint.go`: registry `removedDirectives` +
      `lintRemovedDirectives` (walk de `yaml.Node` p/ número de linha) +
      `lintUnknownFields` (decode estrito `KnownFields(true)`) + combinador `lint()`
      (no-op quando `rawContent` vazio).
- [x] `src/internal/config/validate.go`: chamar `c.lint()` em `Validate()`.
- [x] `src/internal/cli/run.go`: imprimir `WorkflowWarnings()` no startup.
- [x] Migrar `.apiary/example-apiary-full.yaml` para `triage`/`split`.
- [x] Corrigir `.apiary/example-with-recovery.yaml` (`preferred_models` → `model`).
- [x] `src/internal/config/lint_test.go`: assign_from_output → erro+linha+mensagem;
      typo → erro de campo desconhecido; todos os exemplos → lint limpo;
      `rawContent` vazio → nil.
- [x] `go build ./...`, `go vet`, `go test ./...` — tudo verde.
- [ ] Follow-up (change à parte): decidir destino de `settings.retry_policy`.
