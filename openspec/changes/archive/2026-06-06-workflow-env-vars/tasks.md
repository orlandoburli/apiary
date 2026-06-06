# Tasks: Per-scope environment variables

## 1. Config schema (Go structs)

- [x] 1.1 Add `Env map[string]string \`yaml:"env,omitempty"\`` to `AgentConfig` in `internal/config/config.go`.
- [x] 1.2 Add `Env map[string]string \`yaml:"env,omitempty"\`` to `WorkflowConfig` in `internal/config/workflow.go`.
- [x] 1.3 Add `Env map[string]string \`yaml:"env,omitempty"\`` to `StepConfig` in `internal/config/workflow.go` (cross-cutting section, near `Publish`/`Spawn`).
- [x] 1.4 Confirm the config loader's existing `${VAR}`/`${{ }}` expansion covers map values (add a loader test if not already covered).

## 2. Engine threading (workflow scope → executor)

- [x] 2.1 Add `WorkflowEnv map[string]string` to `StepRequest` in `internal/workflow/engine.go`.
- [x] 2.2 Add a `wfEnv map[string]string` parameter to `runStep`; set `WorkflowEnv: wfEnv` when building `StepRequest`.
- [x] 2.3 `dag.go`: pass `r.wf.Env` to `runStep` at the direct call site (L~204).
- [x] 2.4 `dag_parallel.go`: thread a `wfEnv` parameter through `runParallelStep` to its `runStep` call.
- [x] 2.5 `foreach.go`: thread `wfEnv` through `executeForeachStep` / `executeForeachSequential` / `executeForeachConcurrent` to their `runStep` calls.
- [x] 2.6 Update all dispatch points in `dag.go` (where these helpers are invoked) to pass `r.wf.Env`.

## 3. Env composition (daemon executor)

- [x] 3.1 In `internal/daemon/workflow.go`, add `stepEnv(agent, wfEnv, stepEnv map[string]string) map[string]string` that layers `agentIdentityEnv(agent)` ← `agent.Env` ← `wfEnv` ← `stepEnv` (later wins).
- [x] 3.2 Keep `agentIdentityEnv` unchanged as the base/identity layer.
- [x] 3.3 Change the `RunRequest` call site to `Env: stepEnv(req.Agent, req.WorkflowEnv, req.Step.Env)`.

## 4. JSON Schema (`schema/apiary.json`)

- [x] 4.1 Add an `env` property (`{"type":"object","additionalProperties":{"type":"string"}}`) to the agent items object (after `source_name`).
- [x] 4.2 Add the same `env` property to the workflow items object.
- [x] 4.3 Add the same `env` property to the `StepConfig` definition.
- [x] 4.4 Re-run the schema validation / pre-commit hook against a config exercising all three scopes; ensure it passes (`additionalProperties: false` would otherwise reject it).

## 5. Tests

- [x] 5.1 `internal/daemon`: table-driven `stepEnv` tests — agent-only; workflow-overrides-agent; full step>workflow>agent precedence; explicit step `GITHUB_TOKEN` overrides `source_token` overlay; empty-everywhere == `agentIdentityEnv` (regression).
- [x] 5.2 Engine test (in the `workflow_executor_test.go` style): a step with workflow + step env reaches the runner's `RunRequest.Env` merged correctly (assert via a recording `fakeRunner`).
- [x] 5.3 `cd src && go build ./... && go test ./internal/daemon/... ./internal/workflow/... ./internal/config/...` all green.

## 6. Docs

- [x] 6.1 Document the three `env` scopes and the STEP > WORKFLOW > AGENT precedence (and the identity base layer) in the apiary guide / config reference under `docs/`.
- [x] 6.2 Note the deliberate-override escape hatch for `GITHUB_TOKEN` and that values pass through `${ENV}` expansion.

## 7. Changelog & archive

- [x] 7.1 Entry added to `openspec/CHANGELOG.md` under `## Ativas` (done at proposal time).
- [x] 7.2 On completion, move the entry to `## Arquivadas` under the merge date.
