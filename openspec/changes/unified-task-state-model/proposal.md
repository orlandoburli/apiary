# Proposal: Unified Task State Model

## Why

Apiary has **four independent status vocabularies** that nobody can hold in their head at once:

| Layer | Declared in | Values |
|---|---|---|
| Dispatch job | `internal/queue/queue.go:24` | `queued` `leased` `succeeded` `failed` `canceled` |
| Task | `internal/model/task.go:9` | `registered` `running` `approval_waiting` `done` `failed` |
| Workflow instance | `internal/db/workflow_store.go:11` | `pending` `running` `approval_waiting` `waiting` `interrupted` `done` `failed` |
| Step | `internal/db/workflow_store.go:22` | `pending` `running` `passed` `failed` `skipped` `skipped_cached` `interrupted` |

Three different words mean "accepted but not started" (`queued`, `registered`, `pending`). Three
mean "finished successfully" (`done`, `passed`, `succeeded`). Three mean "parked"
(`approval_waiting`, `waiting`, `leased`). None of the four vocabularies is a subset of another,
so every layer boundary needs an ad-hoc translation, and every one of those translations is
written by hand at the call site.

The dashboard is where the cost surfaces. `taskStatusBadge`
(`internal/dashboard/app.go:5519`) papers over the mismatch by **renaming states at render
time**: it prints `registered` as the word "queued" and `success` as "done". The badge is
therefore not a view of the state — it is a second, undocumented vocabulary layered on the
other four.

Two concrete defects follow directly from this:

1. **`registered` is indistinguishable from stuck.** A task that arrived three seconds ago and a
   task whose every workflow instance was orphaned by a daemon restart both sit in `registered`,
   and both render as "queued". Operators read the board, see "queued", and conclude the hive is
   short on workers — the recurring false alarm behind every "stuck queued task" investigation.
   The information needed to tell them apart exists (`workflow_instances.state = 'interrupted'`),
   but nothing propagates it up to the task.

2. **`failed` is not terminal, but reads as if it were.** `settings.max_attempts` re-dispatches a
   failed task, and `internal/db/task_store.go:180` flips `failed` back to `running` with a raw
   SQL `CASE`. A task therefore oscillates `failed → running → failed` while it is healthily
   retrying, and it also sits in `failed` when it has permanently given up. The distinction an
   operator actually needs — *will this be retried, or is it waiting for me?* — is not
   representable.

The root cause in both cases is the same modelling error: **the state axis and the reason axis
are conflated.** `approval_waiting`, `waiting` and `leased` are all the state "parked" with
different reasons. `skipped` and `skipped_cached` are the state "skipped" with different reasons.
Encoding the reason into the state name multiplies the vocabulary and still loses information
(`interrupted` carries no reason at all).

This blocks the kanban board work: a board's premise is that a card occupies exactly one column
and moves forward through them. Built on the current model, the "queued" column would be a lie
and cards would flicker backwards out of `failed` on every retry.

---

## What Changes

| Area | Before | After |
|---|---|---|
| **Vocabulary** | 4 disjoint sets, 15 distinct strings | 1 canonical set of 7 states, subsetted per layer |
| **Parked runs** | `approval_waiting` / `waiting` / `leased` | `blocked` + `blocked_reason` column |
| **Skipped steps** | `skipped` / `skipped_cached` | `skipped` + `skipped_reason` column |
| **Success** | `done` / `passed` / `succeeded` | `done` everywhere |
| **Not started** | `registered` / `pending` / `queued` | `queued` everywhere |
| **Retry visibility** | `failed` covers both retrying and given-up | `failed` is terminal; retry-pending is `blocked` + reason `retry_backoff` |
| **Orphan visibility** | task stays `registered`, instance is `interrupted` | interrupted instances propagate to the task as `blocked` + reason `interrupted` |
| **Layer translation** | hand-written per call site | one `state.Normalize` / `state.For` helper |
| **Dashboard badge** | renames states at render time | renders the stored state verbatim |

---

## New Concepts

| Concept | Description |
|---|---|
| **Canonical state** | One enum, `state.State`, in a new `internal/state` package. Every layer stores a value from this set; layers differ only in which subset they can reach. |
| **Blocked reason** | A separate, nullable column (`blocked_reason`) naming *why* a row is parked: `approval`, `ci`, `dependency`, `retry_backoff`, `interrupted`. Free of state semantics — purely explanatory. |
| **Terminality** | A property of a state (`state.IsTerminal`), not a guess made at each call site. `failed` becomes genuinely terminal, which is what makes it trustworthy. |

### The canonical set

| State | Meaning | Terminal |
|---|---|---|
| `queued` | Accepted; no execution has begun | no |
| `running` | Actively executing right now | no |
| `blocked` | Parked awaiting something external; see `blocked_reason` | no |
| `done` | Finished successfully | yes |
| `failed` | Finished unsuccessfully; no attempts remain | yes |
| `canceled` | Terminated by an operator | yes |
| `skipped` | Never ran by design; see `skipped_reason` (steps only) | yes |

Per-layer reachable subsets:

| Layer | Reachable states |
|---|---|
| Dispatch job | `queued` `running` `done` `failed` `canceled` |
| Task | `queued` `running` `blocked` `done` `failed` `canceled` |
| Workflow instance | `queued` `running` `blocked` `done` `failed` `canceled` |
| Step | `queued` `running` `blocked` `done` `failed` `skipped` |

`interrupted` disappears as a state and becomes `blocked` + `blocked_reason = 'interrupted'`.
This is the change that fixes defect 1: an orphaned instance is now visibly *blocked*, not
quietly *pending*, and the reason says why.

---

## Design

### 1. The `internal/state` package

```go
package state

type State string

const (
    Queued   State = "queued"
    Running  State = "running"
    Blocked  State = "blocked"
    Done     State = "done"
    Failed   State = "failed"
    Canceled State = "canceled"
    Skipped  State = "skipped"
)

type Reason string

const (
    ReasonApproval     Reason = "approval"      // human gate
    ReasonCI           Reason = "ci"            // wait_for on a CI run
    ReasonDependency   Reason = "dependency"    // wait_for kind: dependency
    ReasonRetryBackoff Reason = "retry_backoff" // failed, attempts remain
    ReasonInterrupted  Reason = "interrupted"   // execution stopped abnormally
)

// IsTerminal reports whether no further transition is possible without operator action.
func (s State) IsTerminal() bool

// Normalize maps any legacy value from any of the four old vocabularies onto the
// canonical set. Used on every read for one release cycle; see Migration.
func Normalize(legacy string) State
```

The layer-specific type aliases (`model.TaskState`, `db.InstanceState`, …) are kept as thin
aliases of `state.State` so existing signatures compile, but their constant sets are replaced by
re-exports of the canonical constants. This keeps the diff mechanical rather than sweeping.

### 2. Schema changes

```sql
ALTER TABLE internal_tasks      ADD COLUMN blocked_reason TEXT;
ALTER TABLE workflow_instances  ADD COLUMN blocked_reason TEXT;
ALTER TABLE step_runs           ADD COLUMN blocked_reason TEXT;
ALTER TABLE step_runs           ADD COLUMN skipped_reason TEXT;
```

Appended to the existing bare-`ALTER` migration list in `internal/db/schema.go`, matching how
every prior column was added.

### 3. Data migration — terminal rows only

**The migration never rewrites a row that is currently in flight.** Only terminal rows are
bulk-updated; non-terminal rows keep their legacy value until the engine next writes them, at
which point it writes canonical. `Normalize` on the read path (§1) means they render correctly
throughout, so the split is invisible to operators.

This is what makes the change safe for running work: a task that is `running`, an instance that
is `waiting` on CI, and a job that is `leased` are all left exactly as they are. Nothing under
the daemon's feet moves.

```sql
-- steps (terminal)
UPDATE step_runs SET state='done' WHERE state='passed';
UPDATE step_runs SET state='skipped', skipped_reason='cached'
  WHERE state='skipped_cached';

-- dispatch jobs (terminal)
UPDATE dispatch_jobs SET state='done' WHERE state='succeeded';
```

Deliberately **not** migrated in bulk — every one of these is a live-row state:

| Legacy value | Layer | Converted by |
|---|---|---|
| `registered` | task | next task write (dispatch, settle) |
| `pending` | instance, step | next state transition |
| `approval_waiting` | task, instance | approval resolution, or rehydration at startup |
| `waiting` | instance | `wait_for` re-check |
| `leased` | job | lease claim or expiry (a lease is granted so a worker can *execute*, so it normalizes to `running`, not to a parked state — dispatch jobs never reach `blocked`) |
| `interrupted` | instance, step | `ReconcileOrphanWorkflowInstances` at next daemon start |

`interrupted` deserves a note: it is terminal-ish but is the input to reconcile and to the orphan
propagation in §6, so converting it in bulk would race with a daemon that is mid-reconcile.
Leaving it to reconcile costs nothing — reconcile already rewrites those rows on every start.

Once a release has shipped with `Normalize` on read, a later cleanup pass may bulk-convert any
legacy stragglers. It is not required for correctness; it only shortens the tail.

`waiting → blocked/ci` remains the one lossy mapping when it does eventually convert: the old
`waiting` state covered both CI waits and dependency waits without distinguishing them.
Converted rows record `ci` (the dominant case); newly written rows record the true reason. This
is documented rather than fixed, because the information does not exist in the old rows to
recover.

### 3a. Live-daemon safety

`db.New` (`internal/db/client.go:84`) calls `InitSchema` on **every** open, and `InitSchema` runs
both the `ALTER` list and the one-shot data repairs unconditionally. Four call sites open the DB,
and one of them — `internal/cli/dashboard_cmd.go:36` — is the dashboard, which routinely runs
against the same WAL file as a live daemon.

**Opening the dashboard therefore executes data migrations against a running hive.** This is
already the case today for `repairSupersededFailedTasks`, which flips `failed → done` underneath
a live daemon. It is pre-existing behaviour, not introduced here, but this change must not
inherit it: rewriting lifecycle states under a running engine is a different order of risk from
repairing a settled task.

Three rules, all required:

1. **Additive DDL stays in `InitSchema`.** The four new columns are `ALTER TABLE … ADD COLUMN`,
   idempotent and harmless from any opener.
2. **Data migration moves out of `InitSchema`** into an explicit `migrateStates` step invoked
   only from the daemon startup path, after the daemon has taken its lock — never from a bare
   `db.New`. The dashboard, `apiary memory`, and the worker opener can no longer trigger it.
3. **It refuses to run against a live hive.** Before executing, it checks for a fresh daemon
   heartbeat and aborts with a clear error if one is present, so a second daemon or a stale
   process cannot race it.

#### The mixed-binary window

The failure this guards against is not hypothetical. With a new dashboard and an old daemon
sharing one DB, the dashboard migrates `registered → queued`; the old daemon's hardcoded filter
at `internal/db/workflow_store.go:487` —

```sql
WHERE state IN ('registered', 'running', 'approval_waiting')
```

— then no longer matches those rows, and **live tasks silently stop being dispatched**.
`Normalize` on read would make the dashboard display them perfectly while dispatch had gone
blind, which is the worst possible combination.

Restricting the bulk migration to terminal rows (§3) removes the class of the problem: no live
row changes value, so no live-row filter can miss it. Rules 2 and 3 close the remainder.

> **Resolved ahead of this change:** `InitSchema` mutating data on every DB open was filed as
> #467 and fixed first. `InitSchema` is now DDL-only and safe for any opener; the data repairs
> live in `db.MigrateData`, called by the daemon at startup and by the new `apiary migrate`
> command. Rules 1 and 2 above are therefore already in place — this change only has to put its
> state migration in `MigrateData` alongside them.
>
> Rule 3 (refuse to run against a live hive) is also in place, via #468: `MigrateData`'s callers
> probe the daemon's control socket (`/health`) and refuse when it answers. All three rules are
> therefore satisfied before this change's migration is written, and it inherits them by living
> in `MigrateData`.

### 4. Hardcoded SQL literals

State strings are embedded directly in query text in at least these places, all of which must be
rewritten in the same commit as the data migration:

| File | Lines | Literals |
|---|---|---|
| `internal/db/task_store.go` | 180-181 | `'done'` `'failed'` `'running'` |
| `internal/db/workflow_store.go` | 337, 487, 505 | `'failed'` `'interrupted'` `'registered'` `'running'` `'approval_waiting'` |
| `internal/db/queue_store.go` | 350, 362 | `'queued'` `'leased'` `'canceled'` |
| `internal/db/dashboard.go` | 623 | `'running'` |
| `internal/db/schema.go` | 668-675 | `'done'` `'failed'` |

The `workflow_store.go:487` query is the one that currently selects live tasks with
`state IN ('registered','running','approval_waiting')`; under the new model it becomes
`state NOT IN ('done','failed','canceled')`, which is both shorter and no longer silently wrong
when a new non-terminal state is added.

### 5. Retry semantics (defect 2)

`internal/db/task_store.go:180` currently flips a `failed` task back to `running` inside a
`CASE`. Replaced by an explicit transition:

- On step/instance failure with attempts remaining → task goes to `blocked` +
  `blocked_reason = 'retry_backoff'`, and the generation counter increments as it does today.
- On step/instance failure with attempts exhausted → task goes to `failed`, terminal.

`failed` now means "a human needs to look at this", which is the only reading that makes a
board column, an alert, or a CLI exit code meaningful.

### 6. Orphan propagation (defect 1)

`ReconcileOrphanWorkflowInstances` already marks orphaned running instances at daemon start. It
is extended to also set the owning task to `blocked` + `blocked_reason = 'interrupted'` when the
task has no remaining non-terminal instance. A task therefore never sits in `queued` while its
only instance is dead.

### 7. Dashboard

`taskStatusBadge` stops renaming states. It maps state → colour and nothing else, and renders
`blocked` with its reason appended where the column width allows (`blocked:ci`). The 8-character
budget is preserved: every canonical state is 6-8 characters, which is the reason the set was
chosen with short names.

---

## What Stays

- **SQLite as the store** — no engine change; see the standing decision against a Postgres port.
- **The bare-`ALTER` migration style** — this change follows it rather than introducing a
  migration framework.
- **`generation` semantics** — the counter and its role in dispatch idempotency are untouched.
- **Queue leasing mechanics** — only the *name* of the leased state changes; leases, expiry, and
  redelivery guards are unchanged.
- **Public CLI output shape** — `apiary tasks` keeps its columns; the values inside them change.
- **The kanban board** — explicitly out of scope here; it is the follow-up this change unblocks.

---

## Implementation Plan

The phases are ordered so that **every read-side change ships at least one release before the
write-side change that depends on it.** A hive that has run any prior release can therefore read
everything a later release writes, which is what makes the mixed-binary window in §3a survivable
rather than merely unlikely.

### Release N — Read-side only (no write changes, no schema changes)

1. Add `internal/state` with the enum, `IsTerminal`, and `Normalize`.
2. Re-point `model.TaskState`, `db.InstanceState`, `db.StepState`, `queue.JobState` at it, keeping
   the legacy string values as the constants' values for now.
3. Apply `Normalize` at presentation boundaries — the dashboard badge, CLI output — **not** at
   the store scan layer. Normalizing on scan would return `blocked` where engine code still
   compares against `InstanceStateWaiting`, silently breaking those comparisons; the constants do
   not carry canonical values until step 7. This is the one place the read-side release must stop
   short of "everywhere".
4. Widen every hardcoded SQL filter to accept both vocabularies — critically,
   `workflow_store.go:487` becomes `state NOT IN ('done','failed','canceled')` so it can never
   miss a live row regardless of which vocabulary wrote it.
5. Update tests to the canonical names.

Nothing writes a canonical value yet. This release is pure insurance: it teaches every binary to
understand the new vocabulary before any binary starts producing it.

### Release N+1 — Schema, then write-side

6. Add the four `blocked_reason` / `skipped_reason` columns (additive DDL, stays in `InitSchema`).
7. Flip the constants to their canonical values, so new writes are canonical.
8. Add `migrateStates` for terminal rows only (§3), invoked from the daemon startup path behind
   the daemon lock and the live-hive heartbeat check (§3a).
9. Rewrite the remaining hardcoded SQL literals listed in §4.

### Release N+1, continued — Reason plumbing

10. Set `blocked_reason` at every park site: approval gate, `wait_for` CI, `wait_for` dependency,
    queue lease.
11. Set `skipped_reason='cached'` where `skipped_cached` was written.

### Release N+1, continued — The two defects

12. Retry transition → `blocked`/`retry_backoff` instead of `failed` (§5).
13. Orphan propagation in `ReconcileOrphanWorkflowInstances` (§6).

### Release N+1, continued — Surfaces

14. `taskStatusBadge` renders verbatim; drop the rename table.
15. `apiary tasks` / `apiary instances` output.
16. Docs: `docs/concepts.md`, `docs/workflows.md`, `docs/dashboard.md`, `docs/data-model.md`.
17. JSON Schema regeneration if any state string is schema-visible.

---

## Out of Scope

- **The kanban board** — the follow-up change this one exists to make possible.
- **A migration framework** — the bare-`ALTER` list stays.
- **Per-state SLA / alerting** — a `blocked` task ageing past a threshold is a future concern.
- **Recovering the true reason for historical `waiting` rows** — the data is not there (§3).
- **Renaming `task_executions` legacy `success`/`failed` execution statuses** — that table is the
  agent-run log, a different axis from workflow lifecycle; folding it in would widen the diff
  without clarifying anything.

---

## Migration

The change is breaking for anyone reading the SQLite file directly (including `apiary-pgsink`,
which replicates these tables into Postgres and will need its column list extended and any
state-name filters updated).

| Consumer | Impact |
|---|---|
| Running tasks | **None.** The bulk migration touches terminal rows only (§3); a task that is running, an instance waiting on CI, and a leased job are left untouched and convert on their next natural transition. |
| Existing hives | Terminal-row `UPDATE` on first daemon start of release N+1, behind the daemon lock; no operator action |
| Upgrading | Install release N first, then N+1. Skipping N forfeits the read-side insurance and re-opens the mixed-binary window in §3a. |
| Downgrade | Supported back to release N (which normalizes both vocabularies). Not supported below N. Take a DB copy before upgrading. |
| Mixed binaries on one DB | Safe from release N onward, because N accepts both vocabularies on read and its live-row filters are vocabulary-agnostic. Before N, see §3a. |
| `apiary-pgsink` | Needs a matching release: new columns, both vocabularies accepted on read |
| External SQL / dashboards | Any query filtering on `'registered'`, `'passed'`, `'waiting'`, `'leased'`, `'approval_waiting'`, `'skipped_cached'`, `'succeeded'`, or `'interrupted'` must be updated. Prefer negative filters (`NOT IN ('done','failed','canceled')`) over enumerating live states. |

The two-release split is the mechanism, not a convenience: release N teaches every binary to
*read* the canonical vocabulary, release N+1 starts *writing* it. Because no live row is ever
bulk-rewritten, at no point does a task in flight change value underneath the engine.
