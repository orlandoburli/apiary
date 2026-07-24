# Durable Queue and Distributed Workers

Apiary persists every daemon-mode workflow dispatch in SQLite before a worker
can execute it. The default remains a single `apiary run` command: it starts an
embedded worker against that durable queue. Separate workers are optional.

## Delivery and recovery

Delivery is **at least once**. Enqueueing uses a unique
`task:generation:workflow` key, so polling the same source item repeatedly does
not create duplicate jobs in one dispatch generation. A claim creates a fenced
attempt with an opaque token and a time-bounded lease. Workers extend that lease
with heartbeats. If a worker disappears, the control plane expires the attempt
and makes the job available again; a late worker cannot heartbeat or finish the
new attempt with its stale token.

This means workflow actions should remain idempotent. A process can fail after
performing an external action but before acknowledging completion, causing a
retry. Apiary's existing workflow instance, publish, spawn, and hook
idempotency protections still apply.

Cancellation is cooperative. Canceling an instance marks its queued or leased
job, and the next attempt heartbeat cancels the runner context. A cancellation
wins over a simultaneous successful finish.

## Local mode

No queue configuration is required:

```yaml
settings:
  concurrency: 2
```

The embedded worker has capacity 1 unless `worker_capacity` is set. Set
`embedded_worker: false` only after at least one remote worker is available.

## Remote workers

Expose the versioned worker protocol from the control plane:

```yaml
settings:
  queue:
    listen: 0.0.0.0:8080
    worker_token: ${APIARY_WORKER_TOKEN}
    embedded_worker: false
    lease_duration: 30s
    heartbeat_interval: 10s
    worker_timeout: 30s
```

`worker_token` is mandatory whenever `listen` is configured. Remote worker
traffic must be encrypted; use one of the two options below.

### Option A — Native TLS

Provide a PEM certificate and key (absolute paths or relative to `apiary.yaml`):

```yaml
settings:
  queue:
    listen: 0.0.0.0:8443
    worker_token: ${APIARY_WORKER_TOKEN}
    tls_cert_file: /etc/apiary/tls.crt
    tls_key_file:  /etc/apiary/tls.key
    embedded_worker: false
```

When both fields are present the listener starts with TLS (minimum TLS 1.2).
`apiary worker` auto-derives `https://` from `settings.queue.listen` when TLS is
configured, so `--control-plane` is optional for co-located workers.

### Option B — TLS-terminating reverse proxy

Run the listener on a loopback address and terminate TLS externally (nginx,
Caddy, a private service mesh):

```yaml
settings:
  queue:
    listen: 127.0.0.1:8080
    worker_token: ${APIARY_WORKER_TOKEN}
    embedded_worker: false
```

Workers point at the proxy's HTTPS address:

```sh
apiary worker --control-plane https://apiary.example \
  --token "$APIARY_WORKER_TOKEN"
```

Start a worker with the same `apiary.yaml` and runner credentials (choose either
option above for encryption; `--control-plane` accepts both `http://` and
`https://` URLs):

```sh
apiary worker --control-plane https://apiary.example \
  --token "$APIARY_WORKER_TOKEN" --id linux-build-01 \
  --pool build --label linux --capability docker --capacity 4
```

Each remote worker stores detailed workflow/step execution history in
`.apiary/workers/<worker-id>.db`. The control plane stores the canonical queue,
terminal workflow summary, and task state. If the final response is lost, the
control plane reconciles terminal queue jobs at startup.

## Scheduling

Agents can require a pool, labels, capabilities, and task-level workspace
affinity:

```yaml
agents:
  - id: engineer
    runner: codex
    model: gpt-5
    worker_pool: build
    requires_labels: [linux]
    requires_capabilities: [docker]
    workspace_affinity: true
```

The queue automatically requires `apiary.workflow` and the configured runner
adapter (for example `runner:codex-cli`). A worker must satisfy every required
label and capability. Affinity pins later claims for the task to the first
worker until explicitly released; it is useful for non-portable workspaces but
reduces failover options if that worker is unavailable.

Concurrency limits are checked transactionally against active leases. Zero or
an omitted value means unlimited; a keyed value overrides its default:

```yaml
settings:
  queue:
    concurrency:
      default_project: 12
      default_agent: 4
      agents:
        deployer: 1
      runners:
        codex-cli: 6
      pools:
        production: 1
```

Limits are available for project, source, agent, runner, and pool. Worker
capacity is enforced independently.

## Operations

`apiary status` reports job counts and registered workers, including health,
capacity, active jobs, and drain/readiness state. Graceful worker shutdown sets
drain first, waits for active jobs, and then marks the worker unready. An
ungraceful shutdown is detected from the stale worker heartbeat and expired job
leases; work is then reclaimed automatically.

For safe operations:

1. Keep heartbeat interval comfortably below the lease duration.
2. Use stable, unique worker IDs; affinity is stored against that ID.
3. Run the control plane database on durable storage and back up
   `.apiary/apiary.db`.
4. Rotate the Bearer token by restarting the control plane and workers within a
   controlled drain window.
5. Monitor queued/leased job growth and stale worker heartbeats.
