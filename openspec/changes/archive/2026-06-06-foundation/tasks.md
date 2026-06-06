# Tasks: foundation

## T1 — Specs
- [x] `openspec/specs/visao-geral/spec.md`
- [x] `openspec/specs/arquitetura/spec.md`
- [x] `openspec/specs/schema/spec.md`
- [x] `openspec/specs/plugin-api/spec.md`
- [x] `openspec/specs/cli/spec.md`
- [x] `openspec/specs/roadmap/spec.md`

## T2 — Repository setup
- [x] Create GitHub repo `orlandoburli/apiary` (public)
- [x] LICENSE — AGPLv3
- [x] COMMERCIAL.md — dual licensing terms
- [x] README.md
- [x] `openspec/CHANGELOG.md`

## T3 — Go scaffold (next)
- [ ] `go mod init github.com/orlandoburli/apiary`
- [ ] Define package structure (`cmd/`, `internal/`, `sdk/`)
- [ ] Implement core interfaces (`SourceAdapter`, `RunnerAdapter`, `Cell`)
- [ ] CLI skeleton with cobra (`apiary run`, `apiary validate`, `apiary status`)
- [ ] `apiary.yaml` schema validation
