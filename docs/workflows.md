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
| `inputs` / `outputs` | Typed public contract for a [reusable subworkflow](#reusable-subworkflows) |
| `trigger` | What starts it — see [Triggers](#triggers). Omit for [spawn-only workflows](tasks-and-fanout.md#named-workflows-without-triggers) |
| `steps` | The pipeline — see [Steps](#steps) |
| `on_complete` / `on_fail` | [Hooks](#completion-hooks) applied when the instance finishes |
| `env` | Workflow-scope [environment variables](configuration.md#environment-variables) |
| `working_dir` | [Working directory](runners.md#working-directory) for every step of this workflow |
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

A workflow with **no** `trigger:` block never starts on its own. That is a valid
configuration — see [Manual runs](#manual-runs) — but `apiary validate` warns
about it, since an unreachable workflow is usually a mistake.

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

An exclusive claim is **not** re-evaluated if the exclusive workflow is
afterwards held back before dispatch — because it already has a live instance,
because it is a spent `once` route, or because it hit the consecutive-failure
cap. The task simply runs nothing that cycle: falling through to the triggers
the exclusive one suppressed would start exactly the work it exists to prevent,
alongside a run that may still be active. The daemon reports the situation at
`INFO` and names the suppressed workflows, e.g.:

```text
cell ISSUE-9 ("Ship it"): task 019f… matched 1 workflow(s) but every one was
dropped before dispatch — decompose (once: already completed); exclusive
workflow decompose had already suppressed 1 lower-priority match(es) (review),
which are not reconsidered; nothing will run for this task until the reason clears
```

If you want the lower-priority workflow to take over in that state, make the
handover explicit — narrow the exclusive trigger's `match` so it stops matching
once its work is done (e.g. on a label the workflow itself sets).

### Manual runs

Everything above describes when a workflow starts **on its own**. Any workflow
can also be started by hand, which skips all of it:

```sh
apiary dispatch triage --item PSP-199   # this workflow, this item, right now
apiary dispatch nightly-audit           # standalone: no source item at all
```

The same action is `W` in the dashboard. A manual run ignores the trigger's
`match` block, exclusive suppression, the live-instance guard, `once`, and the
consecutive-failure cap — so it starts a **second concurrent instance** of a
workflow that is already running, by design. Every bypass is reported.

The item does not have to be one apiary is already tracking: a reference it has
never polled is fetched from its source and bound on the spot, so a manual run
reaches tickets outside the source's `filters`. Add `--source` when more than one
source could hold the reference.

Two consequences worth knowing:

- A workflow with no `trigger:` block is perfectly usable as a **manual-only**
  workflow: maintenance jobs, audits, one-off reports.
- A standalone run (no `--item`) has no source binding, so steps that write back
  to a source — comments, state locks, `materialize: sub_issue` — are no-ops.
  Pass `--input key=value` for the values those workflows need instead.

See [`apiary dispatch`](cli.md#apiary-dispatch) for the full behaviour.

### PR event triggers (`on:`)

By default a trigger matches **work items** (issues/tickets polled from a
source). Setting `on:` to a pull-request event kind makes the trigger fire on
PR activity instead, polled from sources that support PR events (currently
`github`):

```yaml
workflows:
  - id: react-to-pr-comment
    trigger:
      on: pr_comment
      comment_matches: "(?i)@apiary"     # only comments mentioning @apiary
    steps:
      - id: fix
        agent: engineer
        prompt: |
          A reviewer commented on PR ${{ event.pr_number }}. Address it:
          the comment body is in $APIARY_EVENT_BODY.

  - id: fix-review-feedback
    trigger:
      on: pr_review_changes_requested
      match:
        source: github
        labels: [apiary]                 # of the PR's originating issue
    steps:
      - id: address
        agent: engineer
```

| Field | Description |
|---|---|
| `on` | `item` (default) or `pr_comment`, `pr_review_approved`, `pr_review_changes_requested` |
| `comment_matches` | `pr_comment` only: comment body must match this Go regexp (case-sensitive — prefix `(?i)` for case-insensitive). Validated at config load |
| `authors` | Only fire for these author logins (case-insensitive); overrides `authors_association` |
| `authors_association` | Only fire for authors with one of these repo associations. **Default: `[OWNER, MEMBER, COLLABORATOR]`** — drive-by comments from strangers never spawn agents |
| `max_dispatches` | Cap dispatches per (workflow, PR) — a runaway-loop budget. `0` = unlimited |

Semantics:

- **Exactly once.** Each event (a comment or review) dispatches a matching
  workflow exactly once, persisted in SQLite — a daemon restart or poll
  overlap never re-dispatches it. First-time enablement baselines to "now":
  historical comments are ignored.
- **Task binding.** The instance binds to the InternalTask of the PR's
  originating issue when one exists (resolved from the PR body's `Closes #N`
  reference), so lineage, dashboard, and transcripts work unchanged.
  Otherwise a standalone per-PR task is bound.
- **Loop prevention.** Events authored by the daemon's own source token
  identity and by bot accounts are dropped at the adapter, so an agent's own
  PR comments can never re-trigger a workflow. `max_dispatches` is the
  backstop for multi-account ping-pong.
- **`match` on event triggers.** `match.source` applies to the event's
  source; the item-shaped fields (`labels`, `states`, …) apply to the
  originating issue's task and only match when that task exists.
- **Event payload.** Every step of an event-triggered instance gets
  `APIARY_EVENT_KIND`, `APIARY_EVENT_AUTHOR`, `APIARY_EVENT_BODY`,
  `APIARY_PR_NUMBER`, and `APIARY_PR_URL` in its environment, and expressions
  can read `event.*` (see [Accessors](#accessors)).

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
                                  # warn still fails a reject_when gate that
                                  # reads this step's own memory.write keys
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
| `on_missing_output` | What to do when `output` is declared but not satisfied: `warn` (default), `fail`, `ignore`. Under `warn` the step still passes, but a `reject_when` gate reading one of this step's own `memory.write` keys fails closed — see [review loops](#reject_when--on_reject--review-loops). `ignore` opts out of both |
| `memory` | Which output fields to persist, and whether to inject memory — see [Workflow memory](#workflow-memory) |
| `publish` / `spawn` | Control agent-emitted write-backs and child tasks — see [Tasks & fan-out](tasks-and-fanout.md) |
| `pull_request_from` | Name of an output field holding the URL of a PR this step opened; links that PR to the task — see [Linking a PR](#linking-a-pr-pull_request_from) |
| `env` | Step-scope environment variables (highest precedence) |
| `working_dir` | [Working directory](runners.md#working-directory) for this step (highest precedence) |
| `idempotent` | Mark the step safe to re-run on [resume](#resuming-instances) |

**Structured output.** When a step declares an `output` schema, the agent is
instructed to end its run with a single line:

```
APIARY_OUTPUT: {"complexity": "high", "approach": "refactor the dispatcher"}
```

Apiary extracts and validates the payload against the schema. Validated
fields listed in `memory.write` become workflow memory for later steps.

#### Linking a PR (`pull_request_from`)

Apiary links pull requests to a task so features like the dashboard's `p`
shortcut ("open the task's PR") have something to open. It discovers them
through the source — which only works for a source that can enumerate the PRs
of one of its items. A GitHub source can; Jira and Plane cannot, so a task from
those sources shows no PRs at all, however many its agents opened.

`pull_request_from` closes that gap from the workflow side: point it at the
output field carrying the PR URL, and the step registers its own PR.

```yaml
- id: implement
  agent: engineer
  prompt: "Implement the change and open a PR."
  output:
    type: object
    properties:
      pr_url: {type: string}
    required: [pr_url]
  pull_request_from: pr_url
```

- The URL is parsed for the PR number; GitHub, Codeberg/Forgejo, GitLab and
  Bitbucket link shapes are recognised, self-hosted hosts included.
- Re-reporting the same PR (a loop-back that re-runs the step) refreshes the
  existing link instead of adding a duplicate. The most recently linked PR is
  the one the shortcut opens.
- Best-effort by design: a step that opened no PR simply leaves the field
  empty, and a URL that cannot be parsed is logged and skipped — neither fails
  a step whose real work succeeded. A failed step's PR is never linked.
- `apiary validate` rejects a `pull_request_from` naming a field the step's
  `output` schema does not declare.

A linked PR is also what `wait_for {kind: ci}` waits on when the forge is not
the task's source — see [waiting on CI hosted
elsewhere](#waiting-on-ci-hosted-elsewhere-ci_source).

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

**Gates fail closed.** A gate that cannot be evaluated is never a pass. If the
gate reads a memory key the step itself declared in `memory.write` and the
agent did not emit a value for it — typically because it skipped its
`APIARY_OUTPUT:` line, or emitted it without that field — the step **fails**
instead of quietly reading `""` and deciding "not rejected". In the example
above, a reviewer that rejects the work in prose but forgets the output line
fails the `review` step and takes the `on_reject` loop back to `implement`,
rather than passing the work along as approved.

This is scoped to keys the gating step owns: a gate reading a key written by
some *earlier* step is unchanged, and so are `if:` conditions and split
branches. Opt out for a single step with `on_missing_output: ignore`.

The condition is recorded as a `step.gate_unevaluable` execution event (visible
in the dashboard and the event stream) and the step run is persisted as
`failed`, not only logged.

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

**Which children are allowed.** A group runs `agent` and `wait_for` children.
Anything else — an `approval`, a `for_each:`, or a nested group — is rejected by
`apiary validate`.

A `wait_for` child parks the whole group until its wait resolves: the children
that already finished are remembered, so a re-check re-polls only the wait and
never re-runs a sibling that passed (this survives daemon restarts, like any
other park). A group whose `join` is already decided by the children that
finished does not wait at all — under `join: all` a failed review fails the
group immediately instead of sitting on a two-hour CI budget.

```yaml
- id: gate
  parallel:
    - id: review
      agent: reviewer
      prompt: "Review the PR."
    - id: await_ci
      type: wait_for
      wait_for:
        kind: ci
        max_duration: 2h
  join: all
```

Children are full steps: a child that declares an `output` schema and emits no
`APIARY_OUTPUT` honours its own `on_missing_output` exactly like a top-level
step — `fail` fails the child (and, under `join: all`, the group), `warn`
records a `step.missing_output` event, `ignore` opts out. The same applies to
the inner step of a `for_each:`.

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
| `event.kind` | string | PR event kind (`pr_comment`, `pr_review_approved`, `pr_review_changes_requested`) |
| `event.body` | string | comment / review body |
| `event.author` | string | event author's login |
| `event.author_association` | string | author's repo association (e.g. `COLLABORATOR`) |
| `event.pr_number` | string | pull request number |
| `event.pr_url` | string | pull request URL |

`event.*` is populated only on instances started by a [PR event
trigger](#pr-event-triggers-on); elsewhere (and on instances rehydrated after
a daemon restart) it resolves as missing (`""`).

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

An approval step parks the workflow until a human answers it. The instance
enters the `blocked` state with a `blocked_reason` of `approval`, and survives daemon restarts.

The simplest form waits for whoever is running apiary — no approver list, no
source signals:

```yaml
- id: approve
  type: approval
  message: High-complexity change detected. Proceed?
  timeout: 48h
```

Answer it from the dashboard (`y`/`n`, or `a` to open the form) or from the
terminal:

```bash
apiary approvals                    # what is waiting
apiary approve wf-8a31:approve      # let it through
apiary reject  wf-8a31:approve --comment "needs a design doc first"
```

Without a `timeout` such a gate parks until it is answered, which is often
what you want; `apiary validate` warns so it is never a surprise.

#### Asking for more than yes/no

Declare `fields` and the gate collects structured answers. They reach the
workflow as `memory.<field>`, so a `choice` field is how a human picks the
branch:

```yaml
- id: pick-rollout
  type: approval
  message: Release is staged. How should it go out?
  fields:
    - name: strategy
      label: Rollout strategy
      type: choice
      options: [canary, blue_green, full]
      required: true

- id: canary-deploy
  agent: release-engineer
  if: ${{ memory.strategy == 'canary' }}
```

Field types are `string`, `text`, `boolean`, `number`, and `choice`. The
dashboard renders them as a form (`a`); the CLI prompts for them on a terminal
and takes `--field strategy=canary` off one. A **rejection never collects
fields** — refusing a change should not mean filling in its paperwork.

#### Waiting on a source signal instead

`resume_on` / `abort_on` park the gate against the source item rather than a
local answer:

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

The `message` is posted to the source item (e.g. as an issue comment), and the
gate is re-checked each poll cycle against the conditions:

| Condition | Fires when |
|---|---|
| `comment_contains: "text"` | a new comment contains the text |
| `label_added: "name"` | the label was added to the item |
| `state_changed: "name"` | the item entered that state |

Any single matching condition is sufficient (OR semantics). `timeout` fails
the step if nobody responds in time.

!!! warning "Comment matching has no author filter"
    `comment_contains` scans every comment on the item, including the
    `message` this step just posted. A gate whose message contains the word it
    waits for resumes itself on the next poll — from the outside it looks like
    the workflow never stopped. Either keep the trigger word out of the
    message, or drop `resume_on` and answer the gate locally.

Parked approvals are durable: they are **rehydrated after a daemon restart**
with their original timestamps and timeout intact.

For authorized approvers, quorum, delegation, structured fields, dashboard and
signed webhook responses, reminders/escalation, and sensitive-action policies,
see [Human-in-the-loop approvals](human-approvals.md).

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
- `kind: ci` needs a source that can poll check runs (GitHub today). Against a
  source that cannot — Jira, Plane, an alert source — `apiary validate` rejects
  the step at config time, wherever it sits, including inside a `parallel:`
  group. When the PRs live on a forge that is not the task's source, add
  [`ci_source`](#waiting-on-ci-hosted-elsewhere-ci_source) instead.
- Each poll records a row of history — status, PR URL, per-check detail —
  which the dashboard shows in the task's detail view, so a long CI wait is
  fully auditable.
- Poll statuses are `passed`, `failed`, `pending`, `timeout`, `error`,
  `unknown`, `unsupported`. `unsupported` means the source cannot poll CI at
  all: the step fails at once with the cause logged and recorded, rather than
  polling until `max_duration`.
- Like approvals, parked CI waits **survive daemon restarts**.
- `remove_label: <name>` clears a stale label from the task before polling
  begins.

**Merge conflicts.** If the PR cannot be merged because of conflicts, the
step fails immediately (no point waiting for CI). When an `on_conflict` edge
is present, it routes that failure exclusively — typically looping back to
the implementation step so the agent rebases — with its own `max_retries`
budget, separate from `on_fail`. Without `on_conflict`, a conflict falls
through to `on_fail` like any other failure.

#### Waiting on CI hosted elsewhere (`ci_source`)

The default CI wait resolves the PR from the task's own source item, which only
a source that hosts both the issue **and** the code can do. In the common split
setup — issues in Jira, code on GitHub — nothing in the tracker knows what a
pull request is, and the wait can never pass.

`ci_source` names the configured source that hosts the PR, and the wait polls
*that* forge for the PR the workflow itself reported:

```yaml
sources:
  - id: jira
    type: jira
    config: {site: acme, email: ..., api_token: ...}
  - id: github            # the forge: same repo the agents push to
    type: github
    config: {repo: acme/backend, api_key: ...}

workflows:
  - id: deliver
    trigger: {match: {source: jira, labels: [ai-ready]}}
    steps:
      - id: implement
        agent: engineer
        prompt: "Implement the change and open a PR."
        output:
          type: object
          properties:
            pr_url: {type: string}
          required: [pr_url]
        pull_request_from: pr_url     # ← records the PR against the task

      - id: check-ci
        type: wait_for
        wait_for:
          kind: ci
          ci_source: github           # ← poll THIS source, by PR number
          check_interval: 60s
          max_duration: 2h
```

- The PR comes from the task's linked pull requests, so a step earlier in the
  workflow must record one with
  [`pull_request_from`](#linking-a-pr-pull_request_from). The **most recently
  linked** PR is the one polled — a rework lap after a red CI waits on the new
  PR, not the old one.
- Until a PR is linked, the wait simply stays **pending** (parked, restart-safe)
  rather than failing — it is safe to start waiting before the PR exists.
  `max_duration` still bounds it.
- The task's own source needs no CI capability at all: with `ci_source` set,
  `apiary validate` checks the *named* source instead, rejecting one that is not
  configured or whose adapter cannot poll a PR's checks.
- The named source must be configured for the repository the PRs live in. A PR
  URL from a different repository is refused rather than answered with a
  same-numbered PR of the configured one.
- Everything else is unchanged: conflict detection, `fail_if_not_passed`,
  `on_conflict`, poll history, restart-safe parking.

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

### Reusable subworkflows

A step can still run another workflow declared in the same `apiary.yaml` by ID:

```yaml
- id: validate
  type: workflow
  workflow: qa-suite
```

For reusable pipelines, place one workflow definition in a local YAML file. The
file declares its typed inputs and explicitly maps its public outputs:

```yaml
# workflows/prepare-repository.yaml
id: prepare-repository
inputs:
  repository:
    type: string
    required: true
  branch:
    type: string
    default: main
outputs:
  workspace:
    type: string
    value: ${{ steps.checkout.workspace }}
steps:
  - id: checkout
    agent: engineer
    prompt: Clone ${{ inputs.repository }} at ${{ inputs.branch }}.
    output:
      type: object
      properties:
        workspace: {type: string}
      required: [workspace]
```

Reference the file relative to the YAML file containing the call. `.yaml` or
`.yml` may be omitted:

```yaml
steps:
  - id: prepare
    uses: ./workflows/prepare-repository
    with:
      repository: ${{ task.repository }}

  - id: test
    uses: ./workflows/run-tests.yaml
    with:
      workspace: ${{ steps.prepare.workspace }}
    timeout: 30m
```

Input and output types are `string`, `number`, `integer`, `boolean`, `array`,
and `object`. `with` accepts literals and expressions rooted at `task`, `cell`,
`memory`, or a prior step's structured output (`steps.<id>.<field>`). A child
may use its values in prompts and nested `with` bindings through
`${{ inputs.<name> }}`. Only outputs listed in the child's `outputs` map return
to the parent.

`apiary validate` recursively resolves every local reference, strictly decodes
the referenced files, validates required/default values and output mappings,
and rejects direct or indirect cycles with the complete reference chain. Remote
URLs and versioned packages are not supported yet.

Each call is recorded as a step in the parent instance. The child runs as its
own instance linked through `parent_instance_id`, so task history exposes the
call plus every internal child step, log, token count, and cost record. Child
failure fails the call step and prevents dependent parent steps from running.
Parent cancellation and an optional call `timeout` cancel the child; both the
child instance and parent call are recorded as failed. Child hooks do not mutate
the parent's source item.

For *runtime* fan-out—an agent deciding to create child tasks—see
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

A workflow can also override the global result-comment behavior with
`result_comment`:

| Mode | Posts |
|---|---|
| `on_complete` | the workflow's memory document, when it succeeds |
| `on_fail` | the memory document, when it fails |
| `always` | both |
| `off` | nothing |
| `per_step` | each step's raw output, as its own comment — **deprecated** |

The completion modes post the **memory document**: the aggregate the engine
builds from every step's `memory.write` fields. No single agent produces it, and
`on_fail` covers exactly the runs where a step crashed, timed out, or was rate
limited and emitted nothing at all — so these are the right tool when you want a
guaranteed write-back.

`per_step` is deprecated because it dumps a step's stdout verbatim. An agent that
wants to report should say so deliberately with an
[`APIARY_PUBLISH`](tasks-and-fanout.md#apiary_publish-write-back) block, choosing its own
wording. It still works, but `apiary validate` advises against it.

## Resuming instances

Every step's state, output, and memory are persisted, so a failed or
interrupted instance can continue from where it stopped instead of starting
over:

```sh
apiary instances                  # list instances and their states
apiary instances <instance-id>    # step-level detail
apiary resume <instance-id>       # create a descendant and continue from failure
apiary resume <instance-id> --from <step-id>
apiary resume <instance-id> --definition original
```

The workflow's `resume` policy controls eligibility: `allowed` (default),
`forbidden`, or `auto`. With `auto` (which requires every step to be
`idempotent: true`) the daemon also resumes by itself: instances orphaned by a
restart are continued at startup instead of being re-dispatched from step 1 —
see [Surviving restarts](resilience.md#surviving-restarts).
A manual resume creates a new workflow instance whose
`resumed_from` field points to the source attempt. Completed step rows selected
for reuse are copied into that descendant and marked cached; the original
attempt remains immutable. Their outputs and memory are restored without
re-firing side effects.

By default Apiary uses the current workflow definition. Every new instance also
stores a definition snapshot, allowing `--definition original` to replay the exact
definition used by the selected attempt. `--from` reuses passed steps declared
before the selected step and reruns that step plus subsequent steps. Split steps
are always re-evaluated so branch activation reflects the restored memory.

Workflow- and step-level environment overlays are not persisted in snapshots,
because expanded values may contain secrets. Original-definition replay resolves
those overlays from the current workflow configuration.

Use `apiary instances compare <before> <after>` to compare step states, prompts,
outputs, model/runner selection, token usage, cost, and timing between attempts.
Steps marked `idempotent: true` remain the operator's signal that rerunning their
external effects is safe.

A complete annotated pipeline using most of this page lives at
[`.apiary/example-workflow.yaml`](https://github.com/orlandoburli/apiary/blob/main/.apiary/example-workflow.yaml).
