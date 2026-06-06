# Proposal: Per-scope environment variables (agent / workflow / step)

## Why

Agent subprocesses inherit the daemon's environment (`os.Environ()`) plus a small,
hard-coded overlay built by `agentIdentityEnv` in `internal/daemon/workflow.go`:
the git author/committer identity and — as of the
`orlandoburli-enterprise/project-erp#1948` fix — the agent's `source_token`
exported as `GITHUB_TOKEN`/`GH_TOKEN`.

There is **no way for an operator to pass arbitrary environment variables** to an
agent run. Real configurations need this routinely:

- A step that runs a deploy or a smoke test needs a target URL, a registry
  credential, or a feature flag that only that step should see.
- A whole workflow (e.g. a release pipeline) needs a common set of vars across
  all its steps.
- An agent needs a standing variable regardless of which workflow invokes it
  (e.g. a tool API key tied to that agent's identity).

The legacy, now-superseded `worker` model already had `config.env`
(`WorkerRunConfig.Env`), but the active agent/workflow/step model dropped it.
This change restores that capability in the right place and makes it
**composable across the three scopes** an agent run actually has.

## What Changes

Add an optional `env: { KEY: VALUE }` map at three config scopes and merge them,
per step, with a well-defined precedence before handing the result to the runner:

| Scope | Field | Applies to |
|---|---|---|
| **Agent** | `agents[].env` | every step that runs this agent, in any workflow |
| **Workflow** | `workflows[].env` | every step of this workflow |
| **Step** | `workflows[].steps[].env` | only that step |

**Precedence (highest wins): STEP → WORKFLOW → AGENT.** A step `env` value
overrides the same key set at workflow scope, which overrides the same key at
agent scope.

The existing automatic identity overlay (git identity + `source_token` →
`GITHUB_TOKEN`/`GH_TOKEN`) is the **base layer**, below all three explicit
scopes — so an operator can still deliberately override `GITHUB_TOKEN` for a
single step if they ever need to, but by default the agent's own token wins as it
does today.

Final layering applied to the subprocess (later overrides earlier):

```
os.Environ()                          (daemon-inherited; in the runner)
  └─ identity overlay (git + source_token → GITHUB_TOKEN/GH_TOKEN)
       └─ agent.env
            └─ workflow.env
                 └─ step.env
```

## What Stays

- **Runner contract** — `RunRequest.Env` and the `cli.go` overlay loop are
  unchanged; this change only enriches the map passed in `Env`.
- **Identity behaviour** — git identity and the `source_token` → token mapping
  keep working exactly as today when no explicit `env` is set.
- **Engine DAG / approvals / foreach / parallel** — unchanged execution
  semantics; the only addition is threading the workflow's `env` to the step
  executor.
- **No env interpolation semantics change** — values are passed through verbatim,
  consistent with how `${{ }}`/`${ }` expansion already works elsewhere in config
  loading (env vars in values are expanded by the existing config loader before
  these maps are read).

## Out of Scope

- Secret management / vaulting. Values come from the config (already subject to
  the loader's `${ENV}` expansion); this change does not add a secrets backend.
- Per-`foreach`-item env. Item-specific values belong in the prompt/templating
  layer, not the process environment.
- Source-adapter (write-back) credentials. Those continue to flow through
  `source.SourceTokenCtxKey`, unchanged.
