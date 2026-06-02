# Proposal: Foundation — Apiary Initial Specification

## Summary

Establish the foundational specification for Apiary: a task-driven agent orchestration harness that connects project management tools (Plane, Jira, Linear, GitHub Issues) to AI agent runners (OpenCode, custom CLIs) and routes each task to the right agent profile and LLM model based on declarative rules.

## Motivation

- **Manual dispatch is a bottleneck**: teams using AI agents to work on tasks still need a human to pick the task, configure context, and trigger the agent. Apiary removes this step.
- **Model-task mismatch**: different task types benefit from different models. A bug fix needs a reasoning-capable model; a docs update does not. There is no tooling today that routes tasks to models automatically.
- **Lock-in risk**: most agent harnesses are tightly coupled to a single AI provider. Apiary is runner- and model-agnostic by design.

## Scope

1. Overall vision and key concepts (`visao-geral/spec.md`)
2. Architecture and design decisions (`arquitetura/spec.md`)
3. `apiary.yaml` config schema (`schema/spec.md`)
4. Plugin API — `SourceAdapter` and `RunnerAdapter` Go interfaces (`plugin-api/spec.md`)
5. CLI design — commands, flags, env vars, exit codes (`cli/spec.md`)
6. Roadmap v0.1 → v1.0 (`roadmap/spec.md`)
