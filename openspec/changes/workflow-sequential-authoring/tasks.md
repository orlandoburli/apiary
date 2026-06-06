# Tasks — workflow-sequential-authoring

> Design only. Nothing implemented yet; this is the proposed build order.

## Engine (two small additions)
- [ ] `fail_when` on the agent step: in `workflow/dag.go` `runAgentDAGStep`, derive
      `failed` from `!res.Success || eval(fail_when over this step's output)`.
- [ ] Step-level `condition` (per-step `if:`): mark a step skipped when false in
      scheduling (`pickRunnable`/`markSkipped`).
- [ ] Eval context helper exposing the current step's fresh structured output
      (transient memory view) — reuse `EvalContext`/`ParseExpr` (`workflow/expr.go`).
- [ ] Honor `on_missing_output: fail` together with `reject_when`.

## Authoring layer (nested tree + lowering)
- [ ] v2 parse: **nested step tree** — leaf (`agent:`), group (`steps:`), parallel
      (`parallel:` + `join`), loop (`for_each:`+`as:`+`steps:`); per-step/group
      `if:`, `name:`, `output:`, `${{ … }}` expressions (`config` package).
- [ ] Lowering pass tree → `[]StepConfig`: nesting/order→`depends_on`,
      group→sub-chain (or anon `type: workflow`), `parallel`+`join`→concurrent
      children + computed outcome, `for_each`→`type: foreach` (anon sub-workflow
      body if >1 step), `if`→`condition`, `on_reject`→`on_fail.goto`,
      `reject_when`→`fail_when`, auto-wire `steps.x.outputs.y` refs into `memory.write`.

## Validation
- [ ] Parse-check `if`/`reject_when`/`join` expressions; `restart_from` must be an
      earlier sibling; `step.field` must be declared in that step's `output`.
- [ ] Reject ambiguous mix of authored nesting and hand-written `goto` in one flow.
- [ ] Warn on `reject_when` without `on_reject`.

## Examples, souls, docs
- [ ] Rewrite project-erp `triage` in v2 (implement + complex tracks).
- [ ] Souls: engineer (implement + open PR; read PR comments on retry), reviewer
      (verdict + PR review comments), qa (verdict + findings), staff (doc + sub-issues).
- [ ] Update `.apiary/example-workflow.yaml` to v2; document `split` as low-level.

## Tests
- [ ] Lowering tests: v2 tree → expected `[]StepConfig` (golden).
- [ ] Engine tests for `fail_when` pass/reject/loop-back/retry-exhaustion.
- [ ] End-to-end: classify→implement→review(reject→loop)→qa→done.

## Composition (nested sub-steps / loops / parallel)
- [ ] Nested `steps:` group (inline sub-workflow) — sub-chain or anon `type: workflow`.
- [ ] `for_each:`+`as:`+nested `steps:` body → `type: foreach`; multi-step body via
      anon sub-workflow (or widen `foreach.step` to a list — decide at impl).
- [ ] `parallel:` step with `join` policy (`all`/`any`/`${{ expr }}`); the parallel
      step's outcome = the join; successor depends on the parallel step.

## Concurrency (in scope — §8e)
- [ ] Concurrent scheduler: `pickAllRunnable()`; single scheduler goroutine owns
      `dagRun` state; worker goroutines run agents and return results on a channel;
      re-dispatch newly-unblocked steps until quiescent.
- [ ] Global agent semaphore sized by `settings.concurrency` around every runner
      invocation (plus per-agent `max_workers` as secondary cap).
- [ ] Deterministic memory ordering: `passedOrder` by declaration order, not
      completion order.
- [ ] Loop-back with in-flight siblings: drain then `resetLoop`; approval steps
      quiesce before parking.
- [ ] Honor `for_each.concurrency` via the same semaphore.
- [ ] Verify `concurrency: 1` reproduces today's sequential behaviour (regression
      guard — all existing engine tests pass unchanged).

## Follow-up (separate change)
- [ ] Decide fate of `settings.retry_policy` (inert today).
