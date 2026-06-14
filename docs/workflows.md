# Workflows

A workflow is a pipeline of steps, fired by a `trigger` that matches tasks.
The simplest workflow — one trigger, one agent step — is plain routing:

```yaml
workflows:
  - id: implement
    trigger:
      priority: 10
      match:
        source: my-repo
        labels: [agent:engineer]
    steps:
      - id: run
        agent: engineer
    on_complete:
      set_state: closed
```

From there, workflows scale up to multi-agent pipelines with structured
outputs, conditional branches, human approval gates, CI waits, and retry
loops. This page covers the whole surface.

| Field | Description |
|---|---|
| `id` | Unique workflow identifier |
| `description` | Human-readable description |
| `trigger` | What starts it — see [Triggers](#triggers). Omit for [spawn-only workflows](tasks-and-fanout.md#named-workflows-without-triggers) |
| `steps` | The pipeline — see [Steps](#steps) |
| `on_complete` / `on_fail` | [Hooks](#completion-hooks) applied when the instance finishes |
| `env` | Workflow-scope [environment variables](configuration.md#environment-variables) |
| `resume` | [Resume policy](#resuming-instances): `allowed` (default), `forbidden`, `auto` |

## Triggers

Triggers are evaluated in ascending `priority` order (**lower number =
evaluated first**).

```yaml
trigger:
  priority: 10
  exclusive: false
  once: false
  match:
    source: my-repo
    labels: [backend, bug]
```

### `match` fields

| Field | Description |
|---|---|
| `source` | Only match tasks from this source ID |
| `labels` | Task must have **all** of these labels (case-insensitive) |
| `exclude_labels` | Task must have **none** of these labels |
| `exclude_label_prefix` | Reject tasks with any label starting with this prefix (e.g. `agent:`) |
| `states` | Task state must be in this list |
| `types` | Task type must be in this list (GitHub tasks are always `issue`) |
| `title_regex` | Task title must match this Go regexp |
| `priority` | Task priority must be in this list |

### Fan-out, `exclusive`, and `once`

By default a task **fans out to every workflow whose trigger matches**, each
as its own instance; the task itself settles when the last one finishes (see
[task-level hooks](tasks-and-fanout.md#task-level-hooks)). Two flags change
that:

- **`exclusive: true`** — when this trigger matches, evaluation stops: no
  lower-priority trigger is considered. Use it for a classifier or
  incident-triage workflow that must own a task alone rather than run
  alongside others.
- **`once: true`** — run this workflow **at most once per task**. After it has
  a completed instance for the task, later polls do not re-dispatch it even
  though the item still matches the trigger. Essential for
  decomposition/fan-out workflows whose source item stays in the trigger set
  after success (it spawns children but doesn't change its own labels) —
  without `once`, every poll would create a duplicate set of children. Failed
  runs are not blocked; they stay eligible for retry up to
  `settings.max_attempts`.

## Steps

Steps run **sequentially in the order written**. Each step has an `id`
(unique within the workflow) and a `type` — `agent` is the default; the
others are `approval`, `wait_for`, `split`, `foreach`, `workflow`, and
`parallel` (authored as a `parallel:` block).

### Agent steps

```yaml
steps:
  - id: classify
    agent: investigator
    model: claude-opus-4-8        # optional: override the agent's model here
    prompt: "Classify this issue by complexity and approach."
    summary_prompt: "In 3-5 bullets: the approach, risks, and open questions."
    output:
      type: object
      properties:
        complexity: {type: string, enum: [low, medium, high]}
        approach:   {type: string}
      required: [complexity, approach]
    on_missing_output: warn       # warn (default) | fail | ignore
    memory:
      write: [complexity, approach]
```

| Field | Description |
|---|---|
| `agent` | Agent that runs this step |
| `model` | Override the agent's model for this step only |
| `prompt` | Step instruction, appended to the task context (title, description, labels) and the agent's soul file |
| `summary_prompt` | Asks the agent for a short summary, stored on the step and shown in the dashboard |
| `output` | JSON Schema for structured output (alias: `output_schema`) — see below |
| `on_missing_output` | What to do when `output` is declared but not satisfied: `warn` (default), `fail`, `ignore` |
| `memory` | Which output fields to persist, and whether to inject memory — see [Workflow memory](#workflow-memory) |
| `publish` / `spawn` | Control agent-emitted write-backs and child tasks — see [Tasks & fan-out](tasks-and-fanout.md) |
| `env` | Step-scope environment variables (highest precedence) |
| `idempotent` | Mark the step safe to re-run on [resume](#resuming-instances) |

**Structured output.** When a step declares an `output` schema, the agent is
instructed to end its run with a single line:

```
APIARY_OUTPUT: {"complexity": "high", "approach": "refactor the dispatcher"}
```

Apiary extracts and validates the payload against the schema. Validated
fields listed in `memory.write` become workflow memory for later steps.

### Workflow memory

Memory is a JSON document scoped to one workflow instance. Steps write to it
by persisting structured-output fields (`memory.write: [field, …]`), and
every later step reads it automatically — the document is injected into the
agent's context unless the step opts out with `memory: {read: false}`.

This is the *instance* tier: it dies with the workflow instance. For memory
that survives across instances — per-task working notes and durable
daemon-wide facts written via `APIARY_MEMORIZE` — see
[Agent Memory](memory.md).

Memory also drives all control-flow expressions:

```yaml
- id: approve
  type: approval
  if: ${{ memory.complexity == "high" }}
```

### Control flow (authoring syntax)

Steps support guards, rejection loops, parallel groups, and iteration
directly in the authored YAML. The engine lowers this to a DAG internally.

#### `if:` — conditional steps

```yaml
- id: approve
  type: approval
  if: ${{ memory.complexity == "high" }}
  message: "High-complexity change — comment `approve` to proceed."
```

When the guard is false the step is skipped (and anything depending on it
cascades).

#### `reject_when:` / `on_reject:` — review loops

A step can declare a logical rejection gate over its own fresh output, and
loop back on rejection:

```yaml
- id: implement
  agent: engineer
  prompt: "Implement the change following the approach in memory."

- id: review
  agent: reviewer
  output:
    type: object
    properties:
      verdict:  {type: string, enum: [approved, rejected]}
      feedback: {type: string}
    required: [verdict]
  memory:
    write: [verdict, feedback]
  reject_when: ${{ memory.verdict == "rejected" }}
  on_reject:
    restart_from: implement    # loop back to an earlier step
    max: 2                     # at most 2 rejection+retry cycles
```

When `reject_when` evaluates true the step is treated as failed and the
engine restarts from `restart_from`, up to `max` times. The reviewer's
`feedback` is in memory, so the implementer sees *why* it was rejected.

#### `parallel:` — concurrent steps

```yaml
- id: checks
  parallel:
    - id: lint
      agent: qa
      prompt: "Run the linters and report problems."
    - id: tests
      agent: qa
      prompt: "Run the test suite and report failures."
  join: all        # all (default) | any | ${{ expr }}
```

Children run concurrently; `join` decides the group's outcome:

- **`all`** (default) — every child must pass.
- **`any`** — at least one child must pass.
- **`${{ expr }}`** — a condition expression evaluated over the children's
  outcomes, exposed as `steps.<child-id>.state` (`passed`/`failed`) and
  `steps.<child-id>.output`, alongside the usual `cell.*` and `memory.*`
  accessors. Example: `${{ steps.lint.state == 'passed' and steps.tests.output
  contains 'ok' }}`. A malformed expression fails `apiary validate`; an
  expression that cannot be evaluated at runtime fails the parallel step.
  Note: expression accessors cannot reference child ids containing hyphens —
  use `snake_case` ids for children you test in a join expression.

#### `for_each:` — iteration

```yaml
- id: implement-each
  for_each: ${{ memory.tasks }}    # an array field from a previous step
  as: task
  max: 10
  step:
    id: implement-one
    agent: engineer
    prompt: "Implement this sub-task: ${{ task }}"
```

#### Low-level primitives

`type: split` (explicit branch table) and `on_fail.goto` /
`on_pass.next` edges are the lowered DAG form, still available for
hand-tuned graphs:

```yaml
- id: route
  type: split
  branches:
    - if: 'memory.agent == "po"'
      goto: spec
    - if: 'memory.agent == "staff"'
      goto: design
    - else: true
      goto: implement
```

`on_fail: {goto: <ancestor-step>, max_retries: N}` loops back on failure with
its own retry budget.

!!! warning "Don't mix the two styles"
    Use either the authoring syntax (`if`, `reject_when`, `parallel`,
    `for_each`, nested `steps:`) or the low-level primitives (`type: split`,
    `goto`) within one workflow — not both.

### Expression syntax

`if:`, `reject_when:`, split branch `if:`, expression-valued `join:`, and
the lowered `condition:` / `fail_when:` fields all share one expression
language. The `${{ … }}` wrapper is optional and purely cosmetic — `if:
${{ memory.x == "done" }}` and `if: 'memory.x == "done"'` parse identically
(only `join:` requires the wrapper, to distinguish an expression from
`all`/`any`).

An expression is one or more **comparisons** combined with logical
operators:

```text
cell.priority == "urgent" and (cell.labels contains "bug" or not memory.triaged == "yes")
```

#### Comparisons

Each comparison is `<accessor> <operator> <literal>` — the accessor must be
on the left, the literal on the right.

| Operator | Works on | Meaning |
|----------|----------|---------|
| `==`, `!=` | strings, numbers | Equality. Numeric when both sides are numbers (e.g. `steps.test.exit_code == 0`), string comparison otherwise. |
| `contains` | lists, strings | On a list (`cell.labels`): exact element membership. On a string: substring match. |
| `matches` | strings | Regular-expression match ([Go RE2 syntax](https://pkg.go.dev/regexp/syntax)). Not supported on lists. |

Literals are quoted strings (`"…"` or `'…'`, no escape sequences) or bare
numbers (integers, decimals, negatives).

#### Logical operators

`and`, `or`, and `not` — lowercase keywords, with parentheses for grouping.
Precedence is `not` > `and` > `or`, and `and`/`or` short-circuit.

!!! warning "No C-style operators"
    `&&`, `||`, and `!` are **not** part of the language — use `and`, `or`,
    `not`. (`!=` is the one exception: it is the inequality operator.)

#### Accessors

| Accessor | Type | Value |
|----------|------|-------|
| `cell.title` | string | task title |
| `cell.type` | string | task type |
| `cell.priority` | string | task priority |
| `cell.state` | string | task state |
| `cell.source` | string | source id the task came from |
| `cell.labels` | list | task labels (use `contains`) |
| `memory.<key>` | string | workflow-memory value written by an earlier step |
| `steps.<id>.state` | string | `passed` or `failed` |
| `steps.<id>.output` | string | the step's output text |
| `steps.<id>.exit_code` | number | the step's exit code |

A `memory.<key>` that was never written — and a `steps.<id>` that has not
run — resolves to the empty string, so `memory.flag == ""` is the idiom for
"not set". Lists only support `contains`; `==` or `matches` on `cell.labels`
is an error.

#### Examples

```yaml
# label routing
if: ${{ cell.labels contains "hotfix" }}

# combine task fields
if: ${{ cell.priority == "urgent" and cell.type != "epic" }}

# branch on a previous step's outcome
if: ${{ steps.tests.state == "failed" or steps.tests.exit_code != 0 }}

# regex over memory
reject_when: ${{ memory.verdict matches "reject|veto" }}

# negation + grouping
if: ${{ not (memory.track == "docs" or memory.track == "chore") }}
```

!!! note "Malformed expressions fail loudly"
    Every expression is statically parsed by `apiary validate` (and at daemon
    config load): a syntax slip like `&&` instead of `and` is rejected
    pre-flight with a pointer to the supported operator. If an expression
    still cannot be evaluated at runtime (e.g. an invalid regex operand or an
    unknown accessor field), the step **fails** — triggering its `on_fail`
    handling — rather than being silently treated as false. A branch can
    therefore never be dropped without an error signal.

### Approval steps

An approval step parks the workflow until a human acts on the source item:

```yaml
- id: approve
  type: approval
  message: |
    High-complexity change detected.
    Comment `approve` to proceed or `reject` to stop.
  resume_on: {comment_contains: "approve"}
  abort_on:  {comment_contains: "reject"}
  timeout: 48h
```

The `message` is posted to the source item (e.g. as an issue comment). The
instance enters the `approval_waiting` state and is re-checked each poll
cycle against the resume/abort conditions:

| Condition | Fires when |
|---|---|
| `comment_contains: "text"` | a new comment contains the text |
| `label_added: "name"` | the label was added to the item |
| `state_changed: "name"` | the item entered that state |

Any single matching condition is sufficient (OR semantics). `timeout` fails
the step if nobody responds in time.

Parked approvals are durable: they are **rehydrated after a daemon restart**
with their original timestamps and timeout intact.

### `wait_for` steps — waiting on CI

A `wait_for` step parks the workflow until an external condition resolves.
Two kinds are supported: `ci` waits for the checks on the pull request linked
to this task; `dependency` waits for the task's upstream blockers (see the
next section).

```yaml
- id: implement
  agent: engineer
  prompt: "Implement the change and open a PR that references this issue."

- id: check-ci
  type: wait_for
  wait_for:
    kind: ci
    check_interval: 60s       # how often to poll (default 1m)
    max_duration: 2h          # total budget before timing out (default 2h)
    fail_if_not_passed: true  # default: red CI fails the step
  on_conflict:                # optional: route merge conflicts separately
    goto: implement
    max_retries: 2

- id: merge
  agent: engineer
  prompt: "CI is green — merge the PR."
```

How it works:

- The PR is discovered through the issue's timeline (a PR that references
  the issue), so the agent just needs to open a PR that links the task.
- Each poll records a row of history — status, PR URL, per-check detail —
  which the dashboard shows in the task's detail view, so a long CI wait is
  fully auditable.
- Poll statuses are `passed`, `failed`, `pending`, `timeout`, `error`,
  `unknown`.
- Like approvals, parked CI waits **survive daemon restarts**.
- `remove_label: <name>` clears a stale label from the task before polling
  begins.

**Merge conflicts.** If the PR cannot be merged because of conflicts, the
step fails immediately (no point waiting for CI). When an `on_conflict` edge
is present, it routes that failure exclusively — typically looping back to
the implementation step so the agent rebases — with its own `max_retries`
budget, separate from `on_fail`. Without `on_conflict`, a conflict falls
through to `on_fail` like any other failure.

### `wait_for` steps — waiting on blockers (`kind: dependency`)

Triggers select a task by its own labels/state, so a task that is *blocked by*
another (a Jira "is blocked by" link, a GitHub issue dependency) would
otherwise start in parallel with its blocker. A `wait_for` step with
`kind: dependency` parks the workflow until every blocker is satisfied, then
auto-resumes — no labels to re-add, enforced by the engine regardless of agent
behaviour:

```yaml
- id: await-blockers
  type: wait_for
  wait_for:
    kind: dependency
    satisfied_when: [merged, done]  # blocker OK when its PR merged OR status is Done-category
    blocker_link_type: "Blocks"     # source-native relation; default: the source's blocking link
    check_interval: 5m
    max_duration: 168h              # optional; default: no deadline for this kind
    on_timeout: hold                # hold (default) keeps it parked for a human; fail fails the step

- id: implement
  agent: engineer
  prompt: "Blockers are resolved — implement the change."
```

How it works:

- Placed as the first step, it suspends the instance (parked, restart-safe,
  exactly like a CI wait) while any blocker is unsatisfied, re-checking every
  poll cycle, and resumes the pipeline automatically once all are.
- `satisfied_when` lists the conditions under which a blocker counts as
  satisfied — `merged` (a pull request linked to the blocker is merged)
  and/or `done` (the blocker's status is Done-category/closed). ANY listed
  condition satisfies a blocker. Default: `[merged, done]`.
- Blockers come from the source adapter: **Jira** reads the inward side of
  the issue's `Blocks` links (*"is blocked by"*; `blocker_link_type`
  overrides the link type); **GitHub** reads the issue dependencies API
  (*"blocked by"*). A source without blocker support is rejected at config
  validation.
- Unlike `ci`, the default `max_duration` is *no deadline*, and at a
  configured deadline the default `on_timeout: hold` keeps the instance
  parked (a blocker may legitimately take days) — set `on_timeout: fail` to
  fail the step instead.
- Every check is recorded in the same poll history as CI waits, so the
  dashboard shows which blockers are still pending.

### Sub-workflows

A step can run another workflow as a child:

```yaml
- id: validate
  type: workflow
  workflow: qa-suite
```

The child runs as its own instance linked to the parent. For *runtime*
fan-out — an agent deciding to create child tasks — see
[Tasks & fan-out](tasks-and-fanout.md).

## Completion hooks

`on_complete` runs when the instance succeeds, `on_fail` when it fails. Both
apply to the task's source item:

```yaml
on_complete:
  set_state: in review
  add_labels: [reviewed]
  remove_labels: [create-spec]
on_fail:
  add_labels: [needs-attention]
```

`remove_labels` strips labels from the source item — typically the label that
triggered the workflow, so a label-driven trigger fires once and the item
stops matching on the next poll. Removals run after `add_labels`, and sources
that don't support label removal skip the directive.

A workflow can also override the global result-comment behavior:
`result_comment: on_complete | per_step | off`.

## Resuming instances

Every step's state, output, and memory are persisted, so a failed or
interrupted instance can continue from where it stopped instead of starting
over:

```sh
apiary instances                  # list instances and their states
apiary instances <instance-id>    # step-level detail
apiary resume <instance-id>       # replay cached steps, continue from failure
```

The workflow's `resume` policy controls eligibility: `allowed` (default),
`forbidden`, or `auto`. Completed steps are skipped on resume (their cached
outputs and memory are restored); steps marked `idempotent: true` are safe to
re-run when the engine needs to.

A complete annotated pipeline using most of this page lives at
[`.apiary/example-workflow.yaml`](https://github.com/orlandoburli/apiary/blob/main/.apiary/example-workflow.yaml).
