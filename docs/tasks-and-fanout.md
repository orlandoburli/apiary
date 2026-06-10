# Tasks & Fan-out

Agents are not limited to answering the task they were given. From inside a
run, an agent can **write results back** to the tracker item and **spawn
child tasks** that drive their own workflows — all through plain text markers
in its output, no API access required. This page covers those markers and the
task model behind them.

## The internal task model

The canonical unit of work in Apiary is the **task**. A task usually starts
life as a source item (a GitHub issue, a Plane work item) — that link is
called a **binding** — but tasks can also be created internally by other
tasks, forming a lineage tree:

```
Task: "Implement billing module" (bound to issue #42)
 ├─ Task: "Add invoices table"        (spawned)
 ├─ Task: "Create invoices API"       (spawned)
 └─ Task: "Wire dashboard UI"         (spawned)
```

Each task fans out to one or more workflow instances (every matching trigger,
or the workflow named by its spawner). The task **settles** when its last
instance reaches a terminal state. The dashboard's Tasks tab and
`apiary task <id>` show the full tree: bindings, lineage, instances, steps,
and logs.

## Output markers

Apiary scans agent output for these markers:

| Marker | Purpose |
|---|---|
| `APIARY_OUTPUT: {…}` | [Structured output](workflows.md#agent-steps) validated against the step's schema |
| `APIARY_PUBLISH_BEGIN … END` | Publish human-facing text back to the source item |
| `APIARY_SPAWN_BEGIN … END` | Create a child task and run a named workflow on it |
| `APIARY_MEMORIZE_BEGIN … END` | Save a task note or durable fact to [agent memory](memory.md) |

Because they are plain text, they work identically across every runner — the
agent does not need credentials or tracker-specific tooling.

## `APIARY_PUBLISH` — write-back

Anything the agent wraps in a publish block is written back to the task's
source bindings (e.g. posted as an issue comment):

```
APIARY_PUBLISH_BEGIN
Triage summary: the spike in 5xx responses traces to the new cache layer.
Mitigation is in progress; a follow-up task collects diagnostics.
APIARY_PUBLISH_END
```

The step-level `publish` field controls this: `auto` (default — write back
when the agent emits a payload) or `off` (never write back, even if a payload
is present).

This is the precise, agent-authored alternative to the global
`settings.result_comment` flag, which posts the entire final output.

## `APIARY_SPAWN` — child tasks

An agent spawns a child task by emitting a JSON payload naming a workflow:

```
APIARY_SPAWN_BEGIN
{"workflow": "collect-logs", "title": "Collect logs for incident", "input": {"severity": "high"}, "key": "incident/collect-logs"}
APIARY_SPAWN_END
```

| Field | Description |
|---|---|
| `workflow` | Workflow ID to run on the child — started **by name**, not by trigger routing |
| `title` | Child task title |
| `input` | Arbitrary JSON handed to the child's steps as task input |
| `key` | Optional idempotency key — see [Spawn deduplication](#spawn-deduplication) |

The parent is always the spawning task — lineage cannot be forged from agent
output.

The step-level `spawn` field controls how the engine treats the request:

- **`spawn: auto`** (default) — fire-and-forget: the child is created and
  dispatched; the parent step finishes without waiting.
- **`spawn: await`** — the step blocks until the spawned task reaches a
  terminal state, and the child's failure fails the step. Use this when the
  parent's later steps depend on the child's work.

### Named workflows (without triggers)

The spawned workflow typically has **no `trigger`** — it can only be started
by name, never by polling:

```yaml
workflows:
  - id: collect-logs
    description: "Collect diagnostic logs (spawned via APIARY_SPAWN)."
    steps:
      - id: collect
        agent: investigator
        prompt: "Gather the relevant logs and metrics for the incident described in the task input."
```

### Spawn deduplication

Spawns are idempotent per parent task. When `key` is set, re-running the
spawning step (or the whole workflow) with the same key resolves to the child
that already exists instead of creating a duplicate. When `key` is omitted,
a key is derived from `(workflow, title, input)` — so byte-identical spawns
still dedup.

Supply an explicit key whenever the title or input may vary between runs —
e.g. a spec that decomposes into a fixed set of sub-tasks should use stable
keys like `"<spec>/<sub-task>"`, so a re-decomposition can never produce two
of the same child.

Pair the spawning workflow's trigger with
[`once: true`](workflows.md#fan-out-exclusive-and-once) so the *workflow
itself* also runs at most once per task — otherwise every poll re-dispatches
the decomposition while the source item still matches the trigger.

## Task-level hooks

Per-workflow `on_complete`/`on_fail` fire once per **instance**. The
top-level `tasks:` block fires once per **task**, when the last of its
fanned-out instances reaches a terminal state:

```yaml
tasks:
  on_complete:        # all instances succeeded
    add_labels: [ai-complete]
  on_fail:            # at least one instance failed
    set_state: blocked
    add_labels: [ai-failed]
```

The hook applies to every source binding on the task. This is the right
place for "the task as a whole is done/broken" signals when several
workflows process the same item.

## Putting it together

A triage workflow that publishes a summary, spawns a follow-up, and waits
for it:

```yaml
workflows:
  - id: incident-triage
    trigger:
      priority: 5
      exclusive: true     # claim incidents alone — no fan-out to other workflows
      once: true          # never re-triage the same incident
      match:
        source: my-repo
        labels: [incident]
    steps:
      - id: triage
        agent: investigator
        prompt: |
          Assess the incident. Write a short summary back to the issue
          (APIARY_PUBLISH block) and spawn a `collect-logs` follow-up task
          (APIARY_SPAWN block) with a stable key.
        publish: auto
        spawn: await      # the triage isn't done until diagnostics exist
    on_complete:
      set_state: in review
```
