# Rate Limits & Resilience

Unattended dispatch is only useful if it is safe to leave running. Apiary is
built around a single dispatch path plus a small set of safeguards that keep
a saturated provider, a failing task, or a daemon restart from turning into a
runaway loop or a stuck queue.

## Runner failures → failover

When a runner's invocation cannot produce useful work — whether the provider
rejected it (rate limit), the account ran out of credits, or the process
exited with an error — Apiary does **not** treat the empty run as a success.
Instead it:

1. **Pauses that runner type** with a failure-kind-specific cooldown. Because
   every agent sharing the same runner type (e.g. all Claude agents) shares one
   account, pausing is keyed by runner type — all agents back off together.
2. **Fails over** to the agent's next `fallbacks` entry whose runner isn't
   paused, retrying the same step on that runner/model. While the primary is
   paused, new steps go *straight* to the fallback — no wasted, pre-failed
   call.

### Failure detection

Apiary classifies failures into three kinds, each with its own cooldown:

| Kind | Detection | Default cooldown |
|---|---|---|
| **Rate-limited** | `rate_limit_event` JSON with `status: "rejected"` (Claude), HTTP 429, `"rate limit"` / `"too many requests"` in error output | Provider-reported reset, or 5 minutes |
| **Credit-exhausted** | `"out of credits"`, `"insufficient credits"`, `"billing limit"`, `"payment required"` in stderr/stdout | 24 hours (configurable via `settings.credit_exhausted_cooldown`) |
| **Aborted** | Non-zero exit with empty or error-only output (no substantive work done) | 0 — retry fallback immediately |

The generic failure detector scans both stdout and stderr for known credit and
rate-limit patterns. Provider-specific detectors can be registered per runner
type for more precise classification.

```yaml
settings:
  # Override the default 24h cooldown for credit-exhausted failures
  credit_exhausted_cooldown: "48h"
```

### Fallback chains

Each agent can declare an ordered list of alternative runner/model pairs:

```yaml
agents:
  - id: engineer
    runner: codex
    model: gpt-5.5
    fallbacks:
      - {runner: opencode-go, model: opencode-go/deepseek-v4-flash}
      - {runner: cursor, model: composer-2.5-fast}
```

Agents without explicit `fallbacks` inherit the global chain from
`settings.default_fallbacks`:

```yaml
settings:
  default_fallbacks:
    - {runner: opencode-go, model: opencode-go/deepseek-v4-pro}
```

### Fallback strategies

The order in which candidates are tried can be controlled via
`fallback_strategy`:

| Strategy | Behavior |
|---|---|
| `ordered` (default) | Primary first, then fallbacks in config order |
| `random` | Shuffle candidates before each dispatch |
| `least_cost` | Sort by historical average cost per run (ascending) |
| `fastest` | Sort by historical average duration per run (ascending) |

```yaml
agents:
  - id: reviewer
    fallback_strategy: fastest
```

Strategies can also be overridden per workflow:

```yaml
workflows:
  - id: code-review
    fallback_strategy: fastest
```

### Runner profiles

Named profiles let you switch the entire fleet's runner assignment at startup
without editing individual agent configs — useful for emergency failover when
a provider runs out of credits:

```yaml
profiles:
  opencode:
    engineer:  {runner: opencode-go, model: opencode-go/deepseek-v4-flash}
    backend:   {runner: opencode-go, model: opencode-go/deepseek-v4-flash}
    frontend:  {runner: opencode-go, model: opencode-go/deepseek-v4-flash}
    reviewer:  {runner: opencode-go, model: opencode-go/deepseek-v4-flash}
    qa:        {runner: opencode-go, model: opencode-go/deepseek-v4-pro}
```

Activate at startup:

```sh
apiary run --profile=opencode
```

See [Configuration - profiles](configuration.md#profiles) for the full reference.

### Execution recording

Each attempt is recorded as its own execution row, so the dashboard shows the
failover trail (primary credit-exhausted → fallback ran), with per-attempt
tokens, cost, and failure classification (`failure_kind`, `credit_exhausted`).

!!! note
    A failed-over attempt is **not** counted as a task failure: it doesn't
    count against `max_attempts`, and the task is never burned on it.

## Re-dispatch failure cap

A task whose workflow keeps failing would otherwise be re-dispatched on every
poll, forever. `settings.max_attempts` is an internal backstop, independent
of source-side labels:

- After **N consecutive failed instances** for the same `(task, workflow)`,
  Apiary stops re-dispatching it and applies the workflow's `on_fail` hook
  (if any) so the source item visibly reflects the situation.
- Rate-limited runs fail over and are not counted.
- A single success resets the count.
- Default `3`; set `<=0` to disable.

```yaml
settings:
  max_attempts: 3
```

## Non-blocking dispatch

Each agent's `max_workers` slot is acquired inside the dispatch goroutine,
not on the poll-loop thread. A fully-busy agent therefore parks its own runs
without stalling polling or dispatch for any other source or agent — one
slow, saturated agent can't freeze the hive.

Parked CI re-checks ([`wait_for` steps](workflows.md#wait_for-steps-waiting-on-ci))
follow the same principle: the cheap status check runs ungated every cycle,
and only the follow-on agent work competes for agent slots — a long agent run
can't starve unrelated CI waits.

## Surviving restarts

Daemon restarts (crash, upgrade, reboot) don't lose in-flight work:

- **Approval rehydration.** Instances parked on an
  [approval gate](workflows.md#approval-steps) are reloaded at startup with
  their original park time and timeout intact, and keep being re-checked
  against their resume/abort conditions.
- **CI-wait rehydration.** Parked [`wait_for`](workflows.md#wait_for-steps-waiting-on-ci)
  instances survive the same way — the wait resumes where it left off.
- **Orphan reconciliation.** Instances left in `running` by a crash are
  marked `interrupted` at startup, so the next poll can dispatch fresh
  instances instead of leaving tasks stuck behind a ghost. Interrupted
  instances of resumable workflows can be continued with
  [`apiary resume`](cli.md#apiary-resume).
- **Force restart.** From the [dashboard](dashboard.md) (`R` on a task) or
  `apiary restart <task>`, a stale task's running dispatch is
  cancelled, its non-terminal instances are interrupted, and it is reset for
  re-dispatch on the next cycle.

## Timeouts

Every run is bounded by `settings.task_timeout` (default 30m) so a hung
subprocess cannot hold an agent slot forever. Approval steps and CI waits
carry their own explicit `timeout` / `max_duration` budgets.

## What to monitor

- The dashboard **Overview** tab: success rate and queued count are the first
  movers when something is wrong.
- A task repeatedly failing toward its `max_attempts` cap shows its attempt
  count in the task detail view; the `on_fail` labels you configure are the
  tracker-side signal.
- `apiary status` gives the same health summary headlessly (e.g. from cron).
