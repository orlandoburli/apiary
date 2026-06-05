# Specification: Orphaned Instance Recovery

Define what happens to in-flight workflow instances when the daemon restarts after a crash or deliberate stop.

## Problem

When the daemon stops (crash, SIGKILL, `systemctl stop`) while workflow instances are executing, SQLite may contain instances in `running` or `approval_waiting` state with no live goroutine behind them. Without a defined recovery policy, these instances are permanently stuck — they never complete and never show up as failed.

## Instance States on Restart

The engine checks the following states in SQLite during startup, before accepting any new tasks:

| State at shutdown | Recovery action |
|---|---|
| `running` | Transition to `interrupted`. Log a warning per instance. |
| `approval_waiting` | Leave unchanged. The approval polling loop will resume checking these on the next cycle. |
| `pending` | Leave unchanged. The engine will pick them up normally. |
| `done` / `failed` / `interrupted` | No action. |

## `interrupted` State

`interrupted` is a terminal-like state — the instance will not resume automatically unless the workflow declares `resume: auto` or the operator runs `apiary resume`.

On startup, if any instances were transitioned to `interrupted`, the daemon logs a warning at the `warn` level and exits with code `5` (non-fatal — the daemon continues running):

```
WARN  3 workflow instance(s) were interrupted and require attention.
WARN  Run `apiary instances --state interrupted` to review.
WARN  Use `apiary resume <id>` to resume, or the instances will remain interrupted.
```

## Auto-Resume on Restart

Workflows with `resume: auto` are resumed automatically during startup recovery, without operator intervention:

```yaml
workflows:
  - id: analysis-pipeline
    resume: auto        # all steps must be idempotent: true
    steps: [...]
```

The engine re-queues these instances immediately after marking their in-progress steps as `pending`. They enter the normal dispatch queue and run as slots become available.

`resume: auto` is only valid when all steps are `idempotent: true` (enforced at config load time). The risk of unintended side-effect duplication is the operator's responsibility when enabling this.

## Step-Level Recovery

When an instance is being resumed (manually or via `resume: auto`), steps in the following states are handled:

| Step state at shutdown | Recovery action |
|---|---|
| `running` | Reset to `pending`. The step will re-run. |
| `passed` | Left as `passed`. Cached output is replayed into memory. Step is not re-run. |
| `failed` | Left as `failed`. The workflow resumes from the failure point (same as a normal `on_fail` evaluation). |
| `pending` / `skipped_cached` | No change. |

## Partial Foreach Recovery

If a foreach step was `running` at shutdown, its sub-runs are inspected individually:
- Sub-runs in `passed` state: cached, not re-run.
- Sub-runs in `running` or `pending` state: reset to `pending` and re-dispatched.

This means a foreach step may re-run some sub-runs even with `resume: auto`. For this reason, foreach inner steps should be `idempotent: true` for `resume: auto` to be safe.

## Approval Waiting — No Recovery Needed

Instances in `approval_waiting` at shutdown are left unchanged. When the daemon restarts, the approval polling loop resumes checking them on the next poll cycle. No data is lost — the approval step is stateless (the condition is re-evaluated against the live source task each time).

If the approval `timeout` would have expired during the downtime, it is evaluated on the first poll cycle after restart and the instance is aborted at that point.

## Startup Sequence

```
WorkflowEngine.Start():
  1. Query instances WHERE state = 'running' → transition to 'interrupted'
  2. For each 'interrupted' instance with resume: auto → re-queue
  3. Log warning if any 'interrupted' instances remain
  4. Resume approval polling for 'approval_waiting' instances
  5. Begin accepting new tasks from Router
```

Step 1 and 2 run inside a SQLite transaction — either all instances are recovered or none are, preventing partial state.

## No Automatic Retry for `interrupted` Without `resume: auto`

Automatically re-running interrupted instances without operator knowledge could cause unintended side effects (duplicate commits, duplicate comments, duplicate state transitions). The default is therefore conservative: interrupt and warn, let the operator decide.

The `resume: auto` flag is the explicit opt-in for fully automated recovery.
