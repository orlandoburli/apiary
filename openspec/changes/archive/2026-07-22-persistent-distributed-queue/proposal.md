# Persistent queue and distributed worker protocol

## Motivation

The dispatcher currently launches one in-memory goroutine per matched workflow.
Agent semaphores, active-run state, and cancellation handles exist only in that
process, so queued work disappears on restart and multiple Apiary processes
cannot enforce one scheduling policy or safely reclaim abandoned execution.

## Scope

- Add a queue abstraction and SQLite implementation for local mode.
- Persist self-contained dispatch jobs before execution and deliver them at least once.
- Claim jobs transactionally with attempt identity, leases, heartbeats, and
  compare-and-set completion/requeue/cancellation.
- Register workers with labels, capabilities, capacity, readiness, drain state,
  health, and current jobs; claim only compatible work.
- Enforce concurrency limits per project, source, agent, runner, and worker pool
  inside queue admission rather than process-local semaphores.
- Preserve workspace affinity for retries/resumes and propagate cancellation to
  the active worker.
- Run an embedded local worker by default so `apiary run` remains one command.
- Expose a versioned worker HTTP/JSON protocol suitable for remote workers.

Postgres/Redis queue implementations, artifact/workspace transfer, automatic
worker provisioning, and cross-region scheduling are deferred. The abstraction
and protocol leave those backends possible without changing workflow semantics.
