# Human-in-the-loop approvals

Apiary approval steps can collect authorized, structured responses through the
terminal dashboard or a provider-neutral signed webhook. Requests and individual
responses are durable, auditable, and safe to retry.

## Workflow declaration

```yaml
settings:
  approvals:
    webhook_secret: ${APIARY_APPROVAL_SECRET}
    require_for: [push, deploy, destructive, external_publication]

workflows:
  - id: production-release
    steps:
      - id: authorize
        type: approval
        message: Approve the production release.
        approvers: [alice, carol]
        required_approvals: 2
        delegates:
          alice: [bob]
        fields:
          - name: change_ticket
            label: Change ticket
            type: string
            required: true
          - name: maintenance_window
            type: choice
            options: [now, scheduled]
            required: true
        remind_after: 2h
        escalate_after: 8h
        escalate_to: [release-managers]
        timeout: 24h
      - id: deploy
        agent: release-engineer
        action_class: deploy
```

Field types are `string`, `text`, `boolean`, `number`, and `choice`. A rejection
ends the gate immediately; approvals wait until `required_approvals` distinct
approver slots respond. A delegate answers the slot named by `for_approver`, so a
delegate and their principal cannot both count toward the quorum.

`settings.approvals.require_for` is a validation-time policy. A step carrying a
listed `action_class` must directly follow or depend on an approval step. Apiary
rejects an unsafe configuration before execution.

## Dashboard and webhook responses

The task detail and workflow monitor show the pending request. For a request
without structured fields, press `y` to approve or `n` to reject. The local OS
username is the actor and must match an approver or configured delegate.

Provider-neutral endpoints on the daemon socket are:

- `GET /approvals?status=pending`
- `POST /approvals/<request-id>/respond` for the local dashboard channel
- `POST /approvals/<request-id>/webhook` for signed integrations

Webhook JSON:

```json
{
  "decision": "approve",
  "actor": "bob",
  "for_approver": "alice",
  "idempotency_key": "provider-delivery-0192",
  "feedback": "Proceed during the scheduled window.",
  "values": {
    "change_ticket": "OPS-482",
    "maintenance_window": "scheduled"
  }
}
```

Sign the exact request body with HMAC-SHA256 using `webhook_secret` and send the
hex digest as `X-Apiary-Signature: sha256=<digest>`. Apiary rejects missing or
invalid signatures, unknown actors, invalid delegation, missing required fields,
and invalid field types/options. The socket can be exposed through an authenticated
reverse proxy or consumed by a provider adapter; it should not be made public
without TLS and request-size/rate limits.

## Idempotency, recovery, and audit

Every provider delivery needs a globally unique `idempotency_key`. Responses are
also unique per request and approver slot. SQLite transactions ensure concurrent
channels cannot count the same approver twice or advance the workflow past its
quorum more than once. A response persisted immediately before a process crash is
applied after the parked workflow is rehydrated.

The execution timeline records `approval.requested`, `approval.reminder`,
`approval.escalated`, `approval.granted`, `approval.rejected`, and
`approval.timed_out`. Actor, channel, feedback, form values, request identity, and
escalation targets are retained in redacted event metadata.

Submitted values and rejection feedback enter workflow memory as
`memory.<field>`, `memory.approval_decision`, and `memory.approval_feedback`, so
subsequent conditions and agent prompts can act on the human response.

Legacy approval steps without `approvers` continue to use source comments, labels,
or state changes. When `approvers` is present, unauthenticated source signals are
ignored because source comment records do not carry a verified author identity.
