# Multi-channel human approvals

## Motivation

Approval gates currently depend on source-state polling and carry only a binary
decision. Production workflows need durable, authorized requests that can be
answered through multiple channels without advancing twice.

## Scope

- Declare approvers, timeout, escalation, and typed form fields on approval steps.
- Persist approval requests and idempotent responses with a complete event audit.
- Support the terminal dashboard and a provider-neutral signed webhook channel.
- Expose rejection feedback and submitted fields to subsequent workflow logic.
- Keep transport concerns behind an approval-provider interface.

Slack, Teams, and email adapters are deferred; they can implement the same
provider interface after the provider-neutral contract is stable.
