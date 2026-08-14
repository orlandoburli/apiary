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

## Escalation notifications

Escalating to a human is only useful if the human finds out. Without
configuration, a workflow that parks a task with `needs-attention` leaves the
label sitting on the issue until someone happens to look — a failed staging
deploy can freeze the pipeline silently.

The top-level `notifications:` block fires whenever a hook (a workflow's
`on_fail`/`on_complete`, the task-level `tasks:` hooks, or the failure-cap
park above) adds one of the watched labels to a source item:

```yaml
notifications:
  on_labels: [needs-attention]
  channels:
    - type: command
      run: curl -s -d "{{number}} escalated ({{label}}): {{summary}}" ntfy.sh/my-alerts
```

Only `type: command` exists — an arbitrary shell hook (ntfy, a Slack webhook
via `curl`, `osascript`, e-mail, …), so Apiary carries no provider
integrations. Channels run asynchronously with a 60-second timeout and never
block or fail the hook that triggered them.

The command may use `{{task_id}}`, `{{cell_id}}`, `{{number}}`, `{{title}}`,
`{{url}}`, `{{label}}`, and `{{summary}}` placeholders — values are
shell-quoted on substitution, so titles with quotes cannot break or inject
into the command line. The same values are exported to the hook as
`APIARY_TASK_ID`, `APIARY_CELL_ID`, `APIARY_NUMBER`, `APIARY_TITLE`,
`APIARY_URL`, `APIARY_LABEL`, and `APIARY_SUMMARY`. `{{summary}}` is the
latest step summary of the task's newest workflow instance (falling back to
the last failed step's error, then the task title).

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
- **Auto-resume (`resume: auto`).** A workflow that declares `resume: auto`
  (which requires every step to be `idempotent: true`) does not wait for a
  human: right after orphan reconciliation, the daemon continues each
  interrupted instance itself — passed steps are replayed from cache and only
  the step that was in flight re-runs. The continuation is a normal resume
  descendant (`resumed_from` points at the orphan), and while it is starting,
  a poll of the same task cannot dispatch a competing fresh run of that
  workflow. Each orphan is continued at most once: an instance that already
  has a descendant (from an earlier auto-resume or a manual `apiary resume`)
  is left alone. Workflows on the default `resume: allowed` — and on
  `resume: forbidden` — keep the older behavior: the interrupted instance
  stays put and the next poll dispatches a fresh run from step 1, so a
  long pipeline that must not restart from scratch should opt into
  `resume: auto`.
- **Queue redelivery.** In [queue mode](distributed-workers.md) the job whose
  run the crash killed is still leased; the next process reclaims it once the
  lease expires and delivers it again. That redelivery is dropped when the same
  task and workflow already has a live run — one being auto-resumed, or an
  instance that is running or parked — so a reclaimed job cannot put a second
  agent on the branch its first attempt is still working on. The embedded worker
  starts only after every startup pass above, so it never claims a job before
  those guards exist. A redelivery with nothing live behind it still runs: that
  is the recovery path.
- **Force restart.** From the [dashboard](dashboard.md) (`R` on a task) or
  `apiary restart <task>`, a stale task's running dispatch and queued jobs are
  cancelled, its non-terminal instances are interrupted, its control labels are
  stripped, and it is re-routed and dispatched immediately. Restart overrides the
  `once` and failure-cap guards — a task parked behind either is exactly what it
  is for — but never the in-flight guard, so a live workflow is not run twice.
  Both surfaces report what was dispatched, including "nothing matched".

## Timeouts

Every run is bounded by `settings.task_timeout` (default **2h**) so a hung
subprocess cannot hold an agent slot forever. Approval steps and CI waits carry
their own explicit `timeout` / `max_duration` budgets.

Two hours is deliberately generous: an implementation step routinely runs for an
hour or more, and a short total bound kills real work rather than runaway work.
That makes `task_timeout` a poor instrument for catching a *hung* process, which
is what `settings.stall_timeout` is for:

```yaml
settings:
  task_timeout: 2h      # total bound — the backstop
  stall_timeout: 20m    # no output at all for this long — the hang detector
```

A step streaming tool calls for ninety minutes is working. A step that has
emitted nothing for twenty is usually wedged. Only the second is worth killing
early, and only `stall_timeout` can tell them apart.

`stall_timeout` is **off by default**. It measures silence, so a runner that
buffers its output until exit instead of streaming would look permanently
stalled — enabling it globally would kill those runs on upgrade. Turn it on for
the streaming CLI runners (`claude`, `codex`, `cursor`, `opencode`).

Both bounds are logged at step start, so the log answers "is this step bounded
at all?" without reading the config:

```
step implement: timeout 2h0m0s, stall timeout 20m0s
```

When either fires, the run is recorded as timed out with the bound that fired
and how long it ran — not as a bare `signal: killed`, which is indistinguishable
from a crash.

## What to monitor

- The dashboard **Overview** tab: success rate and queued count are the first
  movers when something is wrong.
- A task repeatedly failing toward its `max_attempts` cap shows its attempt
  count in the task detail view; the `on_fail` labels you configure are the
  tracker-side signal.
- `apiary status` gives the same health summary headlessly (e.g. from cron).
