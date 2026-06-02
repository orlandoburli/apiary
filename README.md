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

## CLI Runner — Personal Use

Apiary supports a `cli` runner adapter that invokes agent CLI tools (such as `opencode`, `gemini`, or similar) as subprocesses on your local machine.

```yaml
workers:
  - id: my-worker
    runner: cli
    model: openai/gpt-4o
    config:
      command: opencode       # CLI binary on your PATH
      model_flag: "--model"   # flag used to pass the model
      working_dir: /my/repo
```

**Important:** Apiary never handles, stores, intercepts, or transmits authentication credentials of any kind. CLI tools manage their own authentication independently. The `cli` runner simply invokes the binary — it has no knowledge of how the tool authenticates.

This runner is intended for **personal use on your own machine**, where you have already set up and authenticated the CLI tool yourself. For shared or team deployments, use an API-key-based runner instead (see roadmap).

## Dashboard

Apiary ships a terminal dashboard for monitoring task execution, agent
performance, and logs in real time:

```sh
apiary dashboard
```

See [`docs/dashboard.md`](docs/dashboard.md) for a per-tab data reference —
what each tab shows, the source table and query behind every field, the
refresh model, and current data gaps.

## Status

> Pre-alpha. Implementation in progress.

## License

Apiary is dual-licensed:

- **Open source** — [AGPLv3](LICENSE) for open-source and internal use.
- **Commercial** — a commercial license is available for proprietary products and SaaS deployments. See [COMMERCIAL.md](COMMERCIAL.md).
