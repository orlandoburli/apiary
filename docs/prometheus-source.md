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
| `ack_via_silence` | no | When `true`, acknowledging a dispatched alert creates an Alertmanager silence for it, so it stops paging while an agent investigates. Default `false` (acknowledge is a no-op) |
| `silence_duration` | no | How long an `ack_via_silence` silence lasts. Default `2h` |

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
  subsequent polls and a warning names the deferred count.
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
- **Resolved while running.** A running investigation is not interrupted
  when its alert resolves; the workflow finishes normally.

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

## Where results go

Since the adapter cannot comment on an alert, the natural output of an
investigation workflow is a *ticket* source: have the agent emit
`APIARY_PUBLISH` (write findings back to a bound item) or `APIARY_SPAWN`
(create follow-up work, optionally materialized as a GitHub sub-issue).
Alert in from Prometheus, findings out as a GitHub issue.
