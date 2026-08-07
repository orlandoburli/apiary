# Implementation Plan: Self-Improvement Advisor

Companion to [proposal.md](proposal.md). Phases are ordered so each one ends at a
shippable state — Phase 1 is a useful diagnostic on its own, and nothing before Phase 4
can write to disk.

**Conventions.** One feature branch per phase, PR per phase (`git worktree` per the
project's git workflow), squash merge. No Go CI in this repo — `make check` (build + test)
locally before every PR. New DB columns/tables go in the idempotent `migrations` slice in
`internal/db/schema.go`, never in the `schema` const.

---

## Phase 1 — Metrics engine (no LLM)

New package `internal/improve`. Everything here is deterministic and unit-testable against
a seeded database.

### 1.1 Types — `internal/improve/evidence.go`

- [ ] `EvidencePack` — top-level struct: `Window`, `Scope`, `Workflows []WorkflowMetrics`,
      `Steps []StepMetrics`, `Agents []AgentMetrics`, `Failures []FailureCluster`,
      `Waits []WaitMetrics`, `Transcripts []TranscriptExcerpt`, `Config ConfigSnapshot`,
      `Digest string`
- [ ] `StepMetrics` — `WorkflowID`, `StepID`, `AgentID`, `Runs`, `PassRate`, `FailRate`,
      `SkipRate`, `CachedSkipRate`, `DurationP50/P95`, `MeanTokens`, `TotalCost`,
      `MeanTurns`, `MeanToolCalls`, `CacheReuseRatio`, `PromptWeightRatio`,
      `FailoverRate`, `FailureKinds map[string]int`, `MaxTurnsSaturation`
- [ ] `WorkflowMetrics` — instance counts by terminal state, e2e `DurationP50/P95`,
      `CostPerCompleted`, `ReworkLoops []ReworkLoop`, `ParallelCandidates []StepPair`,
      `DeadSteps []string`
- [ ] `AgentMetrics`, `WaitMetrics`, `FailureCluster`, `TranscriptExcerpt`
- [ ] `Digest()` — stable hash over the pack (sorted keys, window excluded) for
      reproducibility and for `improvement_runs.evidence_digest`

### 1.2 Queries — `internal/improve/metrics.go`

Opens the existing DB read-only. All queries take `(ctx, window, scope)`.

- [ ] `stepMetrics` — `step_runs` ⋈ `workflow_instances`, plus a `task_executions`
      sub-select for `attempt > 1` failover rate and `failure_kind` distribution.
      Percentiles computed in Go over the ordered durations (SQLite has no `percentile`).
- [ ] `workflowMetrics` — instance terminal-state counts, e2e duration, cost per completed
- [ ] `reworkLoops` — `GROUP BY workflow_instance_id, step_id HAVING COUNT(*) > 1`;
      this is the `on_fail`/`goto` cycle signature
- [ ] `agentMetrics` — `task_executions` grouped by `agent_id`, `runner`, `model`;
      `max_turns` saturation needs the configured cap from `config`, joined in Go
- [ ] `waitMetrics` — `ci_poll_checks` per (instance, step): poll count, terminal status
      distribution, timeouts; approval latency from `approval_requests` ⋈
      `approval_responses`
- [ ] `deadPaths` — configured workflows/steps/agents/fallbacks with zero rows in the window
- [ ] `failureClusters` — `error_message` from failed executions, normalised (strip
      digits, hex, paths, UUIDs/ULIDs) then grouped with counts and one exemplar

### 1.3 Static config analysis — `internal/improve/static.go`

- [ ] `parallelCandidates(wf)` — consecutive sequential steps where no later step's
      `Condition`/`FailWhen`/`If` or prompt references an earlier step's output.
      Conservative: emit only when there is provably no reference; a false negative is
      cheap, a false positive is a wrong recommendation.

### 1.4 Transcript sampling — `internal/improve/transcripts.go`

- [ ] `sampleTranscripts(hotspots, budget)` — reads `<log-dir>/transcripts/<task-id>/`,
      picks the worst N failures plus one successful control per hotspot, truncates to a
      per-excerpt character budget (head + tail, middle elided)
- [ ] Hotspot ranking: cost × failure rate × run count, so a cheap flaky step and an
      expensive reliable one both surface

### 1.5 Minimum-sample gating

- [ ] `MinRuns` threshold (default 5) — metrics below it are carried in the pack but
      flagged `low_confidence`, and the report says so rather than dropping them silently

### 1.6 CLI entry — `internal/cli/improve.go`

- [ ] `newImproveCmd()`, registered in `root.go`'s `AddCommand` list
- [ ] Flags per proposal §1; `--dump-evidence` prints the pack as indented JSON and exits
- [ ] `--since` duration parsing accepting `7d`/`24h`/`90d`

### 1.7 Tests — `internal/improve/*_test.go`

- [ ] Seeded-DB fixture helper: N instances × M steps with controlled outcomes
- [ ] Per-metric assertions (rates, percentiles, rework detection, dead paths)
- [ ] Failure normalisation table test
- [ ] `parallelCandidates` — positive and negative cases, especially a step referencing an
      earlier output through an expression
- [ ] `Digest` stability: same data → same digest; reordered rows → same digest

**Done when** `apiary improve --dump-evidence --since 30d` prints a correct, stable pack on
a real database, and `make check` passes.

---

## Phase 2 — Advisor agent

### 2.1 Workspace discovery — `internal/improve/workspace.go`

(Proposal §3. Landing it here rather than in Phase 3 because the prompt needs it.)

- [ ] `Discover(cfg, configPath) (Workspace, error)` — walks config → workflow files
      (transitive) → soul files → skill definitions → generated agent prompt files →
      plugin manifests
- [ ] Skill resolution: search `.claude/skills/<name>/SKILL.md` and the runner skill dirs;
      unresolved skills recorded as `Unresolved []string`, never silently dropped
      (see proposal open question 3)
- [ ] `Excluded(path) bool` — `.env`, `*secret*`, `*credential*`, `.git/`, DB, logs,
      transcripts, anything outside the workspace root
- [ ] `RedactConfig(raw) string` — blanks `source_token`, `env` values, MCP env blocks
      before the config text enters a prompt

### 2.2 Prompt composition — `internal/improve/prompt.go`

- [ ] Metrics rendered as compact markdown tables, not raw JSON — token cost matters and
      tables read better for the model
- [ ] Workspace files inlined with path headers, per-file truncation at high effort
- [ ] Constraints block: exclusion list, "cite a metric for every finding", output contract
- [ ] Ledger block (empty until Phase 5)
- [ ] `OutputSchema` for the findings/recommendations JSON, reusing
      `execution.OutputSchemaInstruction` so the sentinel instructions match the daemon's

### 2.3 Advisor resolution — `internal/improve/advisor.go`

Proposal §5. There is **no config-level default model** (`agents[].model` is required and
`daemon.New` errors without it), so this resolves explicitly or fails loudly.

- [ ] `ResolveAdvisor(cfg, flags) (Advisor, error)` in order: `--advisor` → `--runner` +
      `--model` → `settings.improve.agent` → agent id `improver` → error naming all four
      with a copy-pasteable config snippet
- [ ] `--model` without `--runner` is a flag error (the runner determines the adapter)
- [ ] `settings.ImproveSettings` on `config.Settings`: `Agent string`,
      `EffortModels map[string]string`; validated in `internal/config/validate.go`
      (agent must exist; effort keys ∈ {quick,standard,deep}; models must be listed in the
      resolved runner's `models` when that runner declares any)
- [ ] Apply `profiles.<name>` overrides when `--profile` is set, reusing the daemon's
      overlay logic rather than re-implementing it
- [ ] Effort → model: `effort_models[effort]` overrides `agents[].model` for the run;
      unset falls through to the agent's own model

### 2.4 Standalone runner invocation — `internal/improve/run.go`

- [ ] Mirror the dispatcher's construction path: `runners[id]` → `rc.AdapterName()` →
      `runnerimpl.New(adapter)` → `Configure(runnerConfigWithMCPs(...) + sandbox +
      env_passthrough)`. Extract the shared piece from `daemon` if it can be done without
      dragging in dispatcher state; otherwise duplicate deliberately and note why.
- [ ] Build `model.RunRequest` (`SystemAppend`, `Model`, `MaxTurns` from effort,
      `WorkingDir`, `Env`), call `Run`
- [ ] Read `RunResult.StructuredOutput` directly — the runner already parses the
      `APIARY_OUTPUT:` sentinel, so no marker parsing is needed here
- [ ] Fallback chain: on a `RateLimited` / `CreditExhausted` classification from
      `execution.FailureDetectorFor(adapter)`, advance through `agents[].fallbacks` then
      `settings.default_fallbacks`. A `deep` run that trips the 5-hour limit mid-way must
      not discard the work already done.
- [ ] Accumulate `RunResult.Usage` across passes and fallback attempts for the cost line

### 2.5 Effort levels — `internal/improve/effort.go`

- [ ] `Effort` enum → knobs struct: window default, hotspot count, transcripts per hotspot,
      excerpt budget, workspace read breadth, pass count, critic on/off, `MaxTurns`

### 2.6 Report rendering — `internal/improve/report.go`

- [ ] Markdown: summary → findings grouped by severity → recommendations with rationale
      and expected effect → low-confidence section → cost line
- [ ] `--output json` emits the structured result verbatim
- [ ] Written to `<data-dir>/improve/<run-id>/` and echoed to stdout

### 2.7 Default advisor soul — `internal/cli/assets/improver.md` (embedded)

- [ ] Ships a working default so `apiary improve` needs no config; overridable by defining
      an `improver` agent

### 2.8 Tests

- [ ] Prompt composition golden test (redaction verified: no token value appears)
- [ ] Structured-output parsing: well-formed, malformed, missing
- [ ] Effort knob table test
- [ ] Workspace discovery against a fixture tree, including an unresolved skill
- [ ] `ResolveAdvisor` precedence table: each of the four sources wins in turn; the
      no-advisor case returns the actionable error, not a guessed model
- [ ] `--model` without `--runner` is rejected
- [ ] `effort_models` overrides the agent model; unset falls through
- [ ] `--profile` overlay applies to the advisor

**Done when** `apiary improve --output report` produces an evidence-backed markdown report.

---

## Phase 3 — Patches and validation

### 3.1 Patch handling — `internal/improve/patch.go`

- [ ] Parse unified diffs from recommendations; apply in-memory to current file content
- [ ] Reject patches whose target fails `Excluded`/outside-workspace checks
- [ ] Fuzzy-match tolerance: reject rather than guess when context does not match

### 3.2 Validation gate — `internal/improve/validate.go`

Five stages per proposal §6, short-circuiting with a recorded reason:

- [ ] path check → 2. clean apply → 3. `config.Load` + `cfg.Validate()` on the candidate
- [ ] `config.LintExpr` over the candidate's conditions (wire the same hook `cli` injects,
      so a proposed `if`/`reject_when` that would hard-fail at runtime is caught here)
- [ ] `cfg.WorkflowWarnings()` — new warnings surfaced alongside the diff
- [ ] Prose targets (souls, skills) clear stages 1–2 only — document this in the output so
      the reviewer knows which hunks were machine-checked and which were not

### 3.3 Diff rendering

- [ ] Grouped by file; each hunk preceded by the finding and metric that justified it
- [ ] "Could not be validated" section listing rejected patches with reasons

### 3.4 Tests

- [ ] Patch application: clean, conflicting, excluded-path, outside-workspace
- [ ] A patch introducing an invalid expression is rejected at stage 4
- [ ] A patch removing a `soul_file` a config still references is rejected at stage 3

**Done when** `apiary improve` (default `--output diff`) prints a validated, reviewable diff.

---

## Phase 4 — Apply

### 4.1 `internal/improve/apply.go`

- [ ] Write accepted patches to disk; print the list of modified files
- [ ] Warn (do not refuse) when the workspace is not a git repository — version control is
      the user's responsibility and the undo mechanism
- [ ] Print the daemon-restart reminder when config changed
- [ ] `--yes` skips the single confirmation prompt

### 4.2 Tests

- [ ] Apply writes exactly the expected bytes; non-git workspace produces the warning

**Done when** `apiary improve --apply` modifies the workspace and `git diff` shows the change.

---

## Phase 5 — Ledger and effect measurement

### 5.1 Migrations — `internal/db/schema.go`

- [ ] `improvement_runs` and `improvement_findings` per proposal §7, appended to the
      `migrations` slice (`CREATE TABLE IF NOT EXISTS` statements are safe there)
- [ ] Indexes on `improvement_findings(run_id)` and `improvement_findings(scope, state)`

### 5.2 Store — `internal/db/improvement.go`

- [ ] `CreateImprovementRun`, `RecordFindings`, `MarkApplied`, `ListImprovementRuns`,
      `GetImprovementRun`, `ListFindingsByScope`

### 5.3 Effect comparison — `internal/improve/effect.go`

- [ ] Recompute the Phase 1 metrics for each finding's `scope` over the window *since*
      `applied_at`, diff against `baseline_metrics`, render the delta table
- [ ] Guard against thin post-apply samples — say "not enough runs yet" rather than
      reporting a meaningless delta

### 5.4 Sub-commands

- [ ] `apiary improve history|show <run-id>|effect <run-id>`

### 5.5 Prompt feedback loop

- [ ] Inject prior findings and their measured effect into the prompt so the advisor does
      not re-propose something already tried and reverted

### 5.6 Tests

- [ ] Ledger round-trip; effect delta on a seeded before/after dataset; thin-sample guard

**Done when** `apiary improve effect <run-id>` shows a real before/after delta.

---

## Phase 6 — Docs and defaults

- [ ] `docs/improve.md` — what it measures, effort levels, the change surface, the git
      assumption, worked example
- [ ] Add to the docs nav in `mkdocs.yml`
- [ ] `schema/apiary.json` regenerated for `settings.improve` (`agent`, `effort_models`)
- [ ] Example config: an `improver` agent entry plus a `settings.improve` block
- [ ] Archive the change in `openspec/CHANGELOG.md` (move from Ativas to Arquivadas)

---

## Sequencing notes

- **Phases 1–4 are the minimum shippable feature**; Phase 5 is what makes it a loop rather
  than a report generator, and it matters more than usual here because prose edits (souls,
  skills) are in scope from day one and cannot be caught by any lint.
- Workspace discovery (§2.1) is listed in Phase 2 because the prompt needs it, but it is
  the natural thing to pull forward if Phase 1 finishes early.
- The riskiest unknown is skill-path resolution across runner adapters (proposal open
  question 3). Resolve it while building §2.1 — an unresolved skill must be reported, not
  silently skipped, or the advisor will reason about an agent whose instructions it never saw.
