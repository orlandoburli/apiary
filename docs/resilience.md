# Rate Limits & Resilience

Unattended dispatch is only useful if it is safe to leave running. Apiary is
built around a single dispatch path plus a small set of safeguards that keep
a saturated provider, a failing task, or a daemon restart from turning into a
runaway loop or a stuck queue.

## Provider rate limits → failover

When a runner is rejected by a provider usage limit — e.g. the Claude CLI
emits a `rate_limit_event` with `status: rejected` ("you've hit your session
limit") — Apiary does **not** treat the empty run as a success. Instead it:

1. **Pauses that runner type** until the limit resets. Because every Claude
   agent shares one account, pausing is keyed by runner type — all Claude
   agents back off together rather than each burning a task to rediscover the
   same limit.
2. **Fails over** to the agent's next `fallbacks` entry whose runner isn't
   paused, retrying the same step on that runner/model. While the primary is
   paused, new steps go *straight* to the fallback — no wasted, pre-failed
   call.

```yaml
agents:
  - id: engineer
    runner: claude
    model: claude-sonnet-4-6
    fallbacks:
      - {runner: opencode-go-cli, model: opencode-go/deepseek-v4-pro}
      - {runner: cursor, model: composer-2.5-fast}
```

Each attempt is recorded as its own execution, so the dashboard shows the
failover trail (primary rate-limited → fallback ran), with per-attempt
tokens and cost. `fallbacks` load at startup — changing the chain requires a
restart.

!!! note
    A rate-limited attempt is **not** a failure: it doesn't count against
    `max_attempts`, and the task is never burned on it.

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
