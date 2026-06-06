# Tasks: Internal Task Model

Implementation follows 9 phases. Each phase produces a shippable state and must pass `go test ./...` before the next begins. Read `proposal.md` and `design.md` before starting.

**Key references:**
- `design.md` — data model, Go types, affected components, all Mermaid diagrams
- `proposal.md` — behavioral spec, config schema, ERP use case walkthrough

---

## Phase 1 — Foundation: Types & DB Schema

Introduce the new Go types and database tables with no behavioral change. Existing execution paths are untouched.

### 1.1 Go Types

- [ ] 1.1.1 Create `src/internal/model/task.go`: `InternalTask`, `TaskState` constants, `TaskMetadata`, `SourceBinding`, `SpawnRequest` structs (see `design.md` → Go Types)
- [ ] 1.1.2 Create `src/internal/model/source_item.go` as a copy of `cell.go` with type renamed to `SourceItem` (keep `cell.go` temporarily with a `// Deprecated: use SourceItem` comment — removed in Phase 2)
- [ ] 1.1.3 Unit tests: zero-value constructors, JSON round-trip on `TaskMetadata.Input`

### 1.2 DB Schema — new tables

- [ ] 1.2.1 Add `internal_tasks` table to `src/internal/db/schema.go` (columns: `id`, `parent_task_id`, `title`, `description`, `input`, `state`, `metadata`, `outstanding_workflows`, `created_at`, `updated_at`)
- [ ] 1.2.2 Add `source_bindings` table (columns: `id`, `task_id`, `source_id`, `source_item_id`, `source_item_url`, `source_item_number`, `created_at`; unique constraint on `(source_id, source_item_id)`)
- [ ] 1.2.3 Add migration: `ALTER TABLE workflow_instances ADD COLUMN task_id TEXT REFERENCES internal_tasks(id)` (nullable during migration)
- [ ] 1.2.4 Add migration: `ALTER TABLE step_runs ADD COLUMN publish_payload TEXT`, `publish_state TEXT`, `spawned_task_id TEXT REFERENCES internal_tasks(id)`
- [ ] 1.2.5 Implement `InternalTaskStore` in `src/internal/db/task_store.go`: `CreateTask`, `GetTask`, `UpdateTaskState`, `IncrementOutstanding`, `DecrementOutstanding`, `ListTasksByState`
- [ ] 1.2.6 Implement `SourceBindingStore` in same file or `binding_store.go`: `CreateBinding`, `GetBindingBySourceItem`, `ListBindingsByTask`
- [ ] 1.2.7 Unit tests: CRUD round-trip for both stores against a real in-memory SQLite

---

## Phase 2 — SourceItem Rename

Complete the `Cell` → `SourceItem` rename across the entire codebase. No behavioral change.

### 2.1 Rename

- [ ] 2.1.1 Delete `src/internal/model/cell.go`; confirm all references now import `SourceItem` from `task.go`
- [ ] 2.1.2 Update `src/internal/source/source.go`: `Adapter.Poll` returns `[]model.SourceItem`; update `Acknowledge`, `WriteResult`, `TaskPoller.PollTask` signatures
- [ ] 2.1.3 Update all source adapters (`github/adapter.go`, `plane/adapter.go`): rename `toCell` → `toSourceItem`, update return types
- [ ] 2.1.4 Update `src/internal/router/router.go`: rename `Route(cell model.Cell)` → `Route(item model.SourceItem)` (single-match, still used internally in Phase 3 transition)
- [ ] 2.1.5 Update `src/internal/daemon/dispatcher.go` and `workflow.go`: replace all `Cell` references with `SourceItem`
- [ ] 2.1.6 Update `src/internal/workflow/engine.go` and all workflow files: replace `Cell` with `SourceItem`
- [ ] 2.1.7 Full grep check: `grep -rn "model\.Cell\b" src/` must return empty
- [ ] 2.1.8 Run `go test ./...` — must stay green (no behavioral change)

---

## Phase 3 — Binding Layer

Introduce `SourceBinder` between the adapter poll and the dispatcher. The binder creates or retrieves an `InternalTask` for each `SourceItem`. Dispatch still goes to a single workflow (fan-out comes in Phase 4).

### 3.1 SourceBinder

- [ ] 3.1.1 Create `src/internal/source/binder.go`: `SourceBinder` interface (`Bind(ctx, SourceItem) (InternalTask, error)`) and `DefaultSourceBinder` struct
- [ ] 3.1.2 Implement `Bind`: lookup `source_bindings` by `(source_id, source_item_id)`; if found, fetch and return the existing `InternalTask`; if not, create `InternalTask` + `SourceBinding` in a transaction
- [ ] 3.1.3 Wire `SourceBinder` into `src/internal/daemon/dispatcher.go`: call `binder.Bind(item)` after each poll item; pass the resulting `InternalTask` forward (still wrapping in a synthetic SourceItem struct for the router for now — cleaned up in Phase 4)
- [ ] 3.1.4 Unit tests: new item creates task + binding; same item on second poll returns existing task; concurrent bind with same `(source_id, source_item_id)` deduplicates correctly (unique constraint handling)

---

## Phase 4 — Router Fan-out

Replace first-match-wins dispatch with all-matches dispatch. Introduce `trigger.exclusive`.

### 4.1 Router Changes

- [ ] 4.1.1 Add `Exclusive bool` to `TriggerConfig` in `src/internal/config/config.go`
- [ ] 4.1.2 Add `RouteAll(task model.InternalTask) []Match` to `src/internal/router/router.go`: evaluates all triggers in priority order; stops after first exclusive match; returns all matches
- [ ] 4.1.3 Unit tests: two non-exclusive triggers both match; exclusive trigger stops evaluation; priority ordering is respected

### 4.2 Dispatcher Fan-out

- [ ] 4.2.1 Update `src/internal/daemon/dispatcher.go`: call `router.RouteAll(task)` instead of `router.Route(item)`; call `dispatchWorkflow` for each match
- [ ] 4.2.2 Increment `task.outstanding_workflows` by the number of dispatched workflows before any starts
- [ ] 4.2.3 Unit tests: one task dispatches to two workflows when two triggers match; exclusive trigger dispatches only one

---

## Phase 5 — Engine: InternalTask & SourceBindings

Update the workflow engine to operate on `InternalTask` + `[]SourceBinding` instead of `SourceItem`. Side-effects resolve via bindings.

### 5.1 Engine Signature

- [ ] 5.1.1 Update `WorkflowEngine.RunInstance` signature: `RunInstance(ctx, wf WorkflowConfig, task model.InternalTask) (instanceID, success, err)` — remove `SourceItem` parameter
- [ ] 5.1.2 Update `workflow_instances` write path: store `task_id` (not `source_item_id + source_id`)
- [ ] 5.1.3 Update `wfSideEffects`: resolve the source adapter by looking up `source_bindings` for the task instead of reading `SourceItem.SourceID` directly
- [ ] 5.1.4 Update `StateLock`, `PostComment`, `ApplyHook` to fan-out to all bindings (today: one binding per task — this is a no-op change in behavior but correctness for future multi-binding tasks)
- [ ] 5.1.5 Update `CheckParkedApprovals`: resolve the `TaskPoller` adapter via `source_bindings` instead of `cell.SourceID`
- [ ] 5.1.6 Integration test: engine runs a workflow against a task with one binding; side-effects call the correct adapter

---

## Phase 6 — APIARY_PUBLISH

Replace `result_comment` config-driven write-back with the agent-driven `APIARY_PUBLISH` marker. Old config is deprecated with a warning.

### 6.1 Marker Parsing

- [ ] 6.1.1 Add `APIARY_PUBLISH_BEGIN` / `APIARY_PUBLISH_END` block parsing to `src/internal/runner/structured.go` (alongside existing `APIARY_OUTPUT` and `APIARY_SUMMARY` parsers); extract text payload into `RunResult.PublishPayload`
- [ ] 6.1.2 Respect `StepConfig.Publish`: if `"off"`, clear `PublishPayload` before the engine sees it even if the agent emitted the marker

### 6.2 Write-back Execution

- [ ] 6.2.1 In the engine step completion path: if `RunResult.PublishPayload` is non-empty, call `SideEffects.PostComment` for each `SourceBinding` on the task; if the task has no bindings, silently skip
- [ ] 6.2.2 Persist `publish_payload` and `publish_state` (`sent`/`failed`/`skipped`) in the `step_runs` row
- [ ] 6.2.3 Add `Publish string` (`"auto"` | `"off"`) to `StepConfig`; default `"auto"`

### 6.3 Deprecation

- [ ] 6.3.1 Emit a `log.Warn` at config load when `result_comment` is set to any non-default value: `result_comment is deprecated; use APIARY_PUBLISH marker in agent output instead`
- [ ] 6.3.2 Keep `result_comment: on_complete` and `result_comment: per_step` functional (fallback path) until a future removal change

### 6.4 Tests

- [ ] 6.4.1 Unit test: agent output with `APIARY_PUBLISH` block → `PostComment` called once per binding
- [ ] 6.4.2 Unit test: `publish: off` suppresses write-back even when marker is present
- [ ] 6.4.3 Unit test: task with no source bindings — publish silently skipped, no error

---

## Phase 7 — APIARY_SPAWN & WorkflowSpawner

Introduce the internal fan-out path: a workflow step emits `APIARY_SPAWN` and the engine creates a child `InternalTask` and dispatches the named workflow.

### 7.1 WorkflowSpawner

- [ ] 7.1.1 Create `src/internal/workflow/spawner.go`: `WorkflowSpawner` interface and `DefaultSpawner` struct
- [ ] 7.1.2 Implement `Spawn(ctx, SpawnRequest) (InternalTask, error)`: create `InternalTask` with `parent_task_id` + `input` JSON; call `router.RouteAll` on the new task; dispatch matched workflows via `Dispatcher`
- [ ] 7.1.3 Wire `WorkflowSpawner` into `WorkflowEngine` via an interface field (keeps engine testable in isolation)

### 7.2 Marker Parsing & Engine Integration

- [ ] 7.2.1 Add `APIARY_SPAWN_BEGIN` / `APIARY_SPAWN_END` block parsing in `structured.go`; parse content as JSON into `RunResult.SpawnRequest` (`workflow`, `title`, `input` fields)
- [ ] 7.2.2 In the engine step completion path: if `RunResult.SpawnRequest` is set, call `spawner.Spawn`; record the returned `child_task_id` in `step_runs.spawned_task_id`
- [ ] 7.2.3 Default behavior: fire-and-forget (step does not block on child outcome)
- [ ] 7.2.4 Add `Spawn string` (`"auto"` | `"await"`) to `StepConfig`; default `"auto"`
- [ ] 7.2.5 Implement `spawn: await`: step waits until the spawned task reaches a terminal state; if child fails, step fails

### 7.3 Tests

- [ ] 7.3.1 Unit test: step emits `APIARY_SPAWN` → child `InternalTask` created with correct `parent_task_id` and `input`; named workflow dispatched
- [ ] 7.3.2 Unit test: `spawn: await` — step completes only after child task reaches `done`; child `failed` fails the parent step
- [ ] 7.3.3 Unit test: invalid JSON in spawn marker → step-level error, workflow fails with descriptive message
- [ ] 7.3.4 Unit test: `APIARY_SPAWN` on a task with no matching workflow → error, not a silent no-op

---

## Phase 8 — Task Completion Hook & Config

Fire `on_complete` / `on_fail` at the InternalTask level once all outstanding workflows finish. Introduce `tasks:` top-level config block.

### 8.1 Config

- [ ] 8.1.1 Add `TasksConfig` struct (`OnComplete *OnComplete`, `OnFail *OnComplete`) to `src/internal/config/config.go`
- [ ] 8.1.2 Add `Tasks *TasksConfig` to top-level `Config` struct; parse from `tasks:` YAML key
- [ ] 8.1.3 Validate: `tasks.on_complete` and `tasks.on_fail` follow the same rules as per-workflow hooks

### 8.2 Outstanding Counter & Hook

- [ ] 8.2.1 On each `WorkflowInstance` reaching a terminal state, call `db.DecrementOutstanding(task_id)`; read back the updated count
- [ ] 8.2.2 When the count reaches 0 and a `TasksConfig` is set: apply `on_complete` (if all instances succeeded) or `on_fail` (if any failed) to every `SourceBinding` on the task
- [ ] 8.2.3 Unit test: two workflows on one task — hook fires only after both reach terminal state; correct hook applied (complete vs fail)
- [ ] 8.2.4 Unit test: task with no source bindings — hook runs without error (nothing to apply)

---

## Phase 9 — Dashboard & Observability

Update the dashboard to surface InternalTask as the primary unit, with lineage and source bindings visible.

### 9.1 Task View

- [ ] 9.1.1 Update the Tasks tab in the TUI/dashboard to show `InternalTask` rows (not raw workflow instances)
- [ ] 9.1.2 Display `SourceBinding` references per task (source name, item number, URL) alongside the task detail
- [ ] 9.1.3 Display `parent_task_id` as a lineage link: show parent task title and a breadcrumb path to root
- [ ] 9.1.4 Show all `workflow_instances` linked to a task (fan-out: multiple instances per task now possible)

### 9.2 Lineage Tree (stretch)

- [ ] 9.2.1 Add a tree view showing parent → child task relationships (Incident → Collect → Staff → Fix A/B/C)
- [ ] 9.2.2 Each node shows: task title, state badge, source binding if any, workflow instances count

---

## Cross-Cutting

- [ ] X.1 Update `openspec/specs/schema/spec.md`: add `internal_tasks`, `source_bindings` tables; `tasks:` top-level config block; `trigger.exclusive`, `step.publish`, `step.spawn` fields; `APIARY_PUBLISH` and `APIARY_SPAWN` marker spec
- [ ] X.2 Update `openspec/specs/plugin-api/spec.md`: `Adapter.Poll` returns `[]SourceItem`; new optional `SourceBinder` interface; update `TaskPoller` signature
- [ ] X.3 Update example config (`apiary.yaml` or `.apiary/example.yaml`) to show fan-out triggers, `tasks:` completion hook, and an `APIARY_SPAWN`-based workflow chain
- [ ] X.4 Run `gitnexus analyze` after each phase PR to keep the knowledge graph current
- [ ] X.5 Run `go test ./...` green after every phase before opening the next PR
