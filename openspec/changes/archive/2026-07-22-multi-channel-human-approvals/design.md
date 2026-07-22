# Design: multi-channel human approvals

## Decisions

- `approval_requests` is the durable request aggregate; `approval_responses`
  stores immutable channel deliveries and distinct approver slots.
- Response insertion and quorum transition share one SQLite transaction.
- `(request_id, approver)` and global `idempotency_key` uniqueness provide
  channel-independent exactly-once counting.
- Rejection is terminal immediately; approval is terminal at the configured
  quorum. Timers use compare-and-set updates so reminder/escalation events emit once.
- The workflow engine owns state advancement and memory contribution. Transports
  implement `ApprovalProvider`; dashboard/webhook handlers validate and persist.
- A persisted terminal response is replayed when a parked instance is rehydrated.
- Sensitive action policies are validated statically through `action_class` and
  `settings.approvals.require_for`.

## Authorization

Direct actors occupy their own approver slot. A delegate may occupy only its
configured principal's slot. Signed webhook authentication proves channel origin;
the actor/delegation policy authorizes the decision. Dashboard responses travel
over the local Unix socket and use the OS username as actor.

## Compatibility

All new YAML fields are optional. Approval steps without explicit approvers keep
legacy source-trigger behavior. Explicit approvers disable unverified source
signals. Existing execution-event consumers tolerate the new additive event types.
