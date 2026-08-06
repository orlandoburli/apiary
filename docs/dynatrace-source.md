# Dynatrace Source

The `dynatrace` source adapter polls the **Dynatrace problems API**
(`GET /api/v2/problems`) and maps each open problem to an Apiary task — so an
AI-detected operational problem, not just a human-filed ticket, can trigger a
workflow. The canonical pattern: Dynatrace opens a problem (`Response time
degradation` on service X) → an investigation workflow dispatches → the agent
pulls logs, diagnoses, and publishes findings as a GitHub issue or fix PR.

Problems are polled (not individual events or metrics) because Davis has the
correlation semantics already applied: one problem groups the root cause and
every impacted entity, so a cascading failure is one task, not fifty.

## Configuration

```yaml
sources:
  - id: prod-problems
    type: dynatrace
    poll_interval: 30s
    config:
      base_url: https://abc12345.live.dynatrace.com
      api_token: ${DYNATRACE_API_TOKEN}   # scope: problems.read
      max_new_per_poll: 10                # optional, storm cap
      min_age: 1m                         # optional, flap dampener
      lookback: 720h                      # optional, from= window
    filters:
      labels: ["severity=availability", "managementZone=Prod"]

workflows:
  - id: investigate-problem
    trigger:
      match: { source: prod-problems }
      once: true
    steps:
      - id: investigate
        agent: sre-investigator
```

### `config` fields

| Field | Required | Description |
|---|---|---|
| `base_url` | yes | Environment URL — SaaS (`https://{env-id}.live.dynatrace.com`) or Managed (`https://{host}/e/{env-id}`) |
| `api_token` | yes | Access token with the `problems.read` scope, sent as `Authorization: Api-Token …` |
| `max_new_per_poll` | no | Problem-storm cap: at most this many *not-yet-seen* problems become tasks per poll; the overflow is logged and surfaces on later polls. `0` disables the cap. Default `10` |
| `min_age` | no | Flap dampener: a problem must have been open at least this long before it is ingested. Default `1m` |
| `lookback` | no | How far back the poll's `from=` window reaches. The problems API defaults to the last 2 hours, which would hide older still-open problems, so the adapter always sends an explicit window. Default `720h` (30 days) |

### `filters`

| Field | Description |
|---|---|
| `labels` | Mapped to `problemSelector` criteria, ANDed and evaluated server-side. `severity=availability` / `impact=services` / `managementZone=Prod` map to the corresponding selector functions; any other `key=value` (or `key:value`) pair matches an entity tag (`entityTags("key:value")`); a raw criterion (`displayId("P-123")`) is passed through untouched; a bare word matches the problem title (`text(…)`) |
| `states` | Not applicable — only open problems are polled; any value other than `open` is ignored with a warning |

## Problem → task mapping

| Task field | Problem value |
|---|---|
| ID | `problemId` — unique per problem occurrence; a later occurrence of the same failure gets a fresh ID |
| Number | `displayId` (e.g. `P-2145`) |
| Title | problem title |
| Description | severity/impact, root cause entity, affected entities, management zones, tags, open-since timestamp |
| Labels | `severity:<severityLevel>`, `impact:<impactLevel>`, `zone:<each management zone>`, plus every entity tag as `key:value` — so `trigger.match.labels: [severity:availability]` works verbatim |
| Type | `problem` |
| Priority | the severity level, lowercased (`availability`, `error`, `performance`, …) |
| State | `open` |
| URL | deep link to the problem details view |
| Metadata | problemId, displayId, severityLevel, impactLevel, status, startTime, management zones, entity tags |

## Behavior

- **Exactly once per occurrence.** The task ID is the Dynatrace `problemId`,
  which is unique per problem occurrence: while the problem stays open the ID
  is constant and the dispatcher's dedup (active-instance drop, `once: true`,
  persisted task identity) prevents re-dispatch — including across daemon
  restarts. When the problem resolves and the failure later recurs, Dynatrace
  opens a new problem with a fresh ID, producing a new task and a new
  dispatch.
- **Storm cap.** One bad deploy can open dozens of problems in a single poll;
  each would become a task and an agent run. `max_new_per_poll` bounds how
  many new problems are admitted per poll (oldest first); the rest are
  deferred to subsequent polls and a warning names the deferred count.
- **Flap dampening.** `min_age` skips problems younger than the threshold, so
  open→resolved blips never dispatch.
- **Read-only.** Problems have no assignable state or labels to write back
  to: `Acknowledge` and result write-back are no-ops, and the adapter
  implements none of the optional write capabilities. `apiary validate`
  rejects workflows that pin this source and use `on_complete.set_state` /
  `add_labels`, approval steps, `wait_for` CI steps, or
  `materialize: sub_issue`. Posting problem comments (`problems.write`) is a
  possible future opt-in.
- **Resolved while running.** A running investigation is not interrupted when
  its problem resolves; the workflow finishes normally.

## Where results go

Since the adapter cannot comment on a problem, the natural output of an
investigation workflow is a *ticket* source: have the agent emit
`APIARY_PUBLISH` (write findings back to a bound item) or `APIARY_SPAWN`
(create follow-up work, optionally materialized as a GitHub sub-issue).
Problem in from Dynatrace, findings out as a GitHub issue.
