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
  write back to: `Acknowledge` and result write-back are no-ops, and the
  adapter implements none of the optional write capabilities.
  `apiary validate` rejects workflows that pin this source and use
  `on_complete.set_state` / `add_labels`, approval steps, `wait_for` CI
  steps, or `materialize: sub_issue`.
- **Resolved while running.** A running investigation is not interrupted
  when its alert resolves; the workflow finishes normally.

## Where results go

Since the adapter cannot comment on an alert, the natural output of an
investigation workflow is a *ticket* source: have the agent emit
`APIARY_PUBLISH` (write findings back to a bound item) or `APIARY_SPAWN`
(create follow-up work, optionally materialized as a GitHub sub-issue).
Alert in from Prometheus, findings out as a GitHub issue.
