# Specification: Concurrency Model

Define how `settings.concurrency` applies across workflow instances, parallel steps, and foreach sub-runs.

## Problem

The current model is simple: `settings.concurrency` limits how many worker runs execute simultaneously. Workflow mode introduces three levels of concurrency:

1. **Workflow instances** — multiple tasks being worked on at the same time
2. **Parallel steps** — multiple steps within one instance running at the same time
3. **Foreach sub-runs** — multiple items within one foreach step running at the same time

Without a clear model, these levels interact unpredictably. A `foreach` with `concurrency: 8` inside a 3-instance-parallel hive could spawn 24 simultaneous agent invocations against a single working directory.

## Decision: One Global Agent Invocation Semaphore

`settings.concurrency` is a single global cap on the total number of **simultaneous agent invocations** across the entire hive — regardless of which instance, step, or foreach they belong to.

```yaml
settings:
  concurrency: 4   # at most 4 agents running at any moment, across all instances and steps
```

Every `RunnerAdapter.Run(...)` call acquires one slot from this semaphore before executing and releases it on completion. Callers that cannot acquire a slot block until one becomes available.

This is the simplest model: one knob, one guarantee, predictable resource usage.

## What This Means in Practice

| Scenario | concurrency: 4 | behavior |
|---|---|---|
| 4 independent tasks arrive simultaneously | 4 instances start, each runs step 1 | all 4 slots used |
| 5th task arrives | 5th instance queued | waits for a slot |
| One instance has 2 parallel steps ready | both try to acquire slots | each needs 1 slot; runs if available |
| foreach with `concurrency: 8`, global limit is 4 | foreach respects the lower of its own limit and available global slots | at most 4 sub-runs at once |

## Interaction with `foreach.concurrency`

`foreach.concurrency` is a **per-step upper bound**, not a reservation. The effective concurrency for a foreach step is:

```
effective = min(foreach.concurrency, available_global_slots)
```

The foreach step spawns sub-runs up to `effective` at a time, blocking when all global slots are occupied.

## Workflow Instance Queuing

When a new Cell is matched to a workflow and all global slots are occupied, the instance is created in SQLite with state `pending` and waits. The engine re-checks pending instances on each poll cycle and starts them as slots free up. Pending instances are dispatched oldest-first (by `created_at`).

This replaces the previous dispatcher queue behavior. The semantics are the same; the mechanism moves to the WorkflowEngine.

## Step-Level Queuing Within an Instance

When a step is ready (all `depends_on` passed) but no global slot is available, the step waits in memory. The engine uses a per-instance ready queue. Steps in the queue are dispatched as slots free up, in topological order (steps with no dependencies between them dispatch in declaration order as a tiebreaker).

## Starvation Prevention

A pathological case: one instance with a `foreach` of 200 items (capped to `max_items: 200`) monopolizes all slots indefinitely, starving other instances.

Mitigation: the engine interleaves slot acquisition across instances using a round-robin dispatch strategy. When multiple instances have ready steps competing for the same slot, the engine picks from each instance in turn rather than draining one instance completely.

This is a best-effort fairness guarantee — it does not guarantee equal throughput per instance, only that no instance is permanently starved.

## Recommended Settings

| Use case | `concurrency` suggestion |
|---|---|
| Single developer machine, one project | `2` (default) |
| CI environment, dedicated runner | `4–8` |
| Multi-project hive | `8–16` |
| Foreach-heavy workflows | Keep `concurrency` ≥ `foreach.concurrency` to avoid artificial throttling |

## No Per-Workflow Concurrency Limit (v1)

There is no per-workflow or per-instance concurrency limit in v1. All instances share the global semaphore equally. A future `settings.max_instances` field could cap the number of simultaneously active instances independently of the per-invocation limit — deferred to v2.
