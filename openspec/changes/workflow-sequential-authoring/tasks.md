# Tasks — workflow-sequential-authoring

> Implementation complete (PRs #46–#50).

## Engine (two small additions)
- [x] `fail_when` on the agent step: in `workflow/dag.go` `runAgentDAGStep`, derive
      `failed` from `!res.Success || eval(fail_when over this step's output)`.
- [x] Step-level `condition` (per-step `if:`): mark a step skipped when false in
      scheduling (`pickRunnable`/`markSkipped`).
- [x] Eval context helper exposing the current step's fresh structured output
      (transient memory view) — reuse `EvalContext`/`ParseExpr` (`workflow/expr.go`).
- [x] Honor `on_missing_output: fail` together with `reject_when`.

## Authoring layer (nested tree + lowering)
- [x] v2 parse: **nested step tree** — leaf (`agent:`), group (`steps:`), parallel
      (`parallel:` + `join`), loop (`for_each:`+`as:`+`steps:`); per-step/group
      `if:`, `name:`, `output:`, `${{ … }}` expressions (`config` package).
- [x] Lowering pass tree → `[]StepConfig`: nesting/order→`depends_on`,
      group→sub-chain (or anon `type: workflow`), `parallel`+`join`→concurrent
      children + computed outcome, `for_each`→`type: foreach` (anon sub-workflow
      body if >1 step), `if`→`condition`, `on_reject`→`on_fail.goto`,
      `reject_when`→`fail_when`, auto-wire `steps.x.outputs.y` refs into `memory.write`.

## Validation
- [x] Parse-check `if`/`reject_when`/`join` expressions; `restart_from` must be an
      earlier sibling; `step.field` must be declared in that step's `output`.
- [x] Reject ambiguous mix of authored nesting and hand-written `goto` in one flow.
- [x] Warn on `reject_when` without `on_reject`.

## Examples, souls, docs
- [ ] Rewrite project-erp `triage` in v2 (implement + complex tracks). _(follow-up)_
- [ ] Souls: engineer (implement + open PR; read PR comments on retry), reviewer
      (verdict + PR review comments), qa (verdict + findings), staff (doc + sub-issues). _(follow-up)_
- [x] Update `.apiary/example-workflow.yaml` to v2; document `split` as low-level.

## Tests
- [x] Lowering tests: v2 tree → expected `[]StepConfig` (golden).
- [x] Engine tests for `fail_when` pass/reject/loop-back/retry-exhaustion.
- [x] End-to-end: classify→implement→review(reject→loop)→qa→done.

## Composition (nested sub-steps / loops / parallel)
- [x] Nested `steps:` group (inline sub-workflow) — sub-chain or anon `type: workflow`.
- [x] `for_each:`+`as:`+nested `steps:` body → `type: foreach`; multi-step body via
      anon sub-workflow (or widen `foreach.step` to a list — decide at impl).
- [x] `parallel:` step with `join` policy (`all`/`any`/`${{ expr }}`); the parallel
      step's outcome = the join; successor depends on the parallel step.

## Concurrency (in scope — §8e)
- [x] Concurrent scheduler: `pickAllRunnable()`; single scheduler goroutine owns
      `dagRun` state; worker goroutines run agents and return results on a channel;
      re-dispatch newly-unblocked steps until quiescent.
- [x] Global agent semaphore sized by `settings.concurrency` around every runner
      invocation (plus per-agent `max_workers` as secondary cap).
- [x] Deterministic memory ordering: `passedOrder` by declaration order, not
      completion order.
- [x] Loop-back with in-flight siblings: drain then `resetLoop`; approval steps
      quiesce before parking.
- [ ] Honor `for_each.concurrency` via the same semaphore. _(follow-up: foreach still sequential per item)_
- [x] Verify `concurrency: 1` reproduces today's sequential behaviour (regression
      guard — all existing engine tests pass unchanged).

## Follow-up (separate change)
- [ ] Decide fate of `settings.retry_policy` (inert today).
- [ ] Implement `for_each.concurrency` cap via the global semaphore.
- [ ] Conditional step skip should not block implicit successor steps (v2 seq edge case).
