# Specification: Workflow Resume

Allow a failed or interrupted workflow instance to restart from the last successfully completed step, skipping already-completed work.

## Problem

A 5-step workflow that fails at step 4 currently has no recovery path — the operator must wait for the task to re-enter the queue (which may never happen) or manually re-trigger it. All prior agent work is discarded. For long-running workflows with expensive agent steps, this is unacceptable.

## Approach

Resume is an explicit operator action, not automatic. The engine never auto-retries a failed instance — a human (or a future automation layer) decides to resume. This avoids uncontrolled loops when a workflow fails due to a systematic problem (bad config, broken agent, wrong task).

### How Resume Works

1. A workflow instance is in state `failed` or `interrupted` (process killed mid-run).
2. The operator runs `apiary resume <instance-id>` (or uses the TUI).
3. The engine loads the instance from SQLite, finds all steps in state `passed`, and marks them as `skipped_cached`.
4. Steps in state `running` (interrupted) are reset to `pending`.
5. Execution continues from the first non-passed step.

Passed steps are not re-run. Their stored outputs are replayed into the step context for downstream steps exactly as if they had just completed.

## Idempotency Caveats

Steps are **not** guaranteed idempotent. Resume is safe only for steps whose side effects have not yet occurred or are safe to repeat. Apiary surfaces a warning at resume time listing which steps will be skipped and what side effects they may have caused:

```
Warning: resuming instance wf_abc123 from step "review".
The following steps will be skipped (already completed):
  ✓ plan       — no on_complete hooks
  ✓ implement  — on_complete: set_state=in_progress (already applied)
Steps to run: review, finalize
Proceed? [y/N]
```

The operator confirms before the engine proceeds.

## Schema: Step-Level Resume Hints

Steps can declare `idempotent: true` to suppress the warning for that step:

```yaml
steps:
  - id: generate-plan
    agent: architect
    idempotent: true   # re-running produces the same output; no external side effects

  - id: implement
    agent: backend-dev
    idempotent: false  # default; re-running would create duplicate commits
```

`idempotent: true` is a hint to the operator — the engine does not enforce it. It affects only the resume warning display.

## Schema: Workflow-Level Resume Policy

```yaml
workflows:
  - id: feature-development
    resume: allowed       # default; operator can resume via CLI
    # resume: forbidden   # instance cannot be resumed; must re-trigger from scratch
    # resume: auto        # engine automatically resumes on next poll if instance is failed
    steps: [...]
```

`resume: auto` is the closest to Claude workflows' behavior. It should be used only for workflows with fully idempotent steps (e.g., read-only analysis workflows).

## CLI

```
apiary resume <instance-id>          # resume a specific instance
apiary resume --workflow <id>        # resume the latest failed instance of a workflow
apiary instances                     # list all instances with state and resume eligibility
```

## SQLite Changes

The `step_runs` table gains a `skipped_cached` boolean. When a step is skipped on resume, its prior row is marked `skipped_cached = true` and its `output` is reused by the context builder.

The `workflow_instances` table gains a `resumed_from` column (nullable instance ID) to track resume lineage.

## Constraints

1. Only instances in state `failed` or `interrupted` can be resumed.
2. `done` instances cannot be resumed.
3. If the workflow definition has changed since the instance was created (step IDs added/removed), resume is blocked with an error. The operator must re-trigger from scratch.
4. `resume: auto` is only valid if all steps in the workflow are marked `idempotent: true`. Config validation enforces this.
