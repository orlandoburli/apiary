# Proposal: Internal Task Model

## Why

The current architecture treats the **`SourceItem`** — a normalized view of a source item (GitHub Issue, Plane Work-Item) — as the canonical unit of work. The SourceItem flows through routing, workflow execution, side-effects, and approval logic. Every component that touches a task ultimately holds a `SourceItem` with a `SourceID`, making the source system the implicit owner of task identity and state.

This design worked well for simple dispatch (one source item → one workflow) but creates three compounding problems as workflows grow more complex:

1. **One source item can only trigger one workflow.** The router's first-match-wins logic means a GitHub issue labelled `apiary` can dispatch at most one workflow. If you want a code-review *and* a documentation-update to run on the same issue, you need manual workarounds or nested sub-workflows — not a clean fan-out.

2. **Source systems are in the hot path.** Approval polling hits the source API every cycle. Side-effects write back on every step. The workflow engine cannot make any decision without knowing which source adapter to call. This couples execution semantics to source availability.

3. **There is no internal task.** There is no object that represents "this unit of work" independent of its origin. You cannot create a task from an API call, a webhook, a schedule, or a future source type without first manufacturing a fake SourceItem — which is already awkward.

4. **Workflows cannot chain into other workflows.** If a Triage workflow decides that a problem needs an Engineer workflow, there is no first-class way to express that. The only option is sub-workflows, but those are nested and share the same source binding — not independent units of work dispatched to their own execution context.

The fix is to promote **InternalTask** to first-class status, demote sources to **binders** that register work and receive output, and introduce two clean fan-out paths: source-mediated and internal.

---

## What Changes

| Area | Before | After |
|---|---|---|
| **Canonical work unit** | `SourceItem` (source-tied) | `InternalTask` (source-independent) |
| **Source role** | Drives everything: triggers, state, write-back | Registers tasks; receives output log |
| **Workflow trigger** | One match per SourceItem | N workflows per InternalTask (fan-out) |
| **Write-back** | Configured globally per workflow | Optional per step; agent decides whether to emit |
| **Task identity** | `source_item_id + source_id` (compound) | `task_id` (single, internal) |
| **Workflow chaining** | Sub-workflows only (nested, same binding) | Spawned tasks (independent InternalTasks with lineage) |
| **Approval polling** | Polls source for comment/label changes | Polls source only when step explicitly requires it |

---

## New Concepts

| Term | Description |
|---|---|
| **InternalTask** | The canonical, source-independent unit of work. Has its own ID, title, description, state, input payload, and metadata. May be created from a source binding or spawned by a workflow step. |
| **SourceBinding** | A link from a source item (`source_id + source_item_id`) to an InternalTask. One task may have many bindings; today, one — but the model is open. Source bindings are optional: spawned tasks may have none. |
| **Binder** | The role sources play: they register items as InternalTasks via bindings and receive the task's output. They do not own state. |
| **Task Fan-out** | The ability for one InternalTask to trigger multiple named workflows simultaneously or sequentially. |
| **Spawned Task** | An InternalTask created by a workflow step rather than a source adapter. It carries a `parent_task_id` linking it to the originating task, and a structured `input` payload passed by the spawning step. It has no source binding at creation time. |
| **Task Lineage** | The `parent_task_id` chain linking spawned tasks back to their origin. Enables tracing from a targeted fix task all the way back to the original incident or triage item. |
| **`APIARY_SPAWN` marker** | An agent-emitted marker (analogous to `APIARY_PUBLISH`) that instructs the engine to create a new InternalTask and dispatch a named workflow against it. Used for pure internal chaining when no source item exists at the top. |
| **Source-mediated fan-out** | A workflow agent creates new source items (GitHub issues, Jira tasks/stories) via tool calls. Those items are picked up naturally by the Poll → Bind → Route cycle and dispatched to their targeted workflows without a triage step. |
| **Step Publish** | An optional, agent-controlled action within a step that writes a message back to all source bindings of the task. The agent emits a marker to opt in; silence means no write-back. |
| **Task Completion Hook** | An `on_complete` / `on_fail` hook at the InternalTask level (not per-workflow), applied after all bound workflows reach terminal state. |

---

## What Stays

- **Sources / Adapters** — unchanged interface; they still `Poll`, `Acknowledge`, and `WriteResult`. They just do it on instruction from the task layer, not inline with dispatch.
- **Router** — matching logic is unchanged; it now matches against InternalTask metadata instead of SourceItem fields.
- **WorkflowEngine** — DAG execution, split, foreach, approvals, resume — all unchanged. It now operates on InternalTask + SourceBindings instead of SourceItem.
- **Agents and Runners** — completely unchanged.
- **Step-level config** — unchanged, plus optional `publish` and `spawn` blocks described below.

---

## Core Behaviors

### 1. Source as Binder

When a source adapter polls and returns a SourceItem, the **SourceBinder** layer translates it into an InternalTask:

- If an InternalTask already exists for this `(source_id, source_item_id)` pair, retrieve it (resume/dedup logic unchanged).
- If not, create a new InternalTask from the SourceItem's fields (title, description, metadata).
- Record the SourceBinding linking the SourceItem back to the task.
- The SourceItem is then discarded — only the InternalTask travels forward into routing and execution.

Sources write back via their SourceBinding when instructed. They are never polled or called unless a step explicitly requests it (approval steps) or a write-back is queued.

### 2. Workflow Fan-out

The router evaluates an InternalTask against **all** registered workflow triggers (not first-match-wins). Every matching trigger produces a workflow dispatch. The dispatcher starts all matched workflows; they run independently against the same InternalTask.

Fan-out ordering is controlled by a new `trigger.priority` semantic:
- `priority` (existing field) still determines order for conflict-resolution when only one workflow should run.
- A new `trigger.exclusive: true` flag (default `false`) opts a workflow into first-match-wins, suppressing lower-priority matches.

### 3. Two Fan-out Paths

Once a workflow is running, it can spawn downstream work via two patterns:

**Pattern A — Source-mediated fan-out**

The agent uses tool calls to create new items in a source system (e.g., create a GitHub issue, create a Jira task). Those items are picked up on the next Poll cycle, bound as new InternalTasks, and dispatched to their targeted workflows through the normal route. No engine mechanism needed.

The original source item is handled as the agent (and user config) decides:
- GitHub: close the original issue, or leave it open with links to child issues.
- Jira: convert the original ticket to an Epic, create tasks/stories under it.

This is the right pattern when downstream work should be visible and trackable in the source system.

**Pattern B — Internal fan-out via `APIARY_SPAWN`**

The agent emits an `APIARY_SPAWN` marker when there is no source system to go through, or when the downstream task is purely internal:

```
APIARY_SPAWN_BEGIN
{
  "workflow": "collect-workflow",
  "title": "Collect context for incident #4421",
  "input": {
    "log_event": "...",
    "service": "payments",
    "severity": "critical"
  }
}
APIARY_SPAWN_END
```

When the engine detects this marker:
1. It creates a new InternalTask with `parent_task_id` pointing to the current task.
2. It populates the new task's `input` field with the provided JSON.
3. It dispatches the named workflow against the new task.
4. The spawning step continues; it does not wait for the spawned task (fire-and-forget by default).

Spawned tasks have no source binding initially. If the spawned workflow later creates source items via tool calls, those bindings are added then.

### 4. Per-Step Write-back

Write-back to sources is **agent-driven, not config-driven**. A step agent can emit an `APIARY_PUBLISH` marker in its output — containing the message to post. If no marker is emitted, no write-back occurs for that step.

```
APIARY_PUBLISH_BEGIN
Triage complete. Routing to engineer workflow — estimated complexity: medium.
APIARY_PUBLISH_END
```

When the engine detects this marker:
1. It extracts the publish payload.
2. It queues a write-back against every SourceBinding on the InternalTask.
3. Each binding's source adapter calls `WriteResult` with the payload.

If a task has no source bindings (purely internal spawned task), `APIARY_PUBLISH` is silently ignored.

### 5. Task Completion Hook

Once **all workflows** bound to an InternalTask reach a terminal state (done or failed), the task-level completion hook fires:

```yaml
tasks:
  on_complete:
    add_labels: ["done"]
    set_state: "closed"
  on_fail:
    add_labels: ["failed"]
    set_state: "open"
```

This replaces per-workflow `on_complete` for source write-back. Workflows may still have their own `on_complete` for internal state changes (e.g., setting a workflow-level label without closing the issue).

---

## New Config Schema

### Workflow trigger — fan-out enabled by default

```yaml
workflows:
  - id: triage
    trigger:
      priority: 100
      exclusive: true         # first match only — triage owns the issue
      match:
        source: github
        labels: ["apiary"]

  - id: engineer-workflow
    trigger:
      priority: 100
      exclusive: true
      match:
        labels: ["workflow:engineer"]  # set by triage agent via tool call or label
```

### Step publish marker (agent-emitted, no config)

Step config gains an optional `publish` field only to **suppress** write-back for a specific step:

```yaml
steps:
  - id: internal-analysis
    agent: analyzer
    publish: off          # never write-back even if agent emits marker
```

Default is `publish: auto` (agent decides).

### Step spawn — suppress or await

By default, spawned tasks are fire-and-forget. A step can optionally wait for a spawned task to complete before continuing:

```yaml
steps:
  - id: collect
    agent: collector
    spawn: await           # block until spawned task finishes
```

Default is `spawn: auto` (fire-and-forget).

### Task-level completion hook

```yaml
# top-level in apiary.yaml
tasks:
  on_complete:
    add_labels: ["done"]
    set_state: "closed"
  on_fail:
    add_labels: ["needs-review"]
```

---

## Concrete Use Case: ERP Project Workflows

### Triage workflow
- **Triggered by**: GitHub issue or Jira ticket with label `apiary`
- **What it does**: Analyzes the issue; decides whether to route to Engineer or Staff workflow
- **Fan-out pattern**: Source-mediated — agent adds label `workflow:engineer` or `workflow:staff` to the issue, which the router picks up on the next poll. Alternatively, agent emits `APIARY_SPAWN` directly.
- **Original issue**: Agent decides — may add a triage comment via `APIARY_PUBLISH`, leaves issue open.

### Engineer workflow
- **Triggered by**: Task with label `workflow:engineer` (set by triage)
- **What it does**: Engineer step → Code review step → QA step
- **Write-back**: Each step may post progress comments via `APIARY_PUBLISH`
- **Completion**: Closes original issue via task completion hook.

### Staff workflow
- **Triggered by**: Task with label `workflow:staff` (set by triage)
- **What it does**: Breaks work into smaller deliverables; creates child issues/tasks via tool calls
  - GitHub: closes or links original issue; creates N targeted child issues with specific labels
  - Jira: converts original ticket to Epic; creates tasks/stories under it
- **Fan-out**: Child issues/tasks are picked up on next poll — each routes directly to targeted workflow (engineer, docs, etc.) with no triage needed, because the agent already set the right labels.

### Incident workflow
- **Triggered by**: Production log event (log monitor source adapter)
- **What it does**:
  1. Spawns a Collect task via `APIARY_SPAWN` (internal, no source binding)
  2. Collect workflow gathers logs, metrics, user session data
  3. Collect step spawns Staff task via `APIARY_SPAWN` with collected data as input
  4. Staff workflow runs — at this point it switches to source-mediated fan-out, creating Jira tasks or GitHub issues that become independently tracked items

**Lineage chain**:
```
log event
  → Incident task (source: log-monitor)
      → Collect task (spawned, parent: incident)
          → Staff task (spawned, parent: collect)
              → Fix task A (source: jira, parent: staff)
              → Fix task B (source: jira, parent: staff)
              → Fix task C (source: github, parent: staff)
```

---

## Migration Notes

- Existing `on_complete` / `on_fail` blocks on workflows continue to work and are applied per-workflow (not at task completion). The new task-level hook is additive.
- `result_comment: per_step` and `result_comment: on_complete` are deprecated in favor of the agent-driven `APIARY_PUBLISH` marker. They remain functional but emit a deprecation warning.
- The `SourceItem` type is retained internally as the normalized source item within the binding layer; it is no longer the unit passed to the router or engine.
- Existing single-workflow configs require no changes — the fan-out model is backward compatible (one match = one workflow, same as before).
