# Apiary

**Task-driven agent orchestration. Route work from any task system to the right AI agent and model.**

Apiary is an open-source harness that connects project management tools (Jira, Plane, GitHub Issues, Linear) to AI agent runners (Claude Code, OpenCode) and routes each task to the right agent profile and LLM model based on configurable rules.

```
Task System ──► Apiary ──► Agent Profile ──► LLM Model
   (Plane)      (router)    (backend-dev)     (claude-opus)
```

## Why Apiary?

Most AI coding agents operate in isolation — you manually pick a task, paste context, run an agent. Apiary closes the loop: tasks flow in from your PM tool, Apiary decides which agent and model handles each one, and work gets done without a human dispatcher in the middle.

## Key Concepts

| Term | Meaning |
|---|---|
| **Hive** | A configured Apiary instance (`apiary.yaml`) |
| **Source** | A task system integration (Plane, Jira, Linear…) |
| **Worker** | An agent profile — a named combo of runner + model + prompt context |
| **Route** | A rule that maps task attributes to a Worker |
| **Cell** | A single task unit flowing through the system |

## Status

> Pre-alpha. Specification phase.

## License

Apache 2.0
