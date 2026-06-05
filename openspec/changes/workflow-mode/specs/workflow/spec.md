# Specification: Workflow Schema

Complete schema reference for the `workflows:` block in `apiary.yaml`. This is the canonical definition of how workflows, triggers, and steps fit together.

## Structure Overview

```
Workflow
 ├── trigger          — which tasks start this workflow
 └── steps[]          — ordered list of steps (the DAG)
      ├── type: agent     — invokes one agent
      ├── type: split     — conditional routing node
      ├── type: approval      — human approval checkpoint
      ├── type: foreach   — dynamic fan-out over a list
      └── type: workflow  — delegates to a child workflow
```

Steps are children of a workflow. The engine builds a DAG from their `depends_on` relationships and executes them in topological order. Steps without `depends_on` run immediately when the workflow starts. Multiple steps with no shared dependency run in parallel.

---

## Full Annotated Example

```yaml
workflows:
  - id: feature-development                     # unique across all workflows and routes
    description: "Full feature implementation"  # shown in TUI and logs
    resume: allowed                             # allowed | forbidden | auto
    trigger:
      priority: 10                              # lower = evaluated first (same as routes)
      match:                                    # all fields must match (AND logic)
        source: main-plane                      # restrict to this source ID (optional)
        labels: [feature, ai-ready]            # task must have ALL these labels
        types: [feature, improvement]           # task type must be one of these
        title_regex: ".*"                       # task title regex (optional)
        priority: [urgent, high]               # task priority must be one of these

    steps:
      # ── Agent step ──────────────────────────────────────────────────────────
      - id: plan
        type: agent                             # default; can be omitted
        agent: architect                        # agent ID defined in agents:
        model: claude-opus-4-8                  # optional: override agent's preferred_models
        prompt: |                               # optional: extra prompt appended to agent's soul
          Produce a structured implementation plan.
        summary_prompt: |                       # optional: agent produces a brief handoff note
          In 3-5 bullets: conclusions, risks, what the next agent needs to know.
        idempotent: false                       # hint for resume warnings (default: false)
        output_schema:                          # optional: expect structured JSON on last line
          type: object
          properties:
            complexity: {type: string, enum: [low, medium, high]}
            approach:   {type: string}
          required: [complexity, approach]
        on_missing_output: warn                 # warn | fail | ignore (default: warn)
        memory:
          read: true                            # inject memory doc into prompt (default: true)
          write: [complexity, approach]         # output_schema fields to persist in memory
        on_pass:
          next: implement                       # optional: explicit next step on success
        on_fail:
          goto: plan                            # optional: loop-back step ID
          max_retries: 2                        # required when goto is set

      # ── Agent step (depends on plan) ────────────────────────────────────────
      - id: implement
        agent: backend-dev
        depends_on: [plan]                      # list of step IDs that must pass first
        summary_prompt: |
          In 3-5 bullets: what you changed, files modified, any blockers.
        memory:
          read: true    # sees: cell + complexity + approach + plan summary
          write: []     # no structured fields to add; summary is still written

      # ── Split step ──────────────────────────────────────────────────────────
      - id: route-by-complexity
        type: split
        depends_on: [plan]
        multi: false                            # false = first match wins (default)
                                                # true  = all matching branches activate
        branches:
          - if: "memory.complexity == 'high'"   # memory fields accessible in conditions
            goto: senior-review
          - if: "memory.complexity == 'medium'"
            goto: standard-review
          - else: true                          # required when multi: false
            goto: fast-review

      # ── Approval step ────────────────────────────────────────────────────────
      - id: human-approval
        type: approval
        depends_on: [plan]
        message: |
          Plan is ready. Reply "approve" to continue or "reject" to abort.
        resume_on:
          comment_contains: "approve"           # resumes the workflow
          label_added: "approved"               # alternative trigger
          state_changed: "in_review"            # alternative trigger
        abort_on:
          comment_contains: "reject"            # marks instance as failed
        timeout: 48h                            # optional: abort if no response (default: none)

      # ── Foreach step ────────────────────────────────────────────────────────
      - id: fix-issues
        type: foreach
        depends_on: [implement]
        items: "steps.implement.output.issues"  # dot-path to array in structured output
        as: issue                               # variable name inside the inner step
        concurrency: 4                          # max parallel sub-runs (default: 2)
        max_items: 20                           # hard cap; fail if exceeded (default: 50)
        fail_fast: false                        # stop on first sub-run failure (default: false)
        step:                                   # the agent step run once per item
          agent: backend-dev
          model: claude-haiku-4-5               # optional per-item model override
          prompt: |
            Fix issue in {{ issue.file }}:
            {{ issue.description }}
          output_schema:
            type: object
            properties:
              fixed:   {type: boolean}
              summary: {type: string}
            required: [fixed]

      # ── Sub-workflow step ────────────────────────────────────────────────────
      - id: review-phase
        type: workflow
        workflow: standard-review               # ID of another workflow defined in this file
        depends_on: [fix-issues]
        # child workflow receives the current memory snapshot at this point

    on_complete:                                # side-effects when all steps pass
      set_state: in_review
      add_labels: [implemented]
    on_fail:                                    # side-effects when any step fails
      set_state: blocked
      add_labels: [workflow-failed]
```

---

## `workflows[]` — Top-Level Fields

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | ✓ | Unique identifier. Must not conflict with any route `id`. |
| `description` | string | — | Human-readable label shown in TUI and logs. |
| `resume` | enum | — | `allowed` (default) \| `forbidden` \| `auto`. See [resume spec](../resume/spec.md). |
| `trigger` | object | — | Omit to create a sub-workflow-only workflow (no Router matching). |
| `steps` | StepConfig[] | ✓ | At least one step required. |
| `on_complete` | HookConfig | — | Applied when the instance reaches `done`. |
| `on_fail` | HookConfig | — | Applied when the instance reaches `failed`. |

---

## `trigger` Fields

Identical to route `match` plus `priority`. The Router evaluates workflow triggers in `priority` order; first match wins.

| Field | Type | Required | Description |
|---|---|---|---|
| `priority` | int | ✓ | Evaluation order — lower = first. |
| `match.source` | string | — | Restrict to a specific source ID. |
| `match.labels` | string[] | — | Task must have ALL listed labels. |
| `match.types` | string[] | — | Task type must be one of these values. |
| `match.title_regex` | string | — | Task title must match this regex. |
| `match.priority` | string[] | — | Task priority must be one of these values. |

---

## `steps[]` — Common Fields (all step types)

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | ✓ | Unique within the workflow. Used in `depends_on` and expressions. |
| `type` | enum | — | `agent` (default) \| `split` \| `approval` \| `foreach` \| `workflow` |
| `depends_on` | string[] | — | Step IDs that must be in state `passed` before this step runs. Omit to run at workflow start. |

---

## Step Type: `agent`

| Field | Type | Required | Description |
|---|---|---|---|
| `agent` | string | ✓ | Agent ID from `agents:`. |
| `model` | string | — | Model override for this step. Opaque string passed to runner. See [per-step-model spec](../per-step-model/spec.md). |
| `prompt` | string | — | Extra text appended to the agent's soul file content and memory. |
| `summary_prompt` | string | — | Instruction for the agent to produce a brief handoff note. Stored in memory under `<step-id>`. |
| `idempotent` | bool | — | Resume hint. `false` by default. |
| `output_schema` | JSON Schema | — | Expected structured output schema. See [structured-output spec](../structured-output/spec.md). |
| `on_missing_output` | enum | — | `warn` (default) \| `fail` \| `ignore`. Applies when `output_schema` is set but no `APIARY_OUTPUT:` line found. |
| `memory.read` | bool | — | Inject current memory into this step's prompt. Default: `true`. Set `false` for independent judgment steps. |
| `memory.write` | string[] | — | Fields from `output_schema` to persist in memory. Requires `output_schema`. |
| `on_pass.next` | string | — | Explicit next step ID on success. Normally inferred from the DAG; use only to override. |
| `on_fail.goto` | string | — | Step ID to loop back to on failure. Must be an ancestor step. |
| `on_fail.max_retries` | int | — | Required when `on_fail.goto` is set. Max times this loop may repeat before the instance fails. |

---

## Step Type: `split`

| Field | Type | Required | Description |
|---|---|---|---|
| `multi` | bool | — | `false` (default): first matching branch wins. `true`: all matching branches activate in parallel. |
| `branches` | Branch[] | ✓ | Ordered list of branches. |
| `branches[].if` | string | — | Condition expression. Omit (with `else: true`) for the fallback branch. |
| `branches[].else` | bool | — | Marks the fallback branch. Exactly one required when `multi: false`. |
| `branches[].goto` | string | ✓ | Step ID to activate when this branch matches. |

**Expression language** available in `if`: `cell.*`, `memory.*`, and `steps.<id>.state` / `steps.<id>.exit_code`. See [proposal](../../proposal.md#expression-language).

---

## Step Type: `approval`

| Field | Type | Required | Description |
|---|---|---|---|
| `message` | string | ✓ | Posted as a comment on the source task when the approval step is reached. |
| `resume_on` | ApprovalTrigger | ✓ | Condition that resumes the workflow. |
| `abort_on` | ApprovalTrigger | — | Condition that fails the workflow. |
| `timeout` | duration | — | If set, aborts the workflow after this duration with no human response. |

**ApprovalTrigger fields** (at least one required per trigger):

| Field | Type | Description |
|---|---|---|
| `comment_contains` | string | A comment on the task containing this string. |
| `label_added` | string | This label is added to the task. |
| `state_changed` | string | The task moves to this state. |

---

## Step Type: `foreach`

See [foreach-step spec](../foreach-step/spec.md) for full details.

| Field | Type | Required | Description |
|---|---|---|---|
| `items` | string | ✓ | Dot-path to an array in a prior step's structured output. |
| `as` | string | — | Variable name for the current item in prompt templates. Default: `item`. |
| `concurrency` | int | — | Max parallel sub-runs. Default: `2`. Range: 1–16. |
| `max_items` | int | — | Hard cap on array length. Default: `50`. Range: 1–200. |
| `fail_fast` | bool | — | Fail immediately when any sub-run fails. Default: `false`. |
| `step` | StepConfig | ✓ | The `agent` step definition to run per item. Must be `type: agent`. |

---

## Step Type: `workflow`

See [sub-workflows spec](../sub-workflows/spec.md) for full details.

| Field | Type | Required | Description |
|---|---|---|---|
| `workflow` | string | ✓ | ID of another workflow defined in `apiary.yaml`. |
| _(no extra fields)_ | — | — | Child receives the current memory snapshot automatically. |

---

## `on_complete` / `on_fail` — Hook Fields

| Field | Type | Description |
|---|---|---|
| `set_state` | string | Move the task to this state in the source system. |
| `add_labels` | string[] | Add these labels to the task. |

---

## Step Execution Order

The engine computes a topological sort of steps by `depends_on`. Steps at the same depth (no dependency between them) run in parallel. Explicit `on_pass.next` overrides this only for the declaring step — it does not affect other steps that `depends_on` the same node.

```
Example DAG:

  plan ──► route-by-complexity ──► senior-review ─┐
                               └──► fast-review   ─┤
                                                   ▼
  implement ─────────────────────────────────► finalize
```

`finalize` runs when all its `depends_on` entries have passed.

---

## Related Specs

| Spec | Description |
|---|---|
| [workflow-memory](../workflow-memory/spec.md) | Memory object, `memory.write`, `summary_prompt` |
| [structured-output](../structured-output/spec.md) | `output_schema` and `APIARY_OUTPUT:` contract |
| [foreach-step](../foreach-step/spec.md) | Dynamic fan-out |
| [resume](../resume/spec.md) | Resuming failed instances |
| [per-step-model](../per-step-model/spec.md) | Step-level model override |
| [sub-workflows](../sub-workflows/spec.md) | `type: workflow` child invocation |
