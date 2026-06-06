# Specification: Workflow Memory

A shared, enrichable document that travels through a workflow instance. Each step reads the current state of memory and optionally writes structured fields or a summary back to it. Only what is explicitly written to memory is forwarded — full raw output is stored in SQLite for audit and resume, but never injected into downstream agents.

## Mental Model

Memory is a baton passed through a relay race. Each runner reads the current state, does their work, and hands the baton forward — enriched with only what the next runner needs to know. The full history of each leg is recorded in the database, not on the baton.

## Structure

Memory always contains two sections:

```
=== Workflow Memory ===

[Cell]
title:    Fix user auth bug #142
labels:   backend, bug
priority: high
source:   main-plane

[Step Data]
complexity: high
approach: Refactor auth middleware to use JWT instead of sessions
files_to_touch:
  - src/auth/middleware.go
  - src/auth/handler.go

[Summaries]
plan: |
  - Analyzed the auth flow and identified session storage as the root issue
  - JWT is the right replacement: stateless, already used in other services
  - Two files need changes; no migration required
  - No blockers

======================
```

- **Cell section** — always present; populated from the original task at workflow start.
- **Step Data section** — structured fields written by steps via `memory.write`.
- **Summaries section** — plain-text summaries written by steps via `summary_prompt`.

## Schema

### On any `agent` step

```yaml
steps:
  - id: plan
    agent: architect
    output_schema:
      type: object
      properties:
        complexity:     {type: string, enum: [low, medium, high]}
        approach:       {type: string}
        files_to_touch: {type: array, items: {type: string}}
      required: [complexity, approach]
    summary_prompt: |
      In 3-5 bullet points: what you concluded, what the next agent needs to know, any risks.
    memory:
      read: true          # inject current memory into this step's context (default: true)
      write: [complexity, approach, files_to_touch]   # fields from output_schema to persist
      # "summary" is implicitly written when summary_prompt is set — no need to declare it

  - id: implement
    agent: backend-dev
    summary_prompt: |
      In 3-5 bullet points: what you changed, which files, any remaining work or blockers.
    memory:
      read: true
      write: []           # no structured fields to persist; summary is still written
```

### `memory` field reference

| Field | Type | Default | Description |
|---|---|---|---|
| `read` | bool | `true` | Inject the current memory document into this step's system prompt |
| `write` | string[] | `[]` | Field names from `output_schema` to persist into memory. Each becomes a top-level key. |

`summary_prompt` is a sibling of `memory`, not nested inside it. When set, the agent is instructed to produce a brief summary as the second-to-last output block (before the optional `APIARY_OUTPUT:` line). The engine extracts and stores it under `<step-id>` in the Summaries section.

## Key Naming

- Structured fields from `memory.write` are stored as flat top-level keys. Example: `write: [complexity]` adds `complexity: high` to Step Data.
- If two steps write to the same key, last-write-wins. A warning is emitted at runtime.
- Summaries are always namespaced by step ID (`plan:`, `implement:`) — no collisions.

## Memory Read: What the Agent Sees

When `memory.read: true` (default), the formatted memory document is prepended to the agent's system prompt before the soul file content and the step-level `prompt` override:

```
=== Workflow Memory ===
... (current memory state) ...
======================

[soul file content]

[step prompt override, if any]
```

When `memory.read: false`, no memory is injected. Use this for steps that must make an independent judgment uninfluenced by prior context (e.g., an adversarial review step).

## Memory Without output_schema

Steps that do not declare `output_schema` cannot use `memory.write` for structured fields. They can still contribute a summary via `summary_prompt`. If neither is set, the step contributes nothing to memory — the document passes through unchanged.

## Memory Size

Memory stays small by design — each step only writes what it declares. There is no automatic accumulation of raw output. The full text of each step's output is stored in the `step_runs` SQLite table and accessible via `apiary instances <id>` for debugging, but it never flows into downstream prompts.

A `memory_max_chars` setting (default: `4000`) truncates the injected memory block if the total document exceeds the limit. Truncation removes the oldest summaries first, then truncates long string values. The Cell section and Step Data keys are never truncated.

## Interaction with Structured Output

`memory.write` only works when the step also declares `output_schema`. The fields listed in `write` must exist as top-level properties in the schema. The engine validates this at config load time.

If `output_schema` is declared but `memory.write` is empty, structured output is still validated and stored — it just is not forwarded to downstream steps.

## Interaction with `type: split` Conditions

Memory fields are accessible in split conditions using the same dot-path syntax as step outputs:

```yaml
- id: route
  type: split
  branches:
    - if: "memory.complexity == 'high'"
      goto: senior-dev
    - else: true
      goto: standard-dev
```

`memory.<key>` reads from the current Step Data section of memory at the time the split is evaluated.

## Summary: What Goes Where

| Data | Stored in | Forwarded to next step |
|---|---|---|
| Full agent stdout | SQLite `step_runs.output` | Never (only for audit/resume) |
| Structured output fields | SQLite + memory (if `write` declared) | Only declared fields |
| Summary | SQLite + memory (if `summary_prompt` set) | Always (under step ID key) |
| Cell metadata | Memory (always) | Always |
