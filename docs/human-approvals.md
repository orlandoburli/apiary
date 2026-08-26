# Human-in-the-loop approvals

An approval step parks its workflow until a person answers it. The request and
every response are durable, auditable, and safe to retry.

There are two shapes. Reach for the first unless you actually need the second.

| | Answered by | Use when |
|---|---|---|
| **Operator gate** | the dashboard or `apiary approve` | apiary runs on your machine — the default |
| **Multi-party gate** | named approvers, quorum, signed webhook | several people must sign off, or approvals arrive from another system |

## The operator gate

No approvers, no source signals — just a question:

```yaml
workflows:
  - id: production-release
    steps:
      - id: authorize
        type: approval
        message: Approve the production release.
        timeout: 24h
      - id: deploy
        agent: release-engineer
        action_class: deploy
```

The workflow parks at `authorize` and stays parked across daemon restarts.
Nothing resumes it but an answer or the `timeout`, so a gate without a timeout
waits indefinitely — usually what you want, and `apiary validate` warns about it
so it is never accidental.

### Answering from the terminal

```bash
$ apiary approvals
  REQUEST                            WORKFLOW         STEP           PARKED    EXPIRES
  wf-8a31:authorize                  release          authorize      12m       in 23h

$ apiary approve wf-8a31:authorize
✓ Approved — workflow resuming
```

`apiary approvals <request-id>` shows one request in detail, including the
fields it expects. `apiary reject <request-id> --comment "..."` refuses it.

Exit codes make the commands scriptable:

| Code | Meaning |
|---|---|
| `0` | the gate is resolved; the workflow is resuming |
| `3` | recorded, but the gate still waits (a quorum gate) |
| `4` | unknown request, or one that was already answered |
| `1` | anything else — transport or validation |

### Answering from the dashboard

On a parked instance — in the task detail or the workflow monitor:

| Key | Action |
|---|---|
| `y` | approve (opens the form instead when the step declares fields) |
| `n` | reject |
| `a` | open the form |

## Asking a question, not just yes/no

Declare `fields` and the gate collects typed answers alongside the decision:

```yaml
- id: pick-rollout
  type: approval
  message: Release 2.4 is staged. How should it go out?
  fields:
    - name: strategy
      label: Rollout strategy
      type: choice
      options: [canary, blue_green, full]
      required: true
    - name: change_ticket
      type: string
      required: true
  timeout: 24h
```

Field types are `string`, `text`, `boolean`, `number`, and `choice`.

Submitted values enter workflow memory as `memory.<field>`, together with
`memory.approval_decision` and `memory.approval_feedback` — so a choice field
decides what runs next:

```yaml
- id: canary-deploy
  agent: release-engineer
  if: ${{ memory.strategy == 'canary' }}

- id: full-deploy
  agent: release-engineer
  if: ${{ memory.strategy == 'full' }}
```

In the dashboard, `a` opens a form: arrow keys move between fields, a choice is
selected with `←`/`→` or its number, a boolean toggles with space, `⏎` approves,
`^r` rejects, `esc` cancels.

From the CLI, fields are prompted for on a terminal and passed as flags off one:

```bash
# interactive — walks the fields
apiary approve wf-8a31:pick-rollout

# scripted — every field supplied, no prompts
apiary approve wf-8a31:pick-rollout \
  --field strategy=canary --field change_ticket=OPS-482
```

Off a terminal, a missing required field is an error rather than a prompt, so an
unattended approval fails fast instead of hanging on stdin.

A **rejection never collects fields**. Refusing a change should not require
filling in its change ticket; only `--comment` is recorded, as
`memory.approval_feedback`.

## Multi-party gates

Naming `approvers` changes the gate's character: source signals are ignored
(source comments carry no verified author), and responses are authorized against
the list.

```yaml
- id: authorize
  type: approval
  message: Approve the production release.
  approvers: [alice, carol]
  required_approvals: 2
  delegates:
    alice: [bob]
  remind_after: 2h
  escalate_after: 8h
  escalate_to: [release-managers]
  timeout: 24h
```

A rejection ends the gate immediately; approvals wait until
`required_approvals` distinct approver slots respond. A delegate answers the slot
named by `for_approver`, so a delegate and their principal cannot both count
toward the quorum.

`settings.approvals.require_for` is a validation-time policy: a step carrying a
listed `action_class` must directly follow or depend on an approval step, and an
unsafe configuration is rejected before execution.

### Signed webhooks

For approvals arriving from another system, the daemon socket exposes:

- `GET /approvals?status=pending`
- `POST /approvals/<request-id>/respond` — the local dashboard/CLI channel
- `POST /approvals/<request-id>/webhook` — signed integrations

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
and invalid field types/options.

## Who answered

Every response records an actor — `$USER` from the dashboard and the CLI — and it
appears in the execution timeline.

On the local channel this is **provenance, not authentication**: apiary runs on
your machine, and anyone who can reach the daemon socket can already control the
daemon. The actor is checked against a list only when the step declares
`approvers`. Cross a trust boundary with the signed webhook, and put the socket
behind an authenticated proxy with TLS and request-size/rate limits before
exposing it.

## Idempotency, recovery, and audit

Every provider delivery needs a globally unique `idempotency_key`; the CLI and
dashboard derive a stable one, so a retried command records an answer once rather
than twice. Responses are unique per request and approver slot, and SQLite
transactions ensure concurrent channels cannot count the same approver twice or
advance the workflow past its quorum more than once.

A response is durable the moment it is persisted. The workflow advance that
follows runs in the background — a gate followed by a long agent step returns to
the caller immediately rather than holding the connection open — and a response
persisted immediately before a crash is applied once the parked workflow is
rehydrated.

The execution timeline records `approval.requested`, `approval.reminder`,
`approval.escalated`, `approval.granted`, `approval.rejected`, and
`approval.timed_out`. Actor, channel, feedback, form values, request identity, and
escalation targets are retained in redacted event metadata.
