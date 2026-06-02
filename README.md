# Apiary

**Task-driven agent orchestration. Route work from any task system to the right AI agent and model.**

Apiary is an open-source harness that connects project management tools (Jira, Plane, GitHub Issues, Linear) to AI agent runners and routes each task to the right agent profile and LLM model based on declarative rules.

```
Task System ──► Apiary ──► Agent Profile ──► LLM Model
   (Plane)      (router)    (backend-dev)     (gpt-4o)
```

## Why Apiary?

Most AI coding agents require a human to manually pick a task, paste context, and trigger a run. Apiary closes the loop: tasks flow in from your PM tool, Apiary decides which agent and model handles each one, and work gets done without a human dispatcher in the middle.

## Key Concepts

| Term | Meaning |
|---|---|
| **Hive** | A configured Apiary instance (`apiary.yaml`) |
| **Source** | A task system integration (Plane, Jira, Linear…) |
| **Worker** | An agent profile — a named combo of runner + model + prompt context |
| **Route** | A rule that maps task attributes to a Worker |
| **Cell** | A single task unit flowing through the system |

## Quick Example

```yaml
# apiary.yaml
sources:
  - id: main-plane
    type: plane
    config:
      workspace: my-workspace
      project: my-project
      api_key: ${PLANE_API_KEY}
    filters:
      labels: [ai-ready]

workers:
  - id: backend-dev
    runner: opencode
    model: openai/gpt-4o
    config:
      working_dir: /workspace/my-project
      max_turns: 10

routes:
  - id: backend-bugs
    priority: 10
    match:
      labels: [backend, bug]
    worker: backend-dev
```

```sh
apiary run
```

## Specifications

All specs live in [`openspec/`](openspec/):

| Spec | Description |
|---|---|
| [Vision](openspec/specs/visao-geral/spec.md) | Overview, key concepts, workflow |
| [Architecture](openspec/specs/arquitetura/spec.md) | Technology choices and ADRs |
| [Config Schema](openspec/specs/schema/spec.md) | Full `apiary.yaml` reference |
| [Plugin API](openspec/specs/plugin-api/spec.md) | Source and Runner adapter interfaces |
| [CLI](openspec/specs/cli/spec.md) | Commands, flags, exit codes |
| [Roadmap](openspec/specs/roadmap/spec.md) | Milestone plan |

## Status

> Pre-alpha. Specification phase.

## License

Apiary is dual-licensed:

- **Open source** — [AGPLv3](LICENSE) for open-source and internal use.
- **Commercial** — a commercial license is available for proprietary products and SaaS deployments. See [COMMERCIAL.md](COMMERCIAL.md).
