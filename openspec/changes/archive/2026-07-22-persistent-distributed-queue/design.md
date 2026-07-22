# Design: persistent distributed queue

## Durable model

`dispatch_jobs` stores the immutable dispatch snapshot and mutable scheduling
state (`queued`, `leased`, `succeeded`, `failed`, `canceled`). The snapshot
contains task/source/workflow/agent/runner identifiers, normalized source item,
route/worker configuration, required labels/capabilities, affinity key, and
concurrency scopes. A deterministic idempotency key prevents polling from
enqueuing the same active task/workflow twice.

`dispatch_attempts` stores every claim. A claim transaction selects one compatible
job whose scopes have capacity, marks it leased, and inserts an attempt with a
random claim token and expiry. Heartbeat, completion, failure, cancellation, and
lease extension require `(job_id, attempt_id, claim_token)` and the current
leased state. A stale worker therefore cannot finish a reclaimed job.

`worker_registrations` stores worker protocol version, labels, capabilities,
pool, capacity, readiness/drain state, heartbeat, and current-job count. Expired
workers are unhealthy and their attempts become reclaimable after lease expiry.

## Delivery and recovery

Delivery is at least once. Lease expiry transitions the abandoned attempt to
expired and requeues the job in one transaction. Exactly one later claimant can
hold the active lease because the job row is updated with a compare-and-set
predicate. Execution side effects continue to require existing Apiary
idempotency keys; the queue guarantees no two valid active claim tokens, not
exactly-once external effects.

Control-plane restart loses no queued/leased row. On startup the recovery loop
requeues expired leases and the embedded worker resumes claims. Graceful drain
marks the worker draining, stops new claims, waits for active claims, and leaves
unfinished claims leased for another worker to reclaim after shutdown/expiry.

## Scheduling

Workers advertise a set of labels/capabilities; required job values must be a
subset. An optional affinity key binds follow-up attempts to the same worker;
when that worker is permanently unavailable an explicit affinity-release action
is required, avoiding silent execution in a different workspace.

Concurrency usage is derived transactionally from active leases and checked
against configured limits for project, source, agent, runner, and pool. Worker
capacity is an additional hard limit. This makes admission consistent across all
workers sharing a backend.

## Protocol and local mode

Worker protocol `1` exposes register, heartbeat/readiness, claim, attempt
heartbeat, complete/fail, cancellation polling, and drain endpoints. Every
request is authenticated by a configured worker token. Enqueue, registration,
heartbeat, drain, and cancellation are idempotent; claim completion is fenced by
its opaque attempt token, so a replay after acknowledgement is rejected as stale.

Local mode constructs the same worker client in-process against the SQLite queue
and starts it with the dispatcher. No external service or configuration is
required. Remote mode disables or supplements the embedded worker and serves the
same queue through the daemon API.

Remote workers keep detailed workflow and step state in a worker-local SQLite
database. A terminal completion writes an idempotent summary to the canonical
control-plane database and settles task accounting. Startup reconciliation heals
terminal jobs whose acknowledgement or settlement response was interrupted.
