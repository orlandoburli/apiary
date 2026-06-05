# Tasks — workflow-sequential-authoring

> Design only. Nothing implemented yet; this is the proposed build order.

## Engine (minimal)
- [ ] `fail_when` on the agent step: in `workflow/dag.go` `runAgentDAGStep`, derive
      `failed` from `!res.Success || eval(fail_when over this step's output)`.
- [ ] Eval context helper exposing the current step's fresh structured output
      (transient memory view) — reuse `EvalContext`/`ParseExpr` (`workflow/expr.go`).
- [ ] Honor `on_missing_output: fail` together with `reject_when`.

## Authoring layer (lowering)
- [ ] v2 parse: `if/then/else` blocks + implicit sequencing in `config` package.
- [ ] Lowering pass v2 tree → `[]StepConfig` (sequence→`depends_on`,
      `if`→`split`+`goto`, `on_reject`→`on_fail.goto`, `reject_when`→`fail_when`,
      auto-wire `step.field` refs into `memory.write`).
- [ ] Resolve join semantics after an `if` block (open question §7).

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

## Follow-up (separate change)
- [ ] Decide fate of `settings.retry_policy` (inert today).
