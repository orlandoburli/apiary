# Webhook Source

The `webhook` source adapter is **push-mode**: anything that can POST JSON —
Alertmanager `webhook_configs`, Loki ruler, Elastic Watcher, a CI script, a
`curl` in a cron job — delivers events to the daemon, and each accepted event
becomes an Apiary task routed through normal workflow trigger matching
(labels, states, `title_regex`). It is the push counterpart of the
[Prometheus](prometheus-source.md) and [Dynatrace](dynatrace-source.md) poll
sources.

Two pieces work together:

- **`settings.webhook.listen`** starts one daemon-wide HTTP listener. Every
  push-capable source is mounted at `POST /webhook/{source-id}`; `GET
  /healthz` answers 200 for probes.
- **`sources[].type: webhook`** declares a receiving source and owns its own
  authentication, payload format, and storm limits.

## Configuration

```yaml
settings:
  webhook:
    listen: 127.0.0.1:8090        # daemon-wide inbound listener

sources:
  - id: alert-hooks
    type: webhook
    poll_interval: 60s            # fallback cadence; deliveries dispatch immediately
    config:
      secret: ${WEBHOOK_SECRET}
      auth: bearer                # bearer (default) | hmac | none
      format: alertmanager        # generic (default) | alertmanager
    filters:
      labels: ["severity=critical"]

workflows:
  - id: investigate-alert
    trigger:
      match: { source: alert-hooks }
      once: true
    steps:
      - id: investigate
        agent: sre-investigator
```

A `webhook` source without `settings.webhook.listen` is rejected at
validation — it could never receive anything.

### `config` fields

| Field | Required | Description |
|---|---|---|
| `secret` | yes, unless `auth: none` | Shared secret used by the selected auth mode |
| `auth` | no | `bearer` (default): senders put the secret in `Authorization: Bearer …`. `hmac`: senders sign each delivery (below). `none`: accept everything — explicit opt-out, only behind a trusted network boundary |
| `format` | no | `generic` (default) or `alertmanager` (the Alertmanager `webhook_configs` payload) |
| `max_pending` | no | Bounds the in-memory queue of not-yet-dispatched events; overflow deliveries get `429` so the sender's retry re-delivers later. Default `100` |
| `max_body_bytes` | no | Per-delivery body cap; larger bodies get `413`. Default `1048576` (1 MiB) |
| `tolerance` | no | HMAC-mode timestamp window (e.g. `"5m"`). Default `5m` |

### `filters`

Applied at delivery time, before enqueueing:

| Field | Description |
|---|---|
| `labels` | Every entry (`key=value` or `key:value`) must be present on the event |
| `states` | When set, the event's state must be one of these (e.g. `["firing"]`) |

## Authentication

The listener serves plain HTTP. On anything but a loopback/private network,
put it behind a TLS-terminating reverse proxy — both auth modes assume the
transport hides headers and bodies from onlookers.

### `auth: bearer` (default)

Every delivery must carry `Authorization: Bearer <secret>`. This is what
Alertmanager's `webhook_configs` can produce natively:

```yaml
# alertmanager.yml
receivers:
  - name: apiary
    webhook_configs:
      - url: https://hooks.example.com/webhook/alert-hooks
        http_config:
          authorization:
            credentials: <the source's secret>
```

### `auth: hmac`

For senders that can compute a signature (scripts, watchers, your own
services). Each delivery carries:

```
X-Apiary-Timestamp: <unix seconds>
X-Apiary-Signature: sha256=<hex of HMAC-SHA256(secret, "<timestamp>.<raw body>")>
```

Replay protection is built in: the timestamp is bound into the signature and
must be within `tolerance` of the daemon's clock, and a signature already
seen inside the window is rejected. Example sender:

```bash
ts=$(date +%s)
body='{"title":"deploy failed","labels":{"service":"api"}}'
sig=$(printf '%s.%s' "$ts" "$body" | openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" -r | cut -d' ' -f1)
curl -X POST "https://hooks.example.com/webhook/alert-hooks" \
  -H "X-Apiary-Timestamp: $ts" \
  -H "X-Apiary-Signature: sha256=$sig" \
  -d "$body"
```

## Payload formats

### `format: generic`

One JSON object per event; batches as a top-level array or an
`{"events": [...]}` envelope. All fields optional:

| Field | Maps to |
|---|---|
| `id` | Task identity — a sender that retries the same `id` never dispatches twice while the first run lives. Defaults to a hash of the body |
| `title` (or `summary`) | Task title. Default `webhook event <id>` |
| `description` | Task description body (the full payload is always appended as JSON) |
| `labels` | Routable labels; object `{"k":"v"}` or array `["k:v", "k=v"]` |
| `priority` (or `severity`) | Task priority |
| `state` | Task state; default `open` |
| `url` | Deep link back to the sender |

### `format: alertmanager`

The Alertmanager webhook payload (version 4). Each **firing** alert in the
delivery becomes one task with exactly the same mapping and identity scheme
as the [prometheus poll source](prometheus-source.md)
(`fingerprint:startsAt`, alert labels as `key:value` task labels, summary and
annotations rendered into the description) — so switching a fleet from poll
to push keeps dedup semantics. `resolved` notifications are ignored.

## Behavior

- **Immediate dispatch** — a delivery wakes the source's poll loop, so
  routing happens right away; `poll_interval` is only the fallback cadence.
- **Exactly-once per event ID** — re-deliveries of an event still queued are
  dropped; re-deliveries after dispatch are shadowed by the persisted
  task/instance dedup, like any polled item.
- **Storm guard** — the pending queue is bounded (`max_pending`); overflow
  gets `429 queue full, retry later` and is never silently dropped.
- **Read-only source** — like the other monitoring sources, webhook events
  accept no write-backs: `Acknowledge`/`WriteResult` are no-ops, and
  validation rejects workflows that need `set_state`, label writes,
  approvals, or `wait_for: ci` against this source.

### Response codes

| Code | Meaning |
|---|---|
| `202` | Accepted; body reports `{"accepted":N,"dropped":M}` |
| `400` | Unparseable payload |
| `401` | Auth failed (bad token, bad/stale/replayed signature) |
| `405` | Not a POST |
| `413` | Body over `max_body_bytes` |
| `429` | Queue full — retry later |

## Where results go

Same answer as the other read-only monitoring sources: nowhere on the
sender's side. Give the workflow a step that publishes findings — a comment
or issue via `APIARY_PUBLISH`, or a spawned follow-up task via
`APIARY_SPAWN` — on a ticket source (GitHub, Jira, Plane).
