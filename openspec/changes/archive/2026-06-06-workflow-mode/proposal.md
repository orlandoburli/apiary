# Proposal: Workflow Mode

## Why

The current pool/router model is a **flat dispatch**: one task enters, one agent executes it, done. This works well for simple, atomic tasks but breaks down as real-world usage grows more complex:

- **Sequential handoffs are manual**: a backend agent implements a feature, but triggering a code-review agent on the result requires a human or a second task entry in the source system.
- **No shared context between agents**: each agent invocation starts from scratch with only the original task description. Agents cannot build on each other's output.
- **No branching or retry logic per task**: if the reviewer rejects the implementation, there is no way to loop back to the implementer — the human must re-queue the task.
- **Human-in-the-loop is impossible**: there is no place to pause a run and require approval before continuing.

Workflow mode addresses all of this by promoting the dispatch model from **single-shot** to **multi-step pipeline**.

## What Changes

The router remains. Routes become **workflow triggers** — they select which workflow handles a matching task. The workflow then orchestrates one or more agents across multiple steps, with context flowing from step to step.

### New Concepts

| Term | Description |
|---|---|
| **Workflow** | A named, declarative graph of steps with a trigger condition |
| **Step** | One agent invocation within a workflow |
| **Step context** | Accumulated outputs from previous steps, injected into the next agent's prompt |
| **Approval** | A human approval checkpoint that pauses the workflow until someone responds |
| **Split** | A routing node that evaluates multiple `if` branches and activates one or more paths |
| **Condition** | A boolean expression evaluated against cell metadata or prior step outcomes |
| **Workflow Instance** | A single execution of a workflow bound to a specific Cell (task) |

### What Stays

- **Sources** — unchanged; they produce Cells as before.
- **Agents** — unchanged; they are the execution units, now invoked per-step rather than directly.
- **Router** — unchanged in matching logic; it now returns a workflow ID instead of an agent ID.

## New Schema

```yaml
workflows:
  - id: feature-development
    description: "Full feature implementation pipeline"
    trigger:
      priority: 10
      match:
        source: main-plane
        labels: [feature, ai-ready]
    steps:
      - id: plan
        agent: architect
        prompt: |
          Analyze this task and produce a detailed implementation plan.
          Output the plan as a markdown list.

      - id: implement
        agent: backend-dev
        depends_on: plan
        # step context: plan output is automatically injected

      - id: review
        agent: code-reviewer
        depends_on: implement

      - id: fix
        agent: backend-dev
        depends_on: review
        condition: "{{ steps.review.outcome == 'changes_requested' }}"
        next_on_pass: review   # loop back to reviewer

    on_complete:
      set_state: in_review
    on_fail:
      set_state: blocked
      add_labels: [workflow-failed]
```

### Parallel Steps

Steps without a `depends_on` relationship to each other run in parallel when they share the same upstream dependency:

```yaml
steps:
  - id: implement
    agent: backend-dev

  - id: write-tests
    agent: test-engineer
    depends_on: implement

  - id: write-docs
    agent: docs-writer
    depends_on: implement

  # write-tests and write-docs run in parallel after implement completes

  - id: review
    agent: code-reviewer
    depends_on: [write-tests, write-docs]   # waits for both
```

### Approval Steps

An approval step pauses the workflow and posts a message to the source task. A human reacts (e.g., adds a label, changes state, posts a comment with a keyword) to resume or abort.

```yaml
steps:
  - id: propose-plan
    agent: architect

  - id: human-approval
    type: approval
    depends_on: propose-plan
    message: |
      Implementation plan is ready. Reply "approve" to continue or "reject" to abort.
    resume_on:
      comment_contains: "approve"
    abort_on:
      comment_contains: "reject"

  - id: implement
    agent: backend-dev
    depends_on: human-approval
```

## Context Propagation: Workflow Memory

Steps do not receive raw prior outputs. Instead, context flows through a **workflow memory object** — a shared, enrichable document that each step can read from and write to.

Memory always contains the original Cell. Steps enrich it by declaring which structured output fields to persist (`memory.write`) and optionally producing a brief summary (`summary_prompt`). Full raw output is stored in SQLite for audit and resume but never forwarded.

```yaml
- id: plan
  agent: architect
  output_schema:
    type: object
    properties:
      complexity: {type: string, enum: [low, medium, high]}
      approach:   {type: string}
    required: [complexity, approach]
  summary_prompt: |
    In 3-5 bullet points: what you concluded and what the next agent needs to know.
  memory:
    write: [complexity, approach]   # these fields become available to all downstream steps

- id: implement
  agent: backend-dev
  depends_on: plan
  memory:
    read: true    # sees: cell + complexity + approach + plan summary
```

See [workflow-memory spec](specs/workflow-memory/spec.md) for the full reference.

## Backward Compatibility: Simple Route Mode

The existing `routes` + `workers`/`agents` model is preserved. Internally it is treated as a single-step workflow with no explicit workflow definition:

```yaml
# Old (still valid)
routes:
  - id: backend-bugs
    priority: 10
    match: {labels: [bug]}
    agent: backend-dev
    on_complete:
      set_state: in_review

# Equivalent in workflow mode
workflows:
  - id: backend-bugs
    trigger:
      priority: 10
      match: {labels: [bug]}
    steps:
      - id: fix
        agent: backend-dev
    on_complete:
      set_state: in_review
```

Both notations will be supported in `apiary.yaml`. A hive may mix simple routes and full workflows.

## Architecture Impact

| Component | Change |
|---|---|
| `router.go` | Returns `WorkflowID` (or synthesizes a single-step workflow for plain routes) |
| `dispatcher.go` | Replaced by `WorkflowEngine` — manages step sequencing, concurrency, and context |
| `config.go` | New `WorkflowConfig`, `StepConfig`, `ApprovalConfig` structs |
| `validate.go` | Validate step graph for cycles, dangling `depends_on`, agent references |
| SQLite schema | New `workflow_instances` and `step_runs` tables alongside existing `runs` |
| TUI | Workflow instance view: step-by-step progress within a running task |

## Branching: Split Steps and Conditions

### Problem with Binary Branching

The `next_on_pass`/`next_on_fail` fields on agent steps only handle two outcomes. Real workflows need:
- **Multi-way routing** based on task metadata (`bug` vs `feature` vs `docs`)
- **Agent-outcome routing** based on what the agent decided (reject → retry, approve → continue)
- **Parallel fan-out with selective join** (run A and B in parallel; proceed only when both pass)

### `type: split` — Multi-Way Conditional Branch

A split step is a routing node, not an agent invocation. It evaluates a list of `if` branches in order and activates the first match. An optional `else` branch catches the rest.

```yaml
steps:
  - id: triage
    type: split
    depends_on: []   # entry point — or omit to trigger on workflow start
    branches:
      - if: "cell.labels contains 'bug'"
        goto: fix-bug
      - if: "cell.labels contains 'feature' and cell.priority == 'urgent'"
        goto: fast-track-feature
      - if: "cell.labels contains 'feature'"
        goto: standard-feature
      - else: true
        goto: analyze-first

  - id: fix-bug
    agent: backend-dev
    prompt: "Fix this bug."

  - id: fast-track-feature
    agent: senior-dev
    prompt: "Implement urgently."

  - id: standard-feature
    agent: backend-dev
    prompt: "Implement this feature."

  - id: analyze-first
    agent: architect
    prompt: "Analyze and plan."
```

A split step has no agent output and contributes nothing to step context.

### `type: split` with `multi: true` — Fan-Out

When `multi: true`, all branches whose `if` evaluates to true are activated in parallel (not just the first):

```yaml
- id: notify-all
  type: split
  multi: true
  depends_on: review
  branches:
    - if: "cell.labels contains 'backend'"
      goto: update-backend-docs
    - if: "cell.labels contains 'frontend'"
      goto: update-frontend-docs
    - if: "cell.labels contains 'api'"
      goto: update-api-docs
```

### `on_fail: goto` on Agent Steps — Retry Loops

Agent steps can declare `on_fail` with a `goto` to loop back to an earlier step. This replaces the less explicit `next_on_fail` field and makes retry intent clear:

```yaml
steps:
  - id: implement
    agent: backend-dev

  - id: review
    agent: code-reviewer
    depends_on: implement
    on_fail:
      goto: implement      # reviewer rejected → loop back to implementer
      max_retries: 3       # abort if this loop runs more than 3 times
    on_pass:
      next: finalize       # explicit happy path (optional; inferred from depends_on otherwise)

  - id: finalize
    agent: backend-dev
    depends_on: review
    prompt: "Apply final touches and open PR."
```

`on_pass.next` is optional. When omitted, the step flows naturally to whatever depends on it.

### Expression Language

Conditions are simple attribute expressions — not Go templates. The evaluator supports:

| Expression | Description |
|---|---|
| `cell.labels contains "bug"` | Cell has label "bug" |
| `cell.priority == "urgent"` | Cell priority equals value |
| `cell.type == "feature"` | Cell type matches |
| `cell.title matches ".*hotfix.*"` | Cell title matches regex |
| `steps.<id>.state == "passed"` | Step completed successfully |
| `steps.<id>.state == "failed"` | Step failed |
| `steps.<id>.exit_code == 0` | Step exit code |
| `steps.<id>.output contains "LGTM"` | Step stdout contains string |
| `A and B`, `A or B`, `not A` | Boolean combinators |
| `(A or B) and C` | Grouping with parens |

Structured JSON output from agents is **not** supported in v1 — branching on agent output is limited to string matching (`output contains`) and exit codes.

### Summary: Step Types and Their Roles

| Step type | Invokes agent? | Branching | Spec |
|---|---|---|---|
| `agent` (default) | Yes | Optional `on_fail.goto` / `on_pass.next` | this doc |
| `split` | No | Multi-branch `if/else` (or `multi: true` fan-out) | this doc |
| `approval` | No | `resume_on` / `abort_on` | this doc |
| `foreach` | Yes (one per item) | None; `fail_fast` optional | [foreach-step](specs/foreach-step/spec.md) |
| `workflow` | No (delegates to child) | None | [sub-workflows](specs/sub-workflows/spec.md) |

## Capability Specs

| Spec | Description |
|---|---|
| [workflow](specs/workflow/spec.md) | **Complete schema reference** — all fields, all step types, annotated example |
| [workflow-memory](specs/workflow-memory/spec.md) | Shared memory object; `memory.write`, `summary_prompt`, memory injection format |
| [structured-output](specs/structured-output/spec.md) | Opt-in last-line JSON contract for typed agent output; enables field-level conditions |
| [foreach-step](specs/foreach-step/spec.md) | Dynamic fan-out over a runtime list from a prior step's output |
| [resume](specs/resume/spec.md) | Restart a failed instance from the last completed step |
| [per-step-model](specs/per-step-model/spec.md) | Override the agent's preferred model for a specific step |
| [sub-workflows](specs/sub-workflows/spec.md) | Invoke another named workflow as a step; one level of nesting |
| [source-adapter-watch](specs/source-adapter-watch/spec.md) | `PollTask` extension to `SourceAdapter` for approval step polling |
| [runner-interface](specs/runner-interface/spec.md) | `RunRequest`/`RunResult` changes: memory injection, summary, structured output |
| [concurrency-model](specs/concurrency-model/spec.md) | Global semaphore model across instances, parallel steps, and foreach |
| [workflow-hooks](specs/workflow-hooks/spec.md) | `state_lock` and `result_comment` behavior in multi-step workflows |
| [orphan-recovery](specs/orphan-recovery/spec.md) | Interrupted instance recovery on daemon restart |

## Resolved Questions

1. **How is step output structured?** ✓ — Opt-in `APIARY_OUTPUT:` last-line JSON contract; unstructured steps use `summary_prompt`. See [structured-output](specs/structured-output/spec.md) and [workflow-memory](specs/workflow-memory/spec.md).
2. **Cycle detection** ✓ — DAG enforced at config load; only explicit `on_fail.goto` back-edges to ancestor steps are permitted. See validation rules in `design.md`.
3. **Approval timeouts** ✓ — `timeout` field on approval steps; aborts the instance when elapsed. See [workflow/spec.md](specs/workflow/spec.md).
4. **Workflow versioning** ✓ — Config changes require a daemon restart. In-flight instances always run to completion on the config snapshot they started with (stored in SQLite at instance creation). If the workflow definition changes and an instance is later resumed, resume is blocked and the operator must re-trigger. See [orphan-recovery](specs/orphan-recovery/spec.md).
5. **Cross-source workflows** — Deferred to post-v1. A workflow always writes back to the source that triggered it (`cell.SourceID`). Multi-source orchestration requires a separate design.
