# Design: Foundation

## Spec Structure

```
openspec/
├── CHANGELOG.md
├── specs/
│   ├── visao-geral/spec.md     # Project overview, key concepts, workflow
│   ├── arquitetura/spec.md     # Technology choices, ADRs
│   ├── schema/spec.md          # apiary.yaml full schema reference
│   ├── plugin-api/spec.md      # SourceAdapter + RunnerAdapter interfaces
│   ├── cli/spec.md             # CLI commands and flags
│   └── roadmap/spec.md         # Milestone plan
└── changes/
    └── foundation/             # This change
        ├── proposal.md
        ├── design.md
        └── tasks.md
```

## Core Data Model

```
apiary.yaml
  └── sources[]        SourceAdapter per type
  └── workers[]        RunnerAdapter per type  +  model (opaque string)
  └── routes[]         priority-ordered matching rules
  └── settings         global config

Runtime flow:
  Source.Poll() → []Cell → Router.Match() → Worker → Runner.Run() → RunResult → Source.WriteResult()
```

## Key Design Decisions (summary)

| Decision | Choice |
|---|---|
| Language | Go — single binary, no runtime |
| Trigger | Poll-first, webhook-optional |
| Routing | Priority integer, first-match-wins |
| Runners | Thin CLI wrappers — no embedded SDKs |
| Model IDs | Opaque strings — Apiary never interprets them |
| History | Embedded SQLite (v0.3+) |
| Plugins | Go interfaces (v1), gRPC protocol (v2) |

See [arquitetura/spec.md](../../specs/arquitetura/spec.md) for full ADRs.
