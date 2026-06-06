# Design: Per-scope environment variables

## Scope of the change

Three config structs gain an `Env map[string]string` field; the engine threads
the workflow-scope env to the step executor; the executor merges
agent + workflow + step env (in precedence order) on top of the existing identity
overlay and assigns the result to `RunRequest.Env`. The runner already overlays
`RunRequest.Env` onto `os.Environ()`, so no runner change is needed.

## Current state (what exists today)

- `internal/runner/execution/cli.go` (~L81-84): `cmd.Env = os.Environ()` then
  `for k,v := range req.Env { append k=v }`. **`req.Env` already overrides the
  inherited environment.** This is the only place env reaches the process.
- `internal/daemon/workflow.go`:
  - `ExecuteStep` builds the `RunRequest` with `Env: agentIdentityEnv(req.Agent)`
    (~L157).
  - `agentIdentityEnv(agent)` returns git identity + (if `SourceToken != ""`)
    `GITHUB_TOKEN`/`GH_TOKEN`.
- `internal/workflow/StepRequest` carries `Step config.StepConfig` and
  `Agent config.AgentConfig` but **not** the workflow config.
- `internal/workflow/engine.go runStep` builds `StepRequest`. It does not receive
  the `WorkflowConfig`; the running `*dagRun` (`dag.go`) holds `wf`. `runStep` is
  called from `dag.go` (has `r`), `dag_parallel.go`, and `foreach.go` (have only
  `instID`).
- `schema/apiary.json` validates the config with `additionalProperties: false` on
  the agent items, workflow items, and the `StepConfig` definition — so new keys
  must be added there or validation fails. A worker-scope `env` shape already
  exists (`{ "type":"object", "additionalProperties": {"type":"string"} }`) and is
  reused verbatim.

## Design decisions

### 1. Where the merge happens — the daemon executor, not the engine

The merge lives in `internal/daemon/workflow.go`, where the `RunRequest` is
already assembled and where `agentIdentityEnv` already lives. This keeps the
workflow engine free of process-environment concerns (it stays a pure
orchestrator) and keeps all env logic in one file with one test target.

Rename/extend `agentIdentityEnv` into a single builder:

```go
// stepEnv composes the environment for one agent step, lowest precedence first:
//   identity overlay (git + source_token→GITHUB_TOKEN/GH_TOKEN)
//     ← agent.env ← workflow.env ← step.env
func stepEnv(agent config.AgentConfig, wfEnv, stepEnv map[string]string) map[string]string {
    env := agentIdentityEnv(agent) // base: git identity + token (existing fn, kept)
    for k, v := range agent.Env    { env[k] = v }
    for k, v := range wfEnv        { env[k] = v }
    for k, v := range stepEnv      { env[k] = v }
    return env
}
```

`agentIdentityEnv` is kept as-is (it already has tests from #1948) and becomes the
base layer; `stepEnv` is the new public-to-the-package composer used at the call
site.

### 2. Threading workflow env to the executor — add it to `StepRequest`

`StepRequest` gains one field:

```go
type StepRequest struct {
    ...
    Step        config.StepConfig
    Agent       config.AgentConfig
    WorkflowEnv map[string]string // workflow-scope env (engine fills from wf.Env)
    ...
}
```

`runStep` gains a `wfEnv map[string]string` parameter and sets
`WorkflowEnv: wfEnv` when building `StepRequest`. Its three call sites pass the
running workflow's env:

- `dag.go` (L204): `r.wf.Env` is in scope — pass it directly.
- `dag_parallel.go` and `foreach.go`: these helpers receive only `instID`, not the
  `*dagRun`. Two options:
  - **(chosen)** add a `wfEnv map[string]string` parameter to
    `runParallelStep` / `executeForeachStep` / `executeForeachSequential` /
    `executeForeachConcurrent`, threaded from the `*dagRun` at the dispatch point
    in `dag.go` (same place that already has `r`). Mechanical, explicit, no new
    engine state.
  - (rejected) a `instID → *dagRun` registry lookup — adds shared mutable state
    for no benefit here.

The call site in `ExecuteStep` becomes:

```go
Env: stepEnv(req.Agent, req.WorkflowEnv, req.Step.Env),
```

### 3. Precedence rationale

STEP > WORKFLOW > AGENT matches "most specific wins": the step is the narrowest
scope, the agent the broadest. The identity overlay sits below all three so the
`source_token` default still holds, but a deliberate `env: { GITHUB_TOKEN: ... }`
at any explicit scope can override it (escape hatch; documented, not encouraged).

### 4. Empty maps and nil-safety

All `env` fields are optional (`omitempty`). `stepEnv` starts from
`agentIdentityEnv` (always non-nil) and ranges over possibly-nil maps (safe in
Go). No key validation beyond what the runner already does — values are arbitrary
strings, consistent with the legacy worker `env`.

### 5. Config loading / `${ENV}` expansion

The config loader already expands `${VAR}` / `${{ }}` in string values during
load (see the `config-lint-removed-directives` and "preserva `${{ }}`" work).
`env` map values are plain strings and are subject to the same existing expansion
— no new interpolation code. This lets operators write
`env: { DEPLOY_URL: "${DEPLOY_URL}" }` to forward a daemon var explicitly, or a
literal.

## Files touched

| File | Change |
|---|---|
| `internal/config/config.go` | `AgentConfig.Env map[string]string` (`yaml:"env,omitempty"`) |
| `internal/config/workflow.go` | `WorkflowConfig.Env` and `StepConfig.Env` |
| `internal/workflow/engine.go` | `StepRequest.WorkflowEnv`; `runStep` `wfEnv` param + set field |
| `internal/workflow/dag.go` | pass `r.wf.Env` to `runStep` / fan-out helpers |
| `internal/workflow/dag_parallel.go` | thread `wfEnv` param to `runStep` |
| `internal/workflow/foreach.go` | thread `wfEnv` param to `runStep` |
| `internal/daemon/workflow.go` | new `stepEnv` composer; call site uses it |
| `schema/apiary.json` | add `env` to agent items, workflow items, `StepConfig` def |
| `docs/` | document the three scopes + precedence (apiary-guide / config ref) |

## Testing strategy

Unit tests in `internal/daemon` (table-driven, alongside the existing
`workflow_identity_test.go`):

- agent-only env present.
- workflow overrides agent for a shared key; agent-only keys survive.
- step overrides workflow which overrides agent (full 3-way precedence on one key).
- explicit `env.GITHUB_TOKEN` at step scope overrides the `source_token` overlay.
- no env anywhere → identical to today's `agentIdentityEnv` output (regression).

Engine-level: extend an existing `workflow_executor_test.go`-style test to assert
the `fakeRunner` receives the merged `Env` (the runner records `RunRequest.Env`),
proving `WorkflowEnv` is threaded through `runStep` for a plain step. A foreach /
parallel threading check can be a follow-up if cheap.

Schema: the repo's pre-commit / JSON-Schema validation (added in #90) must still
accept a config that uses all three `env` scopes; add such a block to whatever
example/fixture the schema check runs against.

## Risks

- **Low.** Additive, optional fields; default behaviour byte-identical to today
  (the merge over an empty set of explicit envs == `agentIdentityEnv`).
- The only non-mechanical part is threading `wfEnv` through the foreach/parallel
  helpers; covered by build + existing engine tests.
- `additionalProperties: false` in the JSON schema means forgetting a schema edit
  turns a valid config into a validation error — the schema task is mandatory, not
  optional.
