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

## Authoring layer (lowering)
- [ ] v2 parse: flat `steps:` with per-step `if:`, `name:`, `output:`,
      `${{ … }}` expressions, implicit sequencing (`config` package).
- [ ] Lowering pass v2 → `[]StepConfig` (sequence→`depends_on`, `if`→`condition`,
      `on_reject`→`on_fail.goto`, `reject_when`→`fail_when`, auto-wire
      `steps.x.outputs.y` refs into `memory.write`).

## Validation
- [ ] Parse-check `if`/`reject_when` expressions; `restart_from` must be an earlier
      step; `step.field` must be declared in that step's `output`.
- [ ] Reject ambiguous mix of `if:` blocks and manual `goto` in one flow.
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

## Composition (loops / sub-workflows / parallel)
- [ ] `uses:` alias for sub-workflow steps (engine already supports `type: workflow`).
- [ ] `for_each:`/`as:`/`max:` authored aliases → existing `type: foreach`
      (`items`/`as`/`max_items`/`step`); loop already runs (serial).
- [ ] Accept `parallel:` block in the parser (lowers to independent steps + join);
      execute sequentially for now, clearly flagged as not-yet-concurrent.

## Follow-up (separate changes)
- [ ] **Concurrent scheduler + global `settings.concurrency` semaphore** — makes
      `parallel:` truly parallel and `for_each.concurrency` real (the original
      `concurrency-model` spec). Biggest lift; its own change after v2 + gates land.
- [ ] Honor `for_each.concurrency` (bounded goroutines over items) on top of it.
- [ ] Decide fate of `settings.retry_policy` (inert today).
