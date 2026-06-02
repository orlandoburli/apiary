# Apiary — Visão Geral

Apiary is an open-source task-driven agent orchestration harness. It connects project management tools to AI agent runners and routes each task to the right agent profile and LLM model based on declarative rules.

```
Task System ──► Apiary ──► Agent Profile ──► LLM Model
   (Plane)      (router)    (backend-dev)     (gpt-4o)
```

## Core Problem

AI coding agents are powerful but require a human operator to manually select tasks, configure context, and trigger runs. Apiary removes that operator by making the entire dispatch loop declarative and automatic.

## Key Concepts

| Term | Description |
|---|---|
| **Hive** | A configured Apiary instance — defined by `apiary.yaml` |
| **Source** | A task system integration (Plane, Jira, Linear, GitHub Issues…) |
| **Worker** | An agent profile — a named combination of runner, model, and prompt context |
| **Route** | A rule that maps task attributes (labels, type, source) to a Worker |
| **Cell** | A single normalised task unit flowing through the system |

## Goals

1. **Source-agnostic** — any task system via a pluggable Source adapter.
2. **Runner-agnostic** — any agent CLI via a pluggable Runner adapter.
3. **Model-aware routing** — assign different LLM models per task type (e.g. complex architectural work → large reasoning model; documentation → smaller model).
4. **Declarative config** — a single `apiary.yaml` defines everything.
5. **Observable** — structured logs and events for every routing decision and run.
6. **Extensible** — a clean plugin API so the community can add sources, runners, and routing strategies.

## Non-Goals (v1)

- Apiary does not execute code directly — it delegates to agent runners.
- Apiary is not a task management system.
- Apiary does not manage secrets beyond reading from environment variables.
- Apiary is not a general-purpose workflow engine (no DAGs, no branching pipelines).

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│                         Apiary                              │
│                                                             │
│  ┌──────────┐    ┌───────────┐    ┌───────────────────┐    │
│  │  Source  │───►│  Router   │───►│  Runner Adapter   │    │
│  │ Adapters │    │  Engine   │    │                   │    │
│  └──────────┘    └───────────┘    └───────────────────┘    │
│                       │                    │               │
│  Plane, Jira,    Route rules +         opencode,           │
│  Linear, GH      priority matching     custom CLIs         │
│  Issues                │                                   │
│               ┌────────────────┐                           │
│               │  Model Config  │                           │
│               │  (per worker)  │                           │
│               └────────────────┘                           │
└─────────────────────────────────────────────────────────────┘
```

## Workflow

1. Apiary polls (or receives webhooks from) configured sources.
2. A new or updated task (Cell) enters the system.
3. The Router evaluates routing rules in priority order.
4. The first matching rule selects the Worker.
5. Apiary invokes the Worker's runner with task context.
6. Results (output, status, logs) are written back to the source task and to Apiary's run log.

## Specifications

| Spec | Description |
|---|---|
| [Architecture](../arquitetura/spec.md) | Technology choices and architectural decisions |
| [Config Schema](../schema/spec.md) | Full `apiary.yaml` schema reference |
| [Plugin API](../plugin-api/spec.md) | Source and Runner adapter interfaces |
| [CLI](../cli/spec.md) | Command-line interface design |
| [Roadmap](../roadmap/spec.md) | Milestone plan v0.1 → v1.0 |
