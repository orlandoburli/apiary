# Specification: State Lock and Result Comment in Workflow Context

Define when `state_lock` and `result_comment` fire in multi-step workflows.

## Current Behavior (Plain Routes)

| Setting | When it fires |
|---|---|
| `state_lock: true` | Before the single agent run — moves task to "in_progress" |
| `result_comment: true` | After the single agent run — posts agent output as a comment |

Both are global settings in `apiary.yaml` under `settings:`.

## Problem

In a workflow, "before the run" and "after the run" are ambiguous:
- A 5-step workflow with `result_comment: true` would post 5 comments — one per step — flooding the task.
- `state_lock` should lock the task for the duration of the whole workflow, not per step.
- Different workflows may want different comment behavior.

## Decision

### `state_lock`

Fires **once at workflow instance start**, not per step. The task moves to "in_progress" before the first step executes and remains there for the entire workflow duration. On instance completion (`done` or `failed`), the `on_complete`/`on_fail` hooks control the final state via `set_state`.

This is unchanged from the current behavior for plain routes — the difference is only that it no longer fires per step in multi-step workflows.

### `result_comment`

`result_comment` becomes a per-workflow setting with three modes, configurable in the workflow definition:

```yaml
workflows:
  - id: feature-development
    result_comment: on_complete   # default
    steps: [...]
```

| Mode | Behavior |
|---|---|
| `on_complete` | Posts one comment when the workflow instance finishes (done or failed). Content: the final workflow memory document. This is the **default**. |
| `per_step` | Posts one comment after each agent step completes. Content: the step's raw output. Useful for long workflows where visibility matters. |
| `off` | No comments posted. |

The global `settings.result_comment: true/false` remains as a hive-wide default:
- `true` (default) → workflows without an explicit `result_comment` field use `on_complete`
- `false` → workflows without an explicit `result_comment` field use `off`

A workflow-level `result_comment` always overrides the global setting.

### Comment Content

**`on_complete` mode** — posts the final workflow memory document plus a status line:

```
**Workflow: feature-development — ✓ Done** (5 steps, 14m 32s)

=== Workflow Memory ===

[Cell]
title: Implement user auth
labels: feature, backend

[Step Data]
complexity: high
approach: JWT-based auth middleware

[Summaries]
plan: |
  - JWT chosen over sessions: stateless, fits existing infra
  - Two files to change
implement: |
  - Added JwtMiddleware to middleware.go
  - Updated auth handler, added tests
review: |
  - LGTM — no security issues found
  - Suggested minor refactor in handler (addressed)

======================
```

**`per_step` mode** — posts the step's raw `RunResult.Output` with a header:

```
**Step: implement (backend-dev) — ✓ passed** (4m 12s)

[raw agent output]
```

### Approval Step Comments

Approval steps always post their `message` to the task regardless of `result_comment` mode. This is separate from result comments — it is the mechanism by which the approval step communicates with the human.

### Failed Workflow Comments

When a workflow instance fails, `on_complete` mode posts a failure summary:

```
**Workflow: feature-development — ✗ Failed** at step "review" (3 steps completed, 8m 11s)

Failure reason: step "review" failed after 2 retries.

=== Workflow Memory (at failure) ===
...
======================
```

## Schema

```yaml
settings:
  result_comment: true    # hive-wide default: true = on_complete, false = off
  state_lock: true        # unchanged

workflows:
  - id: feature-development
    result_comment: on_complete   # on_complete | per_step | off
                                  # overrides settings.result_comment for this workflow
    steps: [...]
```

## Backward Compatibility

Plain routes (not using `workflows:`) use the global `settings.result_comment` and `settings.state_lock` with their original single-step semantics. No behavior change for existing configurations.

Single-step workflows synthesized from plain routes inherit the global settings: `state_lock` fires before the step, `result_comment` posts the step output after — identical to the current behavior.
