# Tasks

- [x] Define queue, job, attempt, worker, capability, affinity, and concurrency contracts.
- [x] Add SQLite schema and atomic enqueue/claim/heartbeat/complete/reclaim operations.
- [x] Replace in-memory fan-out admission with durable job enqueueing.
- [x] Add the embedded local worker and preserve single-command local mode.
- [x] Add authenticated worker protocol endpoints and remote worker client.
- [x] Enforce capability matching, worker capacity, and scoped concurrency limits.
- [x] Propagate cancellation and implement readiness, health, drain, and graceful shutdown.
- [x] Expose queue/worker/job status through CLI and daemon status APIs.
- [x] Document delivery guarantees, recovery, affinity, deployment, and operations.
- [x] Add restart, reclaim, exclusivity, compatibility, concurrency, cancellation, and drain tests.
- [x] Run repository gates, GitNexus change detection, and archive the change.
