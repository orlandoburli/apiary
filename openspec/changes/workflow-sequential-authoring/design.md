# Design — Workflow authoring v2

> Design only. Describes the authoring surface, how it lowers to the current DAG
> engine, and the one new engine capability (`fail_when`).

## 1. Authoring model

A workflow's `steps` is an **ordered list**. Two kinds of entries:

### a) Agent step
```yaml
- id: review                # optional; auto-generated if omitted, required if referenced
  agent: reviewer
  prompt: "..."             # optional per-step instruction
  output: { verdict: { enum: [approved, rejected] } }   # structured-output schema
  reject_when: 'review.verdict == "rejected"'           # optional gate
  on_reject: { restart_from: implement, max: 3 }        # optional loop-back
```

### b) Conditional block
```yaml
- if: '<expr>'
  then: [ <steps...> ]      # ordered list of steps
  else: [ <steps...> ]      # optional
```

`if` blocks nest arbitrarily. No `goto`, no labels.

### Rules
- **Implicit sequencing:** within any list, step *N* runs only after step *N-1*
  completes (passed). Declaration order = execution order.
- **No `depends_on` in v2.** (Kept only as the lowered form / advanced escape
  hatch — see §6 compatibility.)
- **Output references:** `&lt;step_id&gt;.&lt;field&gt;` resolves to that step's structured
  output. Available to any later step's `if` / `reject_when`. No manual
  `memory.write` — declaring `output:` makes the fields addressable.
- A branch with no further steps just ends (e.g. the `complex` track is
  `staff` then End).

## 2. Approval / reject gates

```yaml
- id: review
  agent: reviewer
  output: { verdict: { enum: [approved, rejected] } }
  reject_when: 'review.verdict == "rejected"'
  on_reject: { restart_from: implement, max: 3 }
```

Semantics:
- After the agent runs, evaluate `reject_when` against the step's **own** fresh
  structured output. True → the step is **rejected** (a logical failure, distinct
  from a crash).
- `on_reject.restart_from: <earlier step id>` → re-run from that step (and
  everything after it), up to `max` times.
- No `on_reject` + rejected → the workflow fails → `on_fail` (e.g.
  `add_labels: [needs-attention]`).
- Retries exhausted → workflow fails → `on_fail`. (If the engineer can't satisfy
  the reviewer in `max` rounds, escalate to a human.)
- A genuine crash (agent exits non-zero) is still a failure independent of
  `reject_when`.

## 3. New engine capability: `fail_when` (the only executor change)

Today `Success = (cli exit == 0)` (`runner/execution/cli.go:197`); structured
output does not affect it. We add a way to derive a step's outcome from its output:

- New optional field on the lowered agent step: `fail_when string` (expression).
- In `runAgentDAGStep` (`workflow/dag.go`), after `runStep` returns `res`:
  - Build a **transient** eval context = current memory (`r.memoryValues()`) plus
    *this* step's just-produced structured output (it isn't "passed" yet, so it
    isn't in memory). Reuse `EvalContext` + `ParseExpr`/`Eval` (`workflow/expr.go`).
  - `rejected := step.FailWhen != "" && eval(step.FailWhen)`.
  - `failed := !res.Success || rejected`.
  - Then take the existing pass/fail branches unchanged — on fail, the current
    `on_fail.goto` + `max_retries` + `resetLoop` machinery handles the loop-back.
- Reuse existing `on_missing_output: fail` so a reviewer that forgets to emit
  `verdict` escalates instead of silently passing (missing key → `reject_when`
  would be false → would otherwise proceed).

That is the entire engine delta. Everything else is authoring-time lowering.

## 4. Lowering v2 → current DAG `StepConfig`

The parser compiles the v2 tree into today's flat `[]StepConfig` that the engine
already runs:

| v2 construct                  | Lowered form |
|-------------------------------|--------------|
| step *N* after step *N-1*     | `depends_on: [<id of N-1>]` |
| `if/then/else`                | a `split` step (`type: split`) whose `branches` are `{if: <expr>, goto: <first step of then>}` and `{else: true, goto: <first step of else>}`; the last step before the block depends on nothing extra; block steps chain internally; steps **after** the block depend on the block's join |
| `reject_when`                 | `fail_when` on the lowered agent step |
| `on_reject: {restart_from, max}` | `on_fail: {goto: <restart_from>, max_retries: max}` |
| `step.field` reference        | `memory.field` is auto-wired: the parser adds the referenced fields to that step's `memory.write`, so the existing eval context resolves them |

Join handling after an `if` block: steps following the block depend on whichever
branch ran. The current engine already models this — unchosen branches are skipped
and skip-cascades downstream (`skipUnreachable`/`markSkipped`), so a post-block
step `depends_on` the branch tips with "any passed" join semantics. (Open question
§7 — confirm the join rule or insert a synthetic no-op join step.)

Because lowering targets the existing structures, **all current engine tests keep
passing**; v2 is validated by asserting the lowered DAG equals a hand-written one.

## 5. Reject feedback survives the loop (via the PR, not memory)

`resetLoop(target)` resets the target and its transitive dependents to pending and
**deletes their memory contributions**. So when `review` rejects and restarts from
`implement`, the reviewer's contribution is wiped — the engineer would not see the
feedback through workflow memory.

Resolution: rejection feedback lives on the **PR itself** (the reviewer posts a
real GitHub review / comments; QA posts findings). On restart, the engineer re-reads
the PR thread. Workflow memory only carries the gate verdict. This matches how human
teams work and sidesteps the memory-reset entirely. Souls are written accordingly
(reviewer/QA comment on the PR; engineer reads PR comments before redoing).

## 6. Backward compatibility

- `depends_on`, `split`/`branches`/`goto` remain valid — they are exactly the
  lowered form. Existing configs run unchanged.
- v2 is additive sugar. A workflow may not mix `if:` blocks and manual `goto` for
  the same flow (validation rejects the ambiguous mix).
- Docs/examples move to v2 as the recommended style; `split` documented as the
  low-level form.

## 7. Validation & open questions

Validation (extends `config/workflow_validate.go`):
- `reject_when`/`if` expressions must parse (`ParseExpr`).
- `on_reject.restart_from` must reference an earlier step in the same sequence.
- referenced `&lt;step&gt;.&lt;field&gt;` must be declared in that step's `output:`.
- `reject_when` without `on_reject` → warning (a reject will fail the whole flow).

Open questions for review:
1. **Join semantics** after an `if` block: rely on skip-cascade "any-passed" join,
   or always insert a synthetic join step? (Affects steps placed after a block.)
2. **Keep or drop `depends_on`** in the authored surface (parallel/diamond flows
   are the only thing pure sequencing can't express). Keep as advanced, or add an
   explicit `parallel:` block later?
3. **Reference syntax:** `review.verdict` (step-scoped, proposed) vs keep
   `memory.verdict`. Step-scoped is clearer but needs the auto-wiring in §4.
4. **`output` shorthand** vs current `output_schema`: adopt the short `output:` key
   or keep `output_schema:`?
