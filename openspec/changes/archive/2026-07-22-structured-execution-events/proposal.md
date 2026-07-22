# Structured execution events

## Why

Apiary's text logs are useful for diagnostics but cannot reliably reconstruct
why a task was routed, which fallback was chosen, or how lifecycle transitions
relate across tasks, workflow instances, steps, and runner attempts.

## What changes

- Add a versioned, append-only execution-event schema with task, workflow,
  instance, step, and runner-attempt correlation IDs.
- Emit structured events for discovery/binding, routing decisions, workflow and
  step lifecycle, runners/fallbacks, approvals, and task completion/escalation.
- Redact built-in secret keys and configured sensitive metadata fields before
  events are persisted or broadcast.
- Support persisted filtering and live Server-Sent Events over the local socket.
- Use the same event stream in task CLI history and the terminal dashboard.
- Prune events according to a documented retention setting.

## Scope

Webhook and OpenTelemetry exporters are deferred. The local persisted/query/live
contract is designed as their future source so exporters do not require another
instrumentation model.

## Tracking

Closes GitHub issue #211.
