# Specification: Per-Step Model Override

Allow individual workflow steps to specify a model, overriding the agent's `preferred_models` list for that step only.

## Problem

An agent's `preferred_models` is a static list set at the agent level. In a multi-step workflow, the same agent may be appropriate for multiple steps but the optimal model varies by step — a planning step benefits from a large reasoning model, while a formatting step only needs a small, fast model. Changing the agent's `preferred_models` affects all routes and workflows that use it.

## Schema

```yaml
steps:
  - id: plan
    agent: backend-dev
    model: claude-opus-4-8          # override: use the largest model for planning

  - id: implement
    agent: backend-dev
    # no model override: uses backend-dev's first preferred_models entry

  - id: format-output
    agent: backend-dev
    model: claude-haiku-4-5         # override: small model for a cheap formatting pass
```

The `model` field on a step is optional. When present, it is passed directly to the runner adapter as the model identifier, bypassing `preferred_models` entirely. The same opaque-string contract from ADR-006 applies: Apiary does not validate or interpret the model ID.

## Interaction with Agent Definition

| `step.model` | `agent.preferred_models` | Resolved model |
|---|---|---|
| set | any | `step.model` |
| not set | non-empty | `agent.preferred_models[0]` |
| not set | empty | runner default |

## Validation

1. `step.model` is an opaque string — no format validation.
2. If both `step.model` and `agent.preferred_models` are unset, the runner decides the model (runner default). This is valid but logged as a warning at startup.

## Rationale

This keeps agent definitions stable (the agent's identity and soul file are unchanged) while giving workflow authors fine-grained control over cost and capability per step. It does not require a new agent definition for each model variant.
