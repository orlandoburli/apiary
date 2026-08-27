# Prometheus Source

The `prometheus` source adapter polls **Prometheus Alertmanager**
(`GET /api/v2/alerts`) and maps each firing alert to an Apiary task — so an
operational signal, not just a human-filed ticket, can trigger a workflow.
The canonical pattern: an alert fires (`HighErrorRate` on service X) → an
investigation workflow dispatches → the agent pulls logs, diagnoses, and
publishes findings as a GitHub issue or fix PR.

Alertmanager is polled (not the Prometheus rules API) because it has the
deduplication, silence, and inhibition semantics already applied: silenced
and inhibited alerts are never ingested.

## Configuration

```yaml
sources:
  - id: prod-alerts
    type: prometheus
    poll_interval: 30s
    config:
      alertmanager_url: https://alertmanager.internal:9093
      bearer_token: ${ALERTMANAGER_TOKEN}   # optional
      max_new_per_poll: 10                  # optional, storm cap
      min_age: 1m                           # optional, flap dampener
      ack_via_silence: true                 # optional, silence while investigating
      silence_duration: 2h                  # optional, how long that silence lasts
    filters:
      labels: ["severity=critical", "team=platform"]

workflows:
  - id: investigate-alert
    trigger:
      match: { source: prod-alerts }
      once: true
    steps:
      - id: investigate
        agent: sre-investigator
```

### `config` fields

| Field | Required | Description |
|---|---|---|
| `alertmanager_url` | yes | Base URL of the Alertmanager instance (`https://host:9093`) |
| `bearer_token` | no | Sent as `Authorization: Bearer …` on every request |
| `basic_auth_user` / `basic_auth_password` | no | HTTP Basic auth (ignored when `bearer_token` is set) |
| `max_new_per_poll` | no | Alert-storm cap: at most this many *not-yet-seen* alerts become tasks per poll; the overflow is logged and surfaces on later polls. `0` disables the cap. Default `10` |
| `min_age` | no | Flap dampener: an alert must have been firing at least this long before it is ingested (complements the alerting rule's own `for:`). Default `1m` |
| `dispatch_by` | no | `alert` (default) dispatches one task per firing alert; `group` dispatches one task per Alertmanager group, so one incident that fans out into many alerts becomes a single investigation |
| `ack_via_silence` | no | When `true`, acknowledging a dispatched alert creates an Alertmanager silence for it, so it stops paging while an agent investigates. Default `false` (acknowledge is a no-op) |
| `silence_duration` | no | How long an `ack_via_silence` silence lasts. Default `2h` |

These are the only keys accepted; `apiary validate` rejects anything else (see
[Accepted `config` keys](configuration.md#accepted-config-keys)).

### `filters`

| Field | Description |
|---|---|
| `labels` | Alertmanager matchers, ANDed and evaluated server-side. Accepts `key=value`, `key:value`, or a raw matcher with operator and quoted value (`env=~"prod|staging"`). A bare word matches `alertname` |
| `states` | Not applicable — only firing (active, unsilenced, uninhibited) alerts are polled; any value other than `firing` is ignored with a warning |

## Alert → task mapping

| Task field | Alert value |
|---|---|
| ID | `fingerprint:startsAt` — stable for the whole fire cycle |
| Number | first 7 chars of the fingerprint |
| Title | `alertname` + `summary` annotation |
| Description | summary/description annotations, full label set, extra annotations, firing-since timestamp, generator URL |
| Labels | every alert label as `key:value` (e.g. `severity:critical`) — so `trigger.match.labels: [severity:critical]` works verbatim |
| Type | `alert` |
| Priority | the `severity` label |
| State | `firing` |
| URL | the alert's `generatorURL` |
| Metadata | raw payload: fingerprint, labels, annotations, startsAt/endsAt, status |

## Behavior

- **Exactly once per fire cycle.** The task ID is the Alertmanager
  fingerprint plus `startsAt`: while the alert stays firing the ID is
  constant and the dispatcher's dedup (active-instance drop, `once: true`,
  persisted task identity) prevents re-dispatch — including across daemon
  restarts. When the alert resolves and later fires again, `startsAt`
  changes, producing a new task and a new dispatch.
- **Storm cap.** One bad deploy can fire 50 alerts in a single poll; each
  would become a task and an agent run. `max_new_per_poll` bounds how many
  new alerts are admitted per poll (oldest first); the rest are deferred to
  subsequent polls and a warning names the deferred count. See also
  `dispatch_by: group`, which collapses one incident into one task.
- **Flap dampening.** `min_age` skips alerts younger than the threshold, so
  firing→resolved→firing blips never dispatch. Combined with the
  fingerprint+startsAt identity this also means a *resolved-and-refired*
  alert within the window is a fresh item only once it has stayed firing.
- **Read-only.** Alerts have no assignable state, labels, or comments to
  write back to: result write-back is a no-op, `Acknowledge` is one too
  unless `ack_via_silence` is set, and the adapter implements none of the
  optional write capabilities. `apiary validate` rejects workflows that pin
  this source and use `on_complete.set_state` / `add_labels`, approval
  steps, `wait_for` CI steps, or `materialize: sub_issue`.
- **Resolved while running.** By default a running investigation is not
  interrupted when its alert resolves; the workflow finishes normally. Set
  `interrupt_on_resolve` on the source to opt into stopping it instead.

## Acknowledge via silence

By default a dispatched alert keeps notifying: Apiary picking it up is
invisible to Alertmanager, so the on-call is still paged for something an
agent is already working on. Setting `ack_via_silence: true` closes that gap
— when the workflow acknowledges the alert, the adapter creates a silence
for it:

```yaml
config:
  alertmanager_url: https://alertmanager.internal:9093
  ack_via_silence: true
  silence_duration: 2h
```

- **Pinned to one alert.** The silence carries an exact-equality matcher for
  every label of the alert that was dispatched, so it suppresses that alert
  and nothing else. It is never a regex or a partial match.
- **Always time-boxed.** `silence_duration` is a ceiling, not an estimate of
  how long the investigation takes. If the agent crashes or the daemon is
  killed, the silence still expires on its own and a genuinely unresolved
  alert comes back — Apiary never suppresses an alert indefinitely.
- **Only on dispatch.** A skipped item is never silenced: it was not picked
  up, so hiding it would be hiding an alert nobody is looking at.
- **At most one per fire cycle.** Re-acknowledging the same alert (a retried
  step, a re-dispatch) does not stack a second silence. A silence that
  Alertmanager *rejected* is not recorded as done, so a later acknowledge
  retries it.
- **The alert leaves the poll.** Silenced alerts are excluded from
  `GET /api/v2/alerts`, so a silenced alert stops being returned until the
  silence lapses. It does not re-dispatch when it comes back: the task ID is
  still `fingerprint:startsAt` for that same fire cycle, and the persisted
  task identity dedups it.
- **Requires write access.** The configured token needs permission to POST
  silences. If Alertmanager rejects the request the error is logged and the
  run continues — a silence failure never fails the investigation.

## Dispatching per group instead of per alert

One bad deploy fires `HighErrorRate` on twelve pods. By default that is twelve
tasks and twelve agent runs investigating the same incident. Alertmanager
already groups those alerts — its `group_by` is exactly "what the on-call
should be paged about once" — and `dispatch_by: group` follows that grouping:

```yaml
sources:
  - id: prod-alerts
    type: prometheus
    config:
      alertmanager_url: https://alertmanager.internal:9093
      dispatch_by: group        # default is "alert"
```

The source then polls `GET /api/v2/alerts/groups` and emits one task per
non-empty group, with every member alert rendered into the task body.

### Group → task mapping

| Task field | Group value |
|---|---|
| ID | `<group key>:<epoch>` — see below |
| Number | 7-char hash of the group key |
| Title | group's `alertname` + member count (`HighErrorRate (12 alerts)`) |
| Description | group key, receiver, group labels, then each member alert in full |
| Labels | the group-by labels, **plus** any label every member shares with the same value |
| Type | `alert_group` |
| Priority | the worst `severity` among the members |
| URL | the first member's `generatorURL` |
| Metadata | group key, epoch, receiver, group labels, member count, member fingerprints |

The label rule is what makes trigger matching work: `severity` is usually not
a `group_by` label, but if every member is `critical` then
`labels: [severity:critical]` matches the group. If members disagree, the
label is not group-wide and is left off.

### Group identity and the fire cycle

A single alert has a natural per-cycle identity (`fingerprint:startsAt`). A
group does not: its membership churns constantly while an incident unfolds, so
there is no timestamp on it that stays put.

Apiary pins one. When a group first becomes non-empty, its **epoch** is set to
the earliest `startsAt` among its members and then held for the whole cycle:

```
14:02  alert A fires    -> group non-empty, epoch := 14:02, dispatch
14:09  alert B joins    -> same id, no re-dispatch
14:20  alert A resolves -> same id, no re-dispatch   <- churn absorbed
14:44  group empties    -> cycle ends, pin dropped
15:10  alert C fires    -> epoch := 15:10, new dispatch
```

Deriving the epoch from the current members on every poll instead would make
the id jump the moment the oldest alert resolved — re-dispatching an incident
that was still being investigated. Pinning avoids that.

The pin is in-memory. After a daemon restart the epoch is re-seeded from the
current members' earliest `startsAt`, which reproduces the previous value
whenever the oldest member is still firing — the usual case for an ongoing
incident, so the investigation is not duplicated. It differs only if that
oldest member resolved during the restart, which produces one new task for the
ongoing group.

### Interaction with the other options

- **`max_new_per_poll`** counts *groups*, not member alerts.
- **`min_age`** is measured against the group's epoch, so a just-formed group
  is deferred. Maturing does not change its id.
- **`ack_via_silence`** silences on the group-wide labels, so acknowledging
  suppresses the whole group rather than one member.
- **`interrupt_on_resolve`** treats a group as resolved when it is empty or
  gone; the same two-confirmation debounce and fail-closed rules apply.

## Interrupting a run when the alert resolves

Sometimes the alert clears on its own — a node came back, a deploy rolled
back — and the investigation still running against it is now pointless work
holding an agent slot. `interrupt_on_resolve` stops those runs.

It is a **source-level** field, a sibling of `poll_interval`, not part of
`config`:

```yaml
sources:
  - id: prod-alerts
    type: prometheus
    interrupt_on_resolve: true
    config:
      alertmanager_url: https://alertmanager.internal:9093
```

Off by default, and deliberately so: an investigation's findings usually
outlive the alert that prompted them, and a flapping alert would otherwise
kill a run that was nearly done.

When enabled, every poll cycle checks the alerts behind all in-flight
instances of that source and stops the ones whose alert is gone. The check
is conservative by design:

- **Suppressed is not resolved.** The check queries Alertmanager for *every*
  alert, including silenced and inhibited ones. This matters most with
  `ack_via_silence`: the silence Apiary creates on dispatch would otherwise
  make the alert look resolved and interrupt the very investigation that
  created it.
- **Two confirmations.** An alert must look gone on two consecutive checks
  before anything is stopped, so one transient empty response — an
  Alertmanager that just restarted and has not been re-fed by Prometheus yet
  — cannot interrupt every running investigation at once. The cost is one
  poll interval of latency.
- **Fails closed.** If Alertmanager cannot be reached the error is logged and
  nothing is stopped. "Could not tell" is never treated as "resolved".
- **Parked runs count too.** Instances waiting at an approval or a `wait_for`
  step are stopped as well — they are as pointless to keep alive as a
  running one.

A stopped instance is marked `interrupted`, exactly like `apiary stop`. It is
not a failure, and it can be replayed later with `apiary resume` if the alert
turns out to matter after all.

Only sources whose adapter can distinguish a resolved item from an invisible
one accept the flag; `apiary validate` rejects it on any other source type
rather than letting it sit there doing nothing.

## Where results go

Since the adapter cannot comment on an alert, the natural output of an
investigation workflow is a *ticket* source: have the agent emit
`APIARY_PUBLISH` (write findings back to a bound item) or `APIARY_SPAWN`
(create follow-up work, optionally materialized as a GitHub sub-issue).
Alert in from Prometheus, findings out as a GitHub issue.
