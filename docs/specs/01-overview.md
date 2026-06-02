# Apiary — Project Overview

## Vision

Apiary is an open-source task-driven agent orchestration harness. It bridges project management systems and AI agent runners, automating the dispatch of tasks to the right agent profile and LLM model without human intervention.

The core problem it solves: AI coding agents are powerful but require a human to manually select tasks, configure context, and trigger runs. Apiary removes that operator role by making task routing declarative and automatic.

## Goals

1. **Source-agnostic** — support any task system through a pluggable source adapter (Plane, Jira, Linear, GitHub Issues, Trello).
2. **Runner-agnostic** — support multiple agent runners through a pluggable runner adapter (Claude Code CLI, OpenCode, custom scripts).
3. **Model-aware routing** — assign different LLM models to different task types (e.g., complex architectural work → Claude Opus; documentation → Claude Haiku).
4. **Declarative config** — a single `apiary.yaml` defines sources, workers, and routing rules.
5. **Observable** — emit structured logs and events for every routing decision and agent run.
6. **Extensible** — a clean plugin API so the community can add sources, runners, and routing strategies.

## Non-Goals (v1)

- Apiary does not execute code directly — it delegates to agent runners.
- Apiary is not a task management system itself.
- Apiary does not manage secrets beyond reading them from environment variables.
- Apiary is not a general workflow engine (no DAGs, no branching pipelines beyond routing).

## High-Level Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                         Apiary                              │
│                                                             │
│  ┌──────────┐    ┌───────────┐    ┌───────────────────┐    │
│  │  Source  │───►│  Router   │───►│  Worker (Runner)  │    │
│  │ Adapters │    │  Engine   │    │     Adapters      │    │
│  └──────────┘    └───────────┘    └───────────────────┘    │
│       │               │                    │               │
│  Plane, Jira,    Route rules          Claude Code,         │
│  Linear, GH      + priority           OpenCode, CLI        │
│  Issues          matching                                   │
│                       │                                     │
│               ┌───────────────┐                             │
│               │  Model Config │                             │
│               │  (per worker) │                             │
│               └───────────────┘                             │
└─────────────────────────────────────────────────────────────┘
```

## Workflow

1. Apiary polls (or receives webhooks from) configured sources.
2. A new or updated task (Cell) enters the system.
3. The Router evaluates routing rules in priority order.
4. The first matching rule determines the Worker to use.
5. Apiary invokes the Worker's runner with the task context.
6. Results (output, status, logs) are written back to the source task and to Apiary's own log.

## Technology Choices

| Concern | Choice | Rationale |
|---|---|---|
| Runtime | Go | Single binary, cross-platform, easy distribution for OSS |
| Config format | YAML | Familiar to DevOps users, good tooling |
| Plugin system | Go interfaces + dynamic loading (v2) | Start with built-in adapters; open plugin API later |
| Observability | Structured JSON logs + optional OTLP traces | Easy to pipe into any stack |
| Distribution | GitHub Releases, Homebrew, Docker | Cover macOS/Linux dev + container deployments |
