# Design: structured execution events

## Event envelope

Every event uses schema version `1` and carries a stable type, UTC timestamp,
optional correlation IDs, and JSON metadata:

```json
{
  "id": 42,
  "schema_version": 1,
  "type": "route.selected",
  "timestamp": "2026-07-22T12:00:00Z",
  "task_id": "task_...",
  "workflow_id": "implementation",
  "workflow_instance_id": "wf_...",
  "step_id": "implement",
  "attempt_id": "128",
  "metadata": {"reason": "labels=agent:engineer"}
}
```

New optional metadata fields are backward compatible within version 1. Removing
or changing field meaning requires a schema-version increment. Consumers must
ignore unknown event types and metadata keys.

## Persistence and live delivery

SQLite is the source of truth. Recording an event inserts it first, then fans it
out best-effort to bounded in-process subscriber channels. Slow subscribers may
miss live delivery and recover through the persisted `after_id` query.

`GET /events` supports task, instance, type, `after_id`, and limit filters.
`GET /events/stream` returns SSE, optionally replaying persisted events after an
ID before subscribing to live events.

## Emission boundaries

- Source polling/binding emits discovered, bound, and refreshed events.
- Router traces emit explicit selected/rejected reasons.
- Workflow-instance and step persistence emit lifecycle events.
- The workflow engine emits approval and task-terminal semantics.
- Runner attempt creation/completion and failover selection emit runner and
  fallback events with model/runner/failure metadata.

This keeps event creation adjacent to authoritative state changes rather than
parsing text logs after the fact.

## Redaction

Metadata is recursively copied before storage. Keys matching built-in credential
names (`token`, `secret`, `password`, `authorization`, `api_key`, and variants)
or `settings.events.sensitive_fields` are replaced with `[REDACTED]`. String
values containing common token prefixes are also redacted. Correlation IDs and
event types never contain config values.

## Retention

`settings.events.retention` is a duration, defaulting to `720h` (30 days).
Non-positive values disable automatic pruning. The daemon prunes at startup and
daily. Event deletion does not affect canonical task/workflow/step records.

## Timeline

Task history queries return events oldest-first. The CLI and TUI render timestamp,
event type, and concise typed metadata from this stream, so the timeline does not
depend on parsing log prose.
