# Tasks: Per-scope environment variables

## 1. Config schema (Go structs)

- [ ] 1.1 Add `Env map[string]string \`yaml:"env,omitempty"\`` to `AgentConfig` in `internal/config/config.go`.
- [ ] 1.2 Add `Env map[string]string \`yaml:"env,omitempty"\`` to `WorkflowConfig` in `internal/config/workflow.go`.
- [ ] 1.3 Add `Env map[string]string \`yaml:"env,omitempty"\`` to `StepConfig` in `internal/config/workflow.go` (cross-cutting section, near `Publish`/`Spawn`).
- [ ] 1.4 Confirm the config loader's existing `${VAR}`/`${{ }}` expansion covers map values (add a loader test if not already covered).

## 2. Engine threading (workflow scope → executor)

- [ ] 2.1 Add `WorkflowEnv map[string]string` to `StepRequest` in `internal/workflow/engine.go`.
- [ ] 2.2 Add a `wfEnv map[string]string` parameter to `runStep`; set `WorkflowEnv: wfEnv` when building `StepRequest`.
- [ ] 2.3 `dag.go`: pass `r.wf.Env` to `runStep` at the direct call site (L~204).
- [ ] 2.4 `dag_parallel.go`: thread a `wfEnv` parameter through `runParallelStep` to its `runStep` call.
- [ ] 2.5 `foreach.go`: thread `wfEnv` through `executeForeachStep` / `executeForeachSequential` / `executeForeachConcurrent` to their `runStep` calls.
- [ ] 2.6 Update all dispatch points in `dag.go` (where these helpers are invoked) to pass `r.wf.Env`.

## 3. Env composition (daemon executor)

- [ ] 3.1 In `internal/daemon/workflow.go`, add `stepEnv(agent, wfEnv, stepEnv map[string]string) map[string]string` that layers `agentIdentityEnv(agent)` ← `agent.Env` ← `wfEnv` ← `stepEnv` (later wins).
- [ ] 3.2 Keep `agentIdentityEnv` unchanged as the base/identity layer.
- [ ] 3.3 Change the `RunRequest` call site to `Env: stepEnv(req.Agent, req.WorkflowEnv, req.Step.Env)`.

## 4. JSON Schema (`schema/apiary.json`)

- [ ] 4.1 Add an `env` property (`{"type":"object","additionalProperties":{"type":"string"}}`) to the agent items object (after `source_name`).
- [ ] 4.2 Add the same `env` property to the workflow items object.
- [ ] 4.3 Add the same `env` property to the `StepConfig` definition.
- [ ] 4.4 Re-run the schema validation / pre-commit hook against a config exercising all three scopes; ensure it passes (`additionalProperties: false` would otherwise reject it).

## 5. Tests

- [ ] 5.1 `internal/daemon`: table-driven `stepEnv` tests — agent-only; workflow-overrides-agent; full step>workflow>agent precedence; explicit step `GITHUB_TOKEN` overrides `source_token` overlay; empty-everywhere == `agentIdentityEnv` (regression).
- [ ] 5.2 Engine test (in the `workflow_executor_test.go` style): a step with workflow + step env reaches the runner's `RunRequest.Env` merged correctly (assert via a recording `fakeRunner`).
- [ ] 5.3 `cd src && go build ./... && go test ./internal/daemon/... ./internal/workflow/... ./internal/config/...` all green.

## 6. Docs

- [ ] 6.1 Document the three `env` scopes and the STEP > WORKFLOW > AGENT precedence (and the identity base layer) in the apiary guide / config reference under `docs/`.
- [ ] 6.2 Note the deliberate-override escape hatch for `GITHUB_TOKEN` and that values pass through `${ENV}` expansion.

## 7. Changelog & archive

- [ ] 7.1 Entry added to `openspec/CHANGELOG.md` under `## Ativas` (done at proposal time).
- [ ] 7.2 On completion, move the entry to `## Arquivadas` under the merge date.
