# Structured execution events

Apiary records a versioned, append-only event timeline in SQLite. The timeline
is the supported way to reconstruct task execution without parsing service or
runner logs.

## Envelope and compatibility

Every event has `id`, `schema_version`, `type`, and `timestamp`, plus optional
`task_id`, `workflow_id`, `workflow_instance_id`, `step_id`, `attempt_id`, and
`metadata`. Schema version `1` is additive: consumers must ignore unknown fields
and unknown event types. A future breaking envelope change will increment
`schema_version`; stored rows retain their original version.

Lifecycle types include task discovery and binding, route selection/rejection,
workflow and step start/terminal states, runner start/stop, rate-limit detection,
and fallback selection. Route and fallback events carry a human-readable
`reason` in metadata.

## Query and live subscription

The daemon's local Unix-socket HTTP API exposes:

- `GET /events?task=<id>&instance=<id>&type=<type>&after_id=<id>&limit=<n>`
  for chronological persisted events. The maximum page size is 1000.
- `GET /events/stream` with the same filters for Server-Sent Events. Existing
  rows after `after_id` are replayed before live delivery. Reconnect with the
  last received event ID to recover from a slow or disconnected subscriber.

`apiary task history` and the dashboard task detail view read this same event
store and render a chronological timeline.

## Redaction and retention

Metadata is recursively redacted before persistence and live delivery. Apiary
always redacts common credential keys and recognizable GitHub, Slack, bearer,
and AWS access-token values. Extend key redaction in configuration:

```yaml
settings:
  events:
    retention: 720h
    sensitive_fields: [customer_key, signing_material]
```

Key matching is case-insensitive and ignores `_`, `-`, and `.`. The default
retention is 720 hours (30 days). A zero or negative duration disables pruning.
Pruning runs at daemon startup and daily. Retention deletes event rows only; it
does not alter workflow, step, execution, or task records.

Events currently remain local to Apiary. External webhook and OpenTelemetry
exporters can be layered on the versioned envelope without changing producers.
