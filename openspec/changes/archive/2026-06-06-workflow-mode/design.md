# Design: Workflow Mode

## Core Model

A **Workflow** is a directed acyclic graph (DAG) of Steps with a single entry point. Each Step invokes one Agent. The graph edges encode sequencing (`depends_on`) and conditional routing (`condition`, `next_on_pass`, `next_on_fail`).

Back-edges are permitted only as explicit `next_on_pass` / `next_on_fail` jump targets. They must point to an ancestor step (validated at config load), and only one level of loop-back is allowed per step to prevent unbounded cycles.

### State Machine per Workflow Instance

```
          ┌────────────────────────────────────────────────┐
          │                Workflow Instance               │
          │                                                │
  Cell    │  pending ──► running ──► (all steps done)     │
  enters  │                │                    │          │
 ────────►│                │               ─────┴──────   │
          │                ▼              ╱              ╲ │
          │           step_running ──► done           failed│
          │                │              ╲              ╱ │
          │                │               ──────────────  │
          │                ▼                               │
          │           approval_waiting                         │
          │           (approval step)                         │
          └────────────────────────────────────────────────┘
```

Each Step has its own state: `pending | running | passed | failed | skipped`.

### Step Execution Flow

```
WorkflowEngine
  1. Receive Cell from Router (as WorkflowID + Cell)
  2. Create WorkflowInstance in SQLite (state: pending)
  3. Identify ready steps (no unmet depends_on)
  4. For each ready step:
       a. Build StepContext (cell + prior step outputs)
       b. Invoke StepRunner → RunnerAdapter (same as today)
       c. Persist StepRun (output, exit code, duration)
       d. Evaluate conditions → compute next ready steps
  5. On all steps terminal: mark instance done/failed
  6. Execute on_complete / on_fail hooks
```

## Config Structs (Go)

```go
type WorkflowConfig struct {
    ID          string        `yaml:"id"`
    Description string        `yaml:"description"`
    Trigger     TriggerConfig `yaml:"trigger"`
    Steps       []StepConfig  `yaml:"steps"`
    OnComplete  *HookConfig   `yaml:"on_complete"`
    OnFail      *HookConfig   `yaml:"on_fail"`
}

type TriggerConfig struct {
    Priority int         `yaml:"priority"`
    Match    MatchConfig `yaml:"match"` // reuses existing MatchConfig
}

type StepConfig struct {
    ID          string   `yaml:"id"`
    Type        string   `yaml:"type"`        // "agent" (default) | "approval"
    Agent       string   `yaml:"agent"`        // agent ID (when type=agent)
    Prompt      string   `yaml:"prompt"`       // extra prompt override
    DependsOn   []string `yaml:"depends_on"`
    MemoryRead  bool     `yaml:"memory_read"`   // default true; false = no memory injected
    MemoryWrite []string `yaml:"memory_write"`  // output_schema field names to persist
    Condition   string   `yaml:"condition"`    // go-template expression
    NextOnPass  string   `yaml:"next_on_pass"` // step ID (back-edge only)
    NextOnFail  string   `yaml:"next_on_fail"` // step ID

    // Approval-only fields
    Message   string      `yaml:"message"`
    ResumeOn  *ApprovalTrigger `yaml:"resume_on"`
    AbortOn   *ApprovalTrigger `yaml:"abort_on"`
    Timeout   string       `yaml:"timeout"` // duration; "" = no timeout
}

type ApprovalTrigger struct {
    CommentContains string `yaml:"comment_contains"`
    LabelAdded      string `yaml:"label_added"`
    StateChanged    string `yaml:"state_changed"`
}

type SplitBranch struct {
    If   string `yaml:"if"`   // condition expression; empty = else
    Else bool   `yaml:"else"` // marks the fallback branch
    Goto string `yaml:"goto"` // step ID to activate
}

// StepConfig extended fields for split and agent steps:
//
// type: "agent" (default)
//   Agent, Prompt, DependsOn, ContextFrom, OnPass, OnFail fields apply
//
// type: "split"
//   Multi, Branches fields apply
//
// type: "approval"
//   Message, ResumeOn, AbortOn, Timeout fields apply

type StepOutcome struct {
    Goto       string `yaml:"goto"`        // step ID
    MaxRetries int    `yaml:"max_retries"` // 0 = unlimited
}

```

## SQLite Schema

```sql
CREATE TABLE workflow_instances (
    id          TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL,
    cell_id     TEXT NOT NULL,
    source_id   TEXT NOT NULL,
    state       TEXT NOT NULL,  -- pending|running|approval_waiting|done|failed
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE step_runs (
    id                  TEXT PRIMARY KEY,
    workflow_instance_id TEXT NOT NULL REFERENCES workflow_instances(id),
    step_id             TEXT NOT NULL,
    agent_id            TEXT,
    state               TEXT NOT NULL,  -- pending|running|passed|failed|skipped
    output              TEXT,
    exit_code           INTEGER,
    started_at          INTEGER,
    finished_at         INTEGER
);
```

## Context Injection: Workflow Memory

Steps do not receive raw prior outputs. Context flows through the **workflow memory object** — see [workflow-memory spec](specs/workflow-memory/spec.md) for the full reference.

The engine builds the memory document and injects it as `system_prompt_prepend` (new field in RunnerInput alongside the existing `system_prompt_append`):

```
=== Workflow Memory ===

[Cell]
title:    Fix user auth bug #142
labels:   backend, bug
priority: high

[Step Data]
complexity: high
approach: Refactor auth middleware to use JWT

[Summaries]
plan: |
  - JWT is the right replacement
  - Two files need changes: middleware.go, handler.go
  - No blockers

======================

[soul file content]
[step-level prompt override, if any]
```

`MemoryBuilder` constructs this document from:
1. The Cell (always)
2. Fields declared in `memory.write` for each completed step (from `step_runs.structured_output`)
3. Summaries extracted from `step_runs.summary` for each completed step

A `memory_max_chars` limit (default: `4000`) applies. When exceeded, summaries are truncated oldest-first. The Cell section and Step Data keys are never truncated.

## Routing

The Router is extended to return either a `WorkflowID` or a `(WorkflowID="__simple__", AgentID)` for plain routes. The WorkflowEngine synthesizes a single-step workflow for plain routes, so the execution path is identical:

```
Router.Match(cell) → RouteResult{WorkflowID, AgentID}
WorkflowEngine.Dispatch(RouteResult, cell) → WorkflowInstance
```

## Split Step Execution

A split step is evaluated synchronously by the WorkflowEngine (no runner invocation):

```
WorkflowEngine.executeStep(step: split, instance)
  for each branch in step.Branches (in order):
    if branch.Else or eval(branch.If, instance.EvalContext()) == true:
      if step.Multi:
        activate(branch.Goto)   // may activate multiple
      else:
        activate(branch.Goto)
        return                  // first-match-wins
  // if no branch matched and no else: workflow fails with "unmatched split"
```

`EvalContext` exposes:
- `cell` — the normalised Cell struct (labels, priority, type, title, source)
- `steps` — map of step ID → `{state, exit_code, output}` for completed steps

### Expression Evaluator

A small hand-written recursive-descent parser handles the expression language. No external dependency. Grammar (simplified):

```
expr     = or_expr
or_expr  = and_expr ( "or" and_expr )*
and_expr = not_expr ( "and" not_expr )*
not_expr = "not" atom | atom
atom     = "(" expr ")" | comparison
comparison = accessor op value
accessor = "cell" "." field | "steps" "." id "." field
op       = "==" | "!=" | "contains" | "matches"
value    = QUOTED_STRING | NUMBER
```

The evaluator returns `(bool, error)`. A parse or evaluation error causes the split step to fail, which marks the workflow instance as failed.

### `on_fail` / `on_pass` Loop Tracking

Each `WorkflowInstance` holds a `retry_counts` map (`step_id → int`). When `on_fail.goto` triggers a loop-back:
1. Increment `retry_counts[step_id]`
2. If `retry_counts[step_id] > max_retries` (and `max_retries > 0`): fail the instance
3. Reset the target step's state to `pending` and re-enqueue it

Steps that are downstream of the reset step are also reset to `pending` (cascade reset), so the loop reruns the full branch.

## Validation Rules

1. All `depends_on` references must point to a step ID within the same workflow.
2. All `branches[].goto` and `on_fail.goto` / `on_pass.next` references must point to a step ID within the same workflow.
2. The step graph must be a DAG (no cycles except explicitly declared back-edges via `next_on_pass`/`next_on_fail`).
3. Back-edge targets must be ancestors of the declaring step.
4. All `agent` references in steps must point to a defined agent ID.
5. Fields in `memory_write` must exist as top-level properties in the step's `output_schema`. A step without `output_schema` cannot declare `memory_write`.
6. Each workflow `id` must be unique across all workflows and all route `id` values.
7. An approval step must not have an `agent` field, and a non-approval step must have an `agent` field.
8. A split step must not have an `agent` field and must have at least one branch.
9. A split step with `multi: false` (default) must have exactly one `else` branch (the fallback). A split with `multi: true` may omit the `else`.
10. `on_fail.goto` back-edges must target a step that is topologically before the declaring step (ancestor in the DAG). `max_retries` must be ≥ 1 when set.
11. Conditions in `branches[].if` must parse successfully under the expression grammar at config load time.

## TUI Changes

The **Task Detail** panel gains a step-progress view when the run belongs to a multi-step workflow:

```
╔═ Task: Implement user auth #142 ════════════════════╗
║ Workflow: feature-development    State: running      ║
║                                                      ║
║  Steps                                               ║
║  ✓ plan         architect       2m 14s               ║
║  ● implement    backend-dev     running (4m 02s)      ║
║  ○ review       code-reviewer   waiting               ║
║  ○ fix          backend-dev     waiting               ║
╚══════════════════════════════════════════════════════╝
```

The existing **Runs** tab becomes a **Workflow Instances** tab showing instance-level state. Step-level runs are nested under each instance.

## Migration Plan

1. **Phase 1 — Schema only**: add `workflows:` parsing alongside existing `routes:`. No execution change. Warn if a workflow is defined but not yet executed.
2. **Phase 2 — Engine**: implement `WorkflowEngine` behind a feature flag (`settings.experimental.workflow_mode: true`). Single-step workflows only.
3. **Phase 3 — Full DAG**: multi-step, parallel steps, conditions. Enable by default.
4. **Phase 4 — Approvals**: approval steps. Requires source-write-back polling.
5. **Deprecation**: `routes:` syntax remains supported indefinitely as sugar for single-step workflows.
