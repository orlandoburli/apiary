# Proposal: Self-Improvement Advisor (`apiary improve`)

## Why

Apiary already records everything needed to judge how well its own agents and workflows
perform, and then throws that judgement away. Every step run persists timing, token and
cache breakdown, turns, tool calls, cost, exit state and the full composed prompt
(`step_runs`, `task_executions`); every failover records a `failure_kind`; every CI wait
records each poll; every session writes a markdown transcript. Today that data only
answers "what happened to this task" in the dashboard. Nobody asks the cross-cutting
question:

> Across the last 200 runs, which steps are wasting money, which agent instructions keep
> producing rework, and what should I change in `apiary.yaml` and the soul files?

That question is answered manually, if at all — reading transcripts one by one. The
result is that configuration drifts: `max_turns` caps set once and never revisited, a
review step that fails 40% of the time and always passes on retry, an agent whose soul
file no longer matches the workflow around it, a fallback chain never exercised, steps
that run sequentially but have no data dependency.

The gap is that **Apiary has no feedback loop from its own execution history back into
its own configuration.** This change adds one: a standalone command that mines the
execution history, has an agent reason over it, and emits either a reviewable diff or an
applied change to the config and soul files — plus the bookkeeping to tell, on the next
run, whether the last set of changes actually helped.

---

## What Changes

| Area | Before | After |
|---|---|---|
| **Execution stats** | Per-task/per-instance views in the dashboard | Cross-run aggregation by workflow, step, agent, runner and model |
| **Config tuning** | Manual, from memory | Evidence-backed recommendations tied to specific metrics |
| **Soul files** | Hand-edited, never reviewed against outcomes | Reviewed against the transcripts of the runs they produced |
| **Effort** | n/a | `--effort quick\|standard\|deep`, trading tokens/time for depth |
| **Delivery** | n/a | Markdown report + unified diff (default) or direct apply behind `--apply` |
| **Feedback** | None | Applied recommendations are recorded and re-scored on the next run (before/after) |
| **Runtime** | n/a | Standalone one-shot command; does not require the daemon to be running |

The advisor reads and may propose changes to the **whole agent-configuration workspace**:
`apiary.yaml`, referenced workflow files, every soul file, every skill definition the
agents declare, and the generated agent prompt files. Version control is the user's
safety net — the command assumes the workspace is a git repository and does not try to
reimplement undo.

Explicitly **not** in scope: an autonomous loop that rewrites config unattended, changes
to Go source, and changes to secrets — `.env`, tokens, and any secret-bearing config value.

---

## New Concepts

| Concept | Description |
|---|---|
| **Evidence pack** | A deterministic, LLM-free bundle of aggregated metrics + selected transcript excerpts, built in Go from the SQLite database. The single input to the advisor agent. Reproducible and inspectable (`--dump-evidence`). |
| **Finding** | An observation with a metric behind it: scope (workflow/step/agent), symptom, the numbers that support it, and severity. Findings exist without recommendations. |
| **Recommendation** | A proposed change addressing one or more findings: target file, rationale, confidence, expected effect, and a patch. |
| **Config workspace** | Every file that shapes agent behaviour, discovered from the config: `apiary.yaml`, referenced workflow files, soul files, skill definitions, generated agent prompt files. The advisor reads all of it and may propose edits anywhere in it, minus the exclusion list. |
| **Effort level** | How much of the history is mined, how many transcripts are read, how many agent passes run, and whether a critic pass reviews each patch. |
| **Improvement ledger** | `improvement_runs` / `improvement_findings` tables recording what was proposed, what was applied and when — so the next run can measure the delta on the same metrics. |

---

## Design

### 1. Command shape

```
apiary improve [flags]
```

```
scope of the analysis
  --since 30d                    history window (default: 14d)
  --workflow <id>                restrict analysis to one workflow (repeatable)
  --agent <id>                   restrict analysis to one agent's runs (repeatable)
  --focus cost|latency|reliability|quality|all   what to optimise for (default: all)

who performs the analysis (see §5)
  --advisor <agent-id>           agent that runs the analysis
  --runner <id> --model <name>   ad-hoc runner/model pair, no config entry needed
  --profile <name>               apply profiles.<name> overrides (global flag)

depth and delivery
  --effort quick|standard|deep   depth of analysis (default: standard)
  --output report|diff|json      what to print (default: diff, report always included)
  --apply                        write the changes to disk instead of printing a diff
                                 (workspace is assumed to be under version control)
  --out <dir>                    write report/diff/evidence to a directory
  --dump-evidence                print the evidence pack and exit (no agent call, no
                                 advisor needed)
  --yes                          skip the confirmation prompt when applying
```

The command is standalone: it opens the database read-only, builds the evidence pack,
invokes a runner directly through `runner.New(...)` (the same path the daemon uses, but
without the dispatcher, queue or workflow engine), and writes its output locally. It works
with the daemon stopped, and takes no dispatch slots when it is running.

Sub-commands:

```
apiary improve history            list past improvement runs
apiary improve show <run-id>      re-print a past run's report and diff
apiary improve effect <run-id>    before/after comparison for an applied run
```

### 2. Evidence pack — deterministic, computed in Go

No LLM is involved in producing the numbers. New package `internal/improve` with a
`metrics.go` that issues read-only aggregate queries over the existing schema.

**Per step** (`step_runs` ⋈ `workflow_instances` ⋈ `task_executions`):

- run count, pass / fail / skipped / skipped_cached rates
- duration p50 / p95 (`finished_at - started_at`)
- mean and total `total_tokens`, `cost_usd`, `num_turns`, `num_tool_calls`
- cache reuse: `cache_read_tokens / input_tokens` — a low ratio on a hot step means the
  prompt prefix churns between runs
- prompt weight: `length(input_prompt)` vs `output_tokens` — a large prompt producing a
  tiny output is a prompt-design smell
- failover rate and `failure_kind` distribution (`rate_limited` / `credit_exhausted` /
  `aborted`), from `task_executions.attempt > 1`

**Per workflow** (`workflow_instances`, `step_runs`):

- instance count by terminal state, end-to-end p50/p95 duration, cost per completed instance
- **rework loops**: the same `step_id` appearing more than once in one instance —
  the direct signature of an `on_fail`/`goto` cycle
- **step concurrency opportunity**: consecutive sequential steps where no later step's
  `if`/expression references an earlier step's output — candidates for `parallel`
- dead paths: steps and whole workflows with zero runs in the window
- wait cost: `ci_poll_checks` per wait step (poll count, terminal status distribution,
  timeouts) and approval latency from `approval_requests` / `approval_responses`

**Per agent / runner / model** (`task_executions`, `agents`):

- success rate, mean cost and duration, turn distribution
- `max_turns` saturation: how often a run ends at exactly the configured cap
- agents configured but never dispatched; fallback chains never exercised

**Failure taxonomy**: `error_message` on failed executions normalised (numbers, paths,
IDs stripped) and grouped, so "the same failure 30 times" reads as one cluster with a
count instead of 30 lines.

**Transcript sampling** (effort-dependent): for each hotspot, N transcripts are selected —
the worst failures plus one successful control — truncated to a per-step character budget.
This is the only free-text input, and it is what lets the advisor say something about
*instructions* rather than only about *numbers*.

The pack is a serialisable struct; `--dump-evidence` prints it as JSON. This makes the
whole feature testable without an LLM and lets the metrics layer ship and be trusted before
the agent layer is wired up.

### 3. Config workspace — discovery and exclusions

The second half of the input, alongside the metrics, is the configuration that produced
them. It is discovered from the config rather than hard-coded, so it stays correct as a
project grows:

| Source | Discovered from |
|---|---|
| Main config | `--config` / `resolveConfigFile()` (`apiary.yaml`, `.apiary/apiary.yaml`) |
| Workflow files | `workflows[].uses` / sub-workflow references, resolved transitively |
| Soul files | `agents[].soul_file` (already validated to exist by `cfg.Validate()`) |
| Skill definitions | `agents[].skills` resolved against the skill directories (`.claude/skills/<name>/SKILL.md` and the runner's skill paths) |
| Agent prompt files | The generated per-agent markdown the dispatcher writes (`~/.config/opencode/agents/<id>.md`) — read as context, not edited, since it is generated from the soul file |
| Plugin manifests | `plugin_dirs` / `plugins[]` — read-only context |

**Exclusions**, enforced before anything reaches the prompt or a patch:

- `.env`, any file matching `*.env` / `*secret*` / `*credential*`
- config values under secret-bearing keys (`source_token`, `env` values, MCP env blocks) —
  redacted through the existing redaction path, never echoed back in a patch
- `.git/`, the SQLite database, log and transcript directories
- anything outside the config workspace root

A patch targeting an excluded path is dropped with a warning rather than rendered. Nothing
else is restricted: souls, skills and workflow YAML are all fair game at every effort level.

Because skill files and soul files are prose, they get the same treatment as any other
input — the advisor is asked to correlate an instruction with the runs it produced, e.g.
"this skill says *always run the full suite*, and the transcripts show 40 minutes spent
there on steps that only touch docs".

### 4. Effort levels

| | `quick` | `standard` (default) | `deep` |
|---|---|---|---|
| History window default | 7d | 14d | 90d |
| Transcripts read | 0 | ~2 per hotspot, top 5 hotspots | ~5 per hotspot, top 15 hotspots |
| Workspace files read | config + souls/skills of flagged agents | config + souls/skills of every agent with runs in the window | entire config workspace |
| Agent passes | 1 | 1 analysis + 1 patch | 1 per workflow + patch + critic pass per patch |
| Patch validation | config lint | config lint + expr lint | config lint + expr lint + counter-argument from the critic |
| Typical output | 3–5 findings | findings + diff across config, souls and skills | findings + diff + rejected-alternatives section |

Effort scales *how much* is read and how hard each patch is scrutinised — never *which
kinds* of file may be changed. Even `quick` can propose a soul-file edit if the metrics
point there; it just reads less to get to that conclusion.

Effort maps to concrete knobs on the run: `MaxTurns`, transcript budget, hotspot count,
and the number of agent invocations. Cost is reported at the end of every run, from the
same token accounting the daemon uses.

### 5. Advisor identity — which runner and model executes the analysis

The advisor is **a normal Apiary agent**, resolved through the same path the dispatcher
uses, so nothing new is invented: `agent → agents[].runner (or default_runner) →
runners[<id>] → rc.AdapterName() → runnerimpl.New(adapter) → Configure(rc.Config + MCPs +
sandbox + env_passthrough)`, with the model taken from `agents[].model`.

This matters because **`model` is required per agent and there is no config-level default
model** — `daemon.New` hard-errors with `agent %q: model is required`. So the command
cannot silently fall back to "the default model": there isn't one. Resolution is explicit,
in this order:

1. `--advisor <agent-id>` — one-off override.
2. `--runner <id> --model <name>` — ad-hoc pair, no config entry needed. Both required
   together; a lone `--model` is an error, since the runner determines the adapter.
3. `settings.improve.agent` — the configured advisor.
4. An agent whose id is `improver`, by convention.
5. **Error**, with a message naming the four options above and a copy-pasteable config
   snippet. No guessing.

```yaml
settings:
  improve:
    agent: improver          # which agent runs the analysis
    effort_models:           # optional: effort picks the model
      quick:    claude-haiku-4-5
      standard: claude-sonnet-5
      deep:     claude-opus-5
```

`effort_models` is the one addition worth making: analysing aggregates at `quick` and
reasoning over transcripts and prose at `deep` are genuinely different jobs, and paying
`deep` prices for a `quick` run is waste. When set, effort overrides `agents[].model` for
that run; the runner is unchanged. An unset effort falls through to the agent's own model.

Two existing mechanisms come along for free because the advisor is an ordinary agent:

- **Profiles** — the global `--profile <name>` flag applies `profiles.<name>` overrides
  (runner, model, fallbacks) to the advisor exactly as it does for daemon agents, so
  `apiary improve --profile cheap` works with no new code.
- **Fallbacks** — `agents[].fallbacks` and `settings.default_fallbacks` are honoured. The
  command reuses `execution.FailureDetectorFor(adapter)` to classify a rate-limit or
  credit-exhaustion rejection and advances the chain. Without this a `deep` run that
  trips the 5-hour limit halfway through would throw away everything it had done.

Flag naming note: `--agent` is a **scope filter** ("only analyse this agent's runs"), and
`--advisor` selects **who does the analysing**. These are easy to conflate, so the two
concerns get visibly different names rather than `--agent` / `--agent-id`.

### 5b. Prompt and output contract

The advisor ships with a default soul file, so it works before any config exists. Its
prompt carries:

1. The evidence pack (metrics as compact tables, not raw JSON dumps).
2. The current `apiary.yaml` and any referenced workflow/soul files, with secrets redacted.
3. The change surface and the hard constraints.
4. The improvement ledger: what was recommended before, what was applied, what the metric
   did afterwards — so it does not re-propose a change that was already tried and reverted.

It returns structured output (`APIARY_OUTPUT`, the existing mechanism) shaped as:

```json
{
  "findings": [
    { "id": "f1", "scope": "workflow:review-pr/step:lint",
      "symptom": "fails on first attempt 42% of runs, passes on retry 95% of the time",
      "evidence": ["fail_rate=0.42 n=88", "retry_success=0.95"],
      "severity": "high", "focus": "reliability" }
  ],
  "recommendations": [
    { "id": "r1", "addresses": ["f1"], "file": "apiary.yaml",
      "summary": "gate the lint step behind a build-artifacts check instead of retrying",
      "rationale": "...", "confidence": "medium",
      "expected_effect": "≈37 fewer wasted runs/2wk, ≈$4.10",
      "patch": "<unified diff>" }
  ]
}
```

Patches are unified diffs against files in the change surface. Anything outside it, and
anything touching a secret-bearing key, is dropped with a warning before rendering.

### 6. Validation and apply

A recommendation is only presented if its patch survives, in order:

1. **Path check** — the target is inside the config workspace and not on the exclusion list.
2. **Apply-in-memory** — the diff applies cleanly to the current file content.
3. **Config validation** — if the target is config, the result parses and passes
   `cfg.Validate()` (which also re-checks that every `soul_file` still resolves).
4. **Expression lint** — `config.LintExpr` over all conditions in the candidate config
   (the existing hook, so a proposed `if`/`reject_when` that would hard-fail at runtime is
   caught here rather than in production).
5. **Warnings** — `cfg.WorkflowWarnings()` on the candidate; new warnings are shown
   alongside the diff.

Prose targets — souls and skills — clear steps 1 and 2 only; there is nothing to lint in a
markdown instruction file. That asymmetry is worth stating plainly: **a bad soul-file edit
cannot be caught by validation, only by review or by its effect on the next runs.** The
critic pass at `deep` and the effect measurement in §7 are the two mitigations.

Failing patches are not silently dropped: they are listed in a "could not be validated"
section with the reason, since a rejected patch is often still a useful signal.

`--apply` writes the files, prints the summary of what changed, and stops. It does not
back up, snapshot or offer to revert: the workspace is expected to be under version
control, and `git diff` / `git checkout` are better at that job than anything this command
would reimplement. It prints one line naming the modified files so the follow-up `git diff`
is obvious, and warns (without refusing) when the workspace is not a git repository, since
that is the one case where the assumption does not hold.

Applying never restarts the daemon. The command prints the reminder that a running daemon
must be restarted to pick up config changes.

### 7. Improvement ledger and effect measurement

Two tables, added through the existing idempotent migration list:

```sql
CREATE TABLE improvement_runs (
  id TEXT PRIMARY KEY,              -- ulid
  effort TEXT NOT NULL,
  focus TEXT,
  window_start TIMESTAMP, window_end TIMESTAMP,
  scope TEXT,                       -- JSON: workflow/agent filters
  evidence_digest TEXT,             -- hash of the evidence pack, for reproducibility
  report_path TEXT, diff_path TEXT,
  applied BOOLEAN DEFAULT 0, applied_at TIMESTAMP,
  cost_usd REAL DEFAULT 0, total_tokens INTEGER DEFAULT 0,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE improvement_findings (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  scope TEXT NOT NULL,              -- "workflow:x/step:y" | "agent:z"
  focus TEXT, severity TEXT, confidence TEXT,
  symptom TEXT, rationale TEXT,
  baseline_metrics TEXT,            -- JSON snapshot of the metrics that justified it
  patch TEXT,
  state TEXT NOT NULL,              -- proposed|applied|rejected|reverted
  FOREIGN KEY(run_id) REFERENCES improvement_runs(id)
);
```

Because `baseline_metrics` is captured at proposal time and the scope is a stable key,
`apiary improve effect <run-id>` recomputes the same metrics over the window *since* the
apply and prints the delta:

```
review-pr/lint    fail rate  0.42 → 0.06   (-86%)   n=88 → n=61
review-pr/lint    cost/run   $0.31 → $0.19 (-39%)
```

This is what makes the feature a loop rather than a one-shot report: the next `improve`
run reads the ledger and is told which of its own past suggestions worked.

### 8. Scheduling (optional, later)

Nothing in the design requires the daemon, so a periodic run is just the command on a
timer. A `settings.improve.schedule` knob that has the daemon run it in report-only mode
and post the report through the existing notification channels is a natural follow-up,
deliberately left out of this change so the reviewable-diff path lands first.

---

## Implementation Phases

**Phase 1 — Metrics engine.** `internal/improve`: evidence pack types, read-only aggregate
queries, normalised failure clustering, transcript sampling, `--dump-evidence`. No LLM.
Unit tests against a seeded database. Ships useful on its own (`apiary improve
--dump-evidence` is already a real diagnostic).

**Phase 2 — Advisor agent.** Prompt composition, standalone runner invocation, structured
output parsing, markdown report, effort levels, cost accounting. `--output report`.

**Phase 3 — Patches and validation.** Workspace discovery and exclusion enforcement, diff
generation, the five-stage validation gate, `--output diff`.

**Phase 4 — Apply.** `--apply`: write the files, report what changed, warn if the workspace
is not version-controlled.

**Phase 5 — Ledger and effect.** Migrations, `improve history|show|effect`, past-run
context injected into the prompt.

**Phase 6 — Docs and defaults.** Docs page under `docs/`, default `improver` soul file,
schema regeneration (`schema/apiary.json`) if any config keys are added, example config
updates.

Phases 1–4 are the minimum shippable feature. Phase 5 is what makes it self-improving
rather than merely advisory.

---

## Risks and Mitigations

| Risk | Mitigation |
|---|---|
| The advisor proposes plausible-sounding but wrong changes | Every finding must cite metrics; every patch passes config+expr validation; `deep` adds a critic pass; diff is the default, apply is opt-in |
| Correlation read as causation (a step is slow because its task is hard) | Metrics are grouped by step *and* workflow so cross-workflow comparison of the same agent is available; the report states sample sizes and suppresses findings below a minimum `n` |
| Secrets leaking into the prompt or the report | Config is redacted through the existing redaction path before composition; transcripts are already redacted at write time; `source_token`/`env` values are never included |
| Cost of the analysis itself | Effort levels, transcript budgets, and a cost line printed at the end of every run; `quick` is a single call over aggregates only |
| Thin data early on | Minimum-sample thresholds per finding; the command says plainly when the window has too few runs instead of inventing conclusions |
| Config rewritten while the daemon runs | Apply warns and prints the restart command |
| A soul or skill edit degrades behaviour, and no lint can catch it | Diff-by-default; the critic pass at `deep`; the effect measurement in §7 scores the change against the same metrics that motivated it, so a regression surfaces on the next run |
| Reviewing a large multi-file diff is hard | Findings and their patches are rendered together — each diff hunk arrives with the metric that justified it, grouped by file, so the diff is read as an argument rather than a blob |

---

## Open Questions

1. **Command name** — `apiary improve` reads well and is unclaimed. `apiary review` and
   `apiary tune` are alternatives; `review` collides conceptually with PR review workflows.
2. **Default effort** — `standard` assumed. If typical histories are small, `quick` may be
   the better default.
3. **Skill-file resolution** — skills are declared as bare names (`agents[].skills`) and
   resolved by the runner, so the workspace walker has to search the same directories the
   runner does (`.claude/skills/<name>/SKILL.md`, plugin skill dirs). A skill that cannot
   be located is reported as unresolved rather than silently skipped, but the search paths
   need confirming against each runner adapter.
4. **Where reports live** — `<data-dir>/improve/<run-id>/` assumed, alongside transcripts.
