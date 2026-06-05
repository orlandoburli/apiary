# Specification: Sub-Workflows

Allow a workflow step to invoke another named workflow, enabling composition and reuse of common pipelines.

## Problem

Common patterns (plan → implement → review) get duplicated across multiple workflows. There is no way to extract a shared sub-pipeline and reference it by name. As the number of workflows grows, maintenance diverges — a fix to the review logic must be applied to every workflow individually.

## Schema

```yaml
workflows:
  # Reusable sub-workflow
  - id: standard-review
    description: "Code review pipeline: review → fix if needed → finalize"
    steps:
      - id: review
        agent: code-reviewer

      - id: fix
        agent: backend-dev
        depends_on: review
        on_fail:
          goto: review
          max_retries: 2

      - id: finalize
        agent: backend-dev
        depends_on: fix

  # Main workflow referencing the sub-workflow
  - id: feature-development
    trigger:
      priority: 10
      match:
        labels: [feature, ai-ready]
    steps:
      - id: plan
        agent: architect

      - id: implement
        agent: backend-dev
        depends_on: plan

      - id: review-phase
        type: workflow
        workflow: standard-review      # sub-workflow ID
        depends_on: implement
        # context from implement is passed into the sub-workflow automatically

    on_complete:
      set_state: in_review
```

## How It Works

A `type: workflow` step is a proxy — when it becomes ready, the engine instantiates the referenced workflow as a **child instance** linked to the parent:

- The child instance receives the same Cell as the parent.
- Step context accumulated up to the `type: workflow` step is injected as the sub-workflow's initial context (same format as normal step context injection).
- The child's steps appear in the TUI nested under the parent step.
- When the child instance reaches `done`, the `type: workflow` step is marked `passed`.
- When the child instance reaches `failed`, the `type: workflow` step is marked `failed`.
- The child's final step outputs are exposed as the sub-workflow step's output in the parent context: `steps.review-phase.output`.

## Nesting Limit

Sub-workflows may not themselves contain `type: workflow` steps. One level of nesting only. This prevents arbitrarily deep recursion and keeps instance tracking manageable.

## Sub-Workflow Triggers

A sub-workflow referenced via `type: workflow` does not use its own `trigger` block — it is invoked by the parent step. The `trigger` is only used when the workflow is matched directly by the Router. A workflow may be used both ways (as a standalone triggered workflow and as a sub-workflow of another).

## Input Passing

Sub-workflows receive the parent's **workflow memory snapshot** at the point the `type: workflow` step executes. The child starts with a copy of that memory and may enrich it further. When the child completes, its final memory state is not merged back into the parent — the parent's memory continues from where it was.

This is intentional: sub-workflows are isolated pipelines. Any data the parent needs from the child must flow through the child's final step output, exposed as `steps.<child-step-id>.output` in the parent.

## SQLite

Child instances are stored in `workflow_instances` with a `parent_instance_id` column linking them to the parent. Step runs within the child are stored normally under the child instance ID.

## Validation

1. The `workflow` field must reference a workflow ID defined in the same `apiary.yaml`.
2. The referenced workflow must not contain any `type: workflow` steps (no nesting).
3. A workflow may not reference itself (direct or indirect cycle detection at config load).
4. Sub-workflows with a `trigger` block are valid — the trigger is simply ignored when the workflow is invoked as a sub-workflow.
