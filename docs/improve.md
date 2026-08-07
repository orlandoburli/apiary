# Self-Improvement Advisor

Apiary records everything needed to judge how well its own agents and workflows
perform: step timings, tokens, cache breakdown, turns, tool calls, cost, exit
state, the full composed prompt, and a markdown transcript of every session.
Until now that data only answered *what happened to this task*.

`apiary improve` asks the cross-cutting question instead:

> Across the last N runs, which steps are wasting money, which agent
> instructions keep producing rework, and what should change in `apiary.yaml`,
> the soul files and the skills?

It mines the execution history, has an agent reason over it, and emits an
evidence-backed report plus a validated diff — or applies the diff for you.

```bash
apiary improve                          # analyse and print findings + diff
apiary improve --effort deep --since 30d
apiary improve --workflow implementation
apiary improve --dump-evidence          # just the metrics, as JSON, no model
```

The command is **standalone**. It opens the database read-only, invokes a runner
directly, and writes its output locally. It works with the daemon stopped and
takes no dispatch slot when it is running.

## The evidence pack

Everything the advisor reasons over is computed in Go. **No model is involved in
producing any number**, so the same database and window always produce the same
pack — and the whole metrics layer is inspectable without paying for a run:

```bash
apiary improve --dump-evidence --since 30d
```

| Group | What it carries |
|---|---|
| **Steps** | pass/fail/skip rates, duration p50/p95, tokens, cost, turns, tool calls, cache-reuse ratio, prompt-weight ratio, failover rate, failure kinds, wall-clock split (thinking / writing / tool waits) |
| **Workflows** | instance counts by terminal state, end-to-end duration, cost per completed instance, **rework loops**, dead steps, parallel candidates |
| **Agents** | success rate, cost and duration by runner *and* model, `max_turns` saturation |
| **Waits** | poll counts and terminal status per `wait_for` step |
| **Failures** | error messages normalised and clustered, with counts and an exemplar |
| **Dead paths** | configured workflows, agents and fallback chains that never ran |

Two of these deserve a note.

**Rework loops** are the same step running more than once inside one workflow
instance — the direct signature of an `on_fail`/`goto` cycle. The repeat runs
are pure waste, and the pack prices them. On a real 30-day window this was the
single largest finding: 27% of one workflow's spend went to loop-backs.

**`max_turns` saturation** is the share of runs ending at exactly the configured
cap. Those runs were cut off, not finished — a different problem from a step
that is merely slow, and invisible in a duration metric.

Metrics below `MinRuns` (5) are carried but flagged `low_confidence`, and the
advisor is told to say when a sample is thin rather than dress it up.

## Effort levels

Effort scales **how much is read and how hard proposals are scrutinised** —
never *which kinds of file may change*. Even `quick` can propose a soul edit if
the metrics point there; it just reads less to get to that conclusion.

| | `quick` | `standard` (default) | `deep` |
|---|---|---|---|
| Default window | 7d | 14d | 90d |
| Transcripts | none | 2 per hotspot, top 5 | 5 per hotspot, top 15 |
| Excerpt budget | — | 24 KB | 40 KB |
| Config workspace read | flagged agents | agents that ran | everything |
| Critic pass | — | — | ✓ |

`--since` overrides the window; `--transcript-bytes` overrides the budget.

Transcripts are what let the advisor say something about *instructions* rather
than only about numbers. At `quick` it has aggregates alone, which is often
enough to find dead config and cost concentration but rarely enough to explain
*why* a step keeps failing.

## The config workspace

The advisor reads — and may propose changes to — everything that shapes agent
behaviour, discovered from the config rather than hard-coded:

| Source | Discovered from |
|---|---|
| Main config | `--config` / the default resolution |
| Workflow files | `uses:` references, resolved transitively |
| Soul files | `agents[].soul_file` |
| Skill definitions | `agents[].skills`, resolved against `.claude/skills/<name>/SKILL.md` and siblings |

Apiary itself has no skill resolver — it passes bare skill names through to the
runner — so discovery mirrors the runner's own conventions. **A skill that
cannot be located is reported, never silently skipped**, because an advisor
reasoning about an agent whose instructions it never saw is worse than one that
knows a piece is missing.

**Excluded, always:** `.env` and anything matching `*secret*`, `*credential*`,
`*.pem`, `*.key`; `.git/`; the database, logs and transcripts; anything outside
the workspace root.

**Secrets are redacted** before the config text reaches a prompt. Every value
inside an `env:` block is blanked rather than filtered by key name — env blocks
routinely carry tokens and the key name does not tell you. Key names survive, so
the advisor still sees *which* variables a step sets. A pure `${VAR}` reference
is preserved, since it names an environment variable rather than carrying a
secret.

To see exactly what would be sent, including the redaction:

```bash
apiary improve --dump-prompt
```

## Who performs the analysis

The advisor is an ordinary Apiary agent, resolved in this order:

1. `--advisor <agent-id>`
2. `--runner <id> --model <name>` — ad-hoc, no config entry needed
3. `settings.improve.agent`
4. an agent whose id is `improver`
5. **error**

It never invents a model. `agents[].model` is required per agent and there is no
global default, so a guess would bill you for a model you never chose. The error
names all four options and carries a config snippet.

```yaml
settings:
  improve:
    agent: improver
    effort_models:            # optional: effort picks the model
      quick:    claude-haiku-4-5
      standard: claude-sonnet-5
      deep:     claude-opus-5

agents:
  - id: improver
    description: "Analyses execution metrics and proposes improvements"
    soul_file: .apiary/agents/improver.md
    model: claude-sonnet-5
    max_workers: 1
```

No workflow triggers this agent, so the daemon never dispatches it and it adds
nothing to your concurrency. Each agent gets its own semaphore, so an agent that
never dispatches costs nothing.

Because the advisor is a normal agent, `--profile` overlays and
`agents[].fallbacks` work unchanged. A provider rejection advances the fallback
chain rather than aborting, so a `deep` run that trips a rate limit does not
discard what it already spent.

A default soul ships with the binary, so the command works before you configure
anything.

## Validation

Every proposal passes a gate before you see it:

| Stage | Check |
|---|---|
| `path` | inside the workspace, not excluded, and a file the advisor was actually shown |
| `apply` | the diff applies cleanly to current content |
| `config` | the patched tree parses and passes `cfg.Validate()` |
| `expr` | conditions still lint |
| `warnings` | new workflow warnings the patch introduces, shown beside it |

Patch application does **no fuzzy matching and no offset search**. A hunk whose
context does not match is rejected, because a patch that lands in the wrong
place is worse than one that does not land at all — the diff you reviewed would
no longer describe what the file became.

Failed proposals are not hidden. They appear under *Could not be validated* with
the stage and reason, because the observation behind a rejected patch is often
still worth acting on by hand.

### Prose cannot be validated

Souls and skills clear the first two stages only. **Nothing in a markdown
instruction file can be checked mechanically**, and the rendered diff says so
per file:

> _prose file — only checked that the patch applies; nothing here can be
> validated mechanically_

This is the common case, not the edge case — instruction edits are frequently
the highest-value change the advisor finds. Two things mitigate it: the critic
pass at `deep` effort, which argues against each surviving proposal, and effect
measurement, which scores the change against the metric that motivated it.

## Applying

```bash
apiary improve --apply          # prints the diff, asks once, writes
apiary improve --apply --yes    # skips the question, not the diff
```

Apply does not back up, snapshot, or offer to revert. **Your workspace is
expected to be under version control**, and `git diff` / `git checkout` do that
job better than anything Apiary would reimplement. What it owes you instead is
an accurate account of what it touched:

```
Applied 2 change(s):
  .apiary/agents/engineer.md  (+3 −0)  — prose, not machine-checked
  .apiary/apiary.yaml         (+1 −1)

Review with `git diff`; undo with `git checkout -- <file>`.

Configuration changed. A running daemon keeps its loaded copy until restarted:
  apiary restart
```

Where the git assumption does not hold it says so — before writing and again
afterwards. It warns rather than refuses.

Two proposals patching the same file are refused outright: each was validated
against the *original* content, so applying both in sequence would apply the
second to content it never saw.

## Measuring whether it helped

Every run is recorded with the metrics that justified each proposal, so a later
run can recompute them and compare:

```bash
apiary improve history          # past runs, newest first
apiary improve show <run-id>    # that run's findings and their fate
apiary improve effect <run-id>  # before/after for an applied run
```

```
## workflow:implementation/step:implement

n: 88 before → 61 after

| metric      | before  | after   | change        |
|---|---|---|---|
| fail rate   | 42%     | 6%      | ↓ 86% better  |
| cost/run    | $0.310  | $0.190  | ↓ 39% better  |
```

This is what makes the advisor a loop rather than a report generator, and it
matters most for instruction edits: since nothing could validate them when they
were applied, these numbers are the first evidence either way.

A post-apply window below `MinRuns` is labelled *shown for completeness, not as
a result* — a percentage off two runs reads as a finding when it is noise.

Applied findings are also fed back into the next run's prompt, so the advisor
does not re-propose something already tried. If a problem persists after its fix
was applied, that is worth knowing: the fix did not work, and the reason matters
more than another attempt at the same idea.

## Flags

```
scope
  --since 30d                    history window; defaults per effort
  --workflow <id>                restrict to these workflows (repeatable)
  --agent <id>                   restrict to these agents' runs (repeatable)
  --focus cost|latency|reliability|quality|all

who analyses
  --advisor <agent-id>
  --runner <id> --model <name>   ad-hoc pair
  --profile <name>

depth and delivery
  --effort quick|standard|deep   default: standard
  --output diff|report|json      default: diff
  --apply                        write the accepted changes
  --yes                          skip the confirmation
  --out <dir>                    also write report, analysis and evidence
  --dump-evidence                print the metrics as JSON, run no model
  --dump-prompt                  print the composed prompt, run no model
  --transcript-bytes <n>         override the per-excerpt budget
```

## What it will not do

- Change Go source, or anything outside the config workspace
- Touch secrets — `.env`, tokens, credential files
- Run unattended. There is no scheduled mode; every run is operator-initiated,
  and applying is opt-in on top of that
- Claim a thin sample is a result

## Reading the output well

The advisor is asked to cite a metric for every finding, to declare thin
samples, and to prefer three solid findings over twelve guesses. It generally
does. What it cannot do is know why a step is hard: a step may be expensive
because the work is genuinely difficult, not because it is misconfigured. Where
the same agent runs in more than one workflow the pack makes that comparison
available, but the judgement is yours.

Treat a recommendation as an argument with evidence attached, not a conclusion —
which is why the diff renders each hunk next to the number that motivated it.
