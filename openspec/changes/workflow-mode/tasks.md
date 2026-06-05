# Tasks: Workflow Mode

Implementation follows the 4-phase plan from `design.md`. Complete each phase fully before starting the next — each phase produces a shippable state.

---

## Phase 1 — Schema Parsing

Parse the `workflows:` block alongside existing `routes:`. No execution changes. A defined workflow that is not yet executed emits a startup warning.

### 1.1 Config Structs

- [ ] 1.1.1 Add `WorkflowConfig`, `StepConfig`, `TriggerConfig`, `SplitBranch`, `ApprovalConfig`, `ApprovalTrigger`, `StepOutcome`, `MemoryConfig` structs to `src/internal/config/config.go`
- [ ] 1.1.2 Add `Workflows []WorkflowConfig` field to top-level `Config` struct
- [ ] 1.1.3 Update YAML unmarshaling to parse `workflows:` block

### 1.2 Config Validation

- [ ] 1.2.1 Validate workflow IDs are unique and do not conflict with route IDs (`src/internal/config/validate.go`)
- [ ] 1.2.2 Validate step graph: all `depends_on` references point to existing step IDs within the same workflow
- [ ] 1.2.3 Validate step graph is a DAG (cycle detection); allow only declared `on_fail.goto` back-edges pointing to ancestors
- [ ] 1.2.4 Validate all `agent` fields in steps reference a defined agent ID
- [ ] 1.2.5 Validate all `branches[].goto` and `on_fail.goto` references point to existing step IDs
- [ ] 1.2.6 Validate `memory.write` fields exist as top-level properties in the step's `output_schema`
- [ ] 1.2.7 Validate `output_schema` is a supported JSON Schema subset (object, string, number, boolean, enum, required — no arrays at top level, no `$ref`)
- [ ] 1.2.8 Validate split steps: `multi: false` requires exactly one `else` branch; `type: agent` steps require `agent` field; `type: approval` steps must not have `agent` field
- [ ] 1.2.9 Validate `on_fail.max_retries` is present and ≥ 1 when `on_fail.goto` is set
- [ ] 1.2.10 Validate `foreach.items` dot-path resolves to an `array` type in the referenced step's `output_schema`
- [ ] 1.2.11 Validate sub-workflow references: `type: workflow` step must reference an existing workflow ID; referenced workflow must not contain `type: workflow` steps; no self-reference
- [ ] 1.2.12 Validate `resume: auto` only when all steps are `idempotent: true`
- [ ] 1.2.13 Write validation unit tests covering all new cases

### 1.3 `apiary validate` Update

- [ ] 1.3.1 Update `apiary validate` to report workflow validation errors with step-level context (e.g. `workflow "feature-dev", step "implement": agent "backend" not found`)
- [ ] 1.3.2 Update `apiary validate` to warn when a workflow is defined but no trigger is set and it is not referenced as a sub-workflow

### 1.4 Tests

- [ ] 1.4.1 Unit tests: valid workflow parses correctly
- [ ] 1.4.2 Unit tests: all validation error cases produce correct messages
- [ ] 1.4.3 Run full test suite: `go test ./...`

---

## Phase 2 — Single-Step Workflow Engine

Implement `WorkflowEngine` behind `settings.experimental.workflow_mode: true`. Single-step workflows only (no DAG, no split, no approval, no foreach). Plain `routes:` are synthesized as single-step workflows and run through the same engine.

### 2.1 SQLite Schema

- [ ] 2.1.1 Add `workflow_instances` table migration to `src/internal/store/migrations/`
- [ ] 2.1.2 Add `step_runs` table migration
- [ ] 2.1.3 Add `summary` and `structured_output` columns to `step_runs`
- [ ] 2.1.4 Implement `WorkflowStore` in `src/internal/store/workflow_store.go`: CRUD for instances and step runs

### 2.2 Runner Interface Changes

- [ ] 2.2.1 Add `SystemPrepend`, `SummaryPrompt`, `StepID`, `WorkflowInstanceID` fields to `RunRequest` — see [runner-interface spec](specs/runner-interface/spec.md)
- [ ] 2.2.2 Add `StructuredOutput map[string]any`, `Summary string` fields to `RunResult`
- [ ] 2.2.3 Update OpenCode runner: inject `SystemPrepend` before soul file; parse `APIARY_OUTPUT:` last line into `StructuredOutput`; extract summary block into `Summary`
- [ ] 2.2.4 Update script runner: inject `SystemPrepend`; pass `APIARY_SUMMARY_PROMPT` env var; parse `APIARY_OUTPUT:` line
- [ ] 2.2.5 Update runner interface tests

### 2.3 Memory Builder

- [ ] 2.3.1 Implement `MemoryBuilder` in `src/internal/workflow/memory.go`: builds the memory document from cell + completed step runs
- [ ] 2.3.2 Implement `memory_max_chars` truncation (oldest summaries first; Cell and Step Data never truncated)
- [ ] 2.3.3 Unit tests for `MemoryBuilder` with various step combinations

### 2.4 Workflow Engine

- [ ] 2.4.1 Implement `WorkflowEngine` in `src/internal/workflow/engine.go`
- [ ] 2.4.2 Implement route synthesis: plain `routes:` entries produce a single-step `WorkflowConfig` internally
- [ ] 2.4.3 Implement instance lifecycle: create → run step → complete/fail → apply hooks
- [ ] 2.4.4 Implement concurrency semaphore — see [concurrency-model spec](specs/concurrency-model/spec.md)
- [ ] 2.4.5 Implement `state_lock` and `result_comment` workflow behavior — see [workflow-hooks spec](specs/workflow-hooks/spec.md)
- [ ] 2.4.6 Wire `WorkflowEngine` into daemon; respect `settings.experimental.workflow_mode` flag
- [ ] 2.4.7 Integration test: plain route dispatches through engine, produces instance + step_run in SQLite

### 2.5 Tests

- [ ] 2.5.1 Unit tests: engine creates instance, runs step, marks done
- [ ] 2.5.2 Unit tests: `state_lock` fires at workflow start; `result_comment` posts at workflow end
- [ ] 2.5.3 Integration test: end-to-end with a real source + runner stub
- [ ] 2.5.4 Run full test suite: `go test ./...`

---

## Phase 3 — Full DAG Execution

Multi-step workflows, parallel steps, split steps, foreach, sub-workflows, per-step model override. Enable by default (remove feature flag).

### 3.1 DAG Executor

- [ ] 3.1.1 Implement topological sort of steps by `depends_on` in `WorkflowEngine`
- [ ] 3.1.2 Implement parallel step dispatch: steps ready at the same time run concurrently up to global concurrency limit
- [ ] 3.1.3 Implement `on_fail.goto` loop-back with `retry_counts` tracking and `max_retries` enforcement
- [ ] 3.1.4 Implement cascade reset: when a loop-back fires, reset all downstream steps to `pending`

### 3.2 Split Steps

- [ ] 3.2.1 Implement expression evaluator in `src/internal/workflow/expr.go` — see [proposal expression language](proposal.md#expression-language)
- [ ] 3.2.2 Implement split step execution: evaluate branches, activate matching step(s)
- [ ] 3.2.3 Support `multi: true` fan-out
- [ ] 3.2.4 Unit tests: all expression operators, multi-branch split, fallback branch, unmatched split error

### 3.3 Foreach Steps

- [ ] 3.3.1 Implement foreach step execution: read items array from step structured output, spawn sub-runs
- [ ] 3.3.2 Implement `concurrency` cap and `max_items` guard
- [ ] 3.3.3 Implement prompt template rendering (`{{ item.field }}` substitution)
- [ ] 3.3.4 Implement `fail_fast` behavior
- [ ] 3.3.5 Expose `steps.<id>.outputs`, `steps.<id>.passed_count`, `steps.<id>.failed_count` in memory/expressions
- [ ] 3.3.6 Unit tests: foreach with structured output, concurrency cap, max_items abort, fail_fast

### 3.4 Sub-Workflows

- [ ] 3.4.1 Implement `type: workflow` step: create child instance, pass memory snapshot, link via `parent_instance_id`
- [ ] 3.4.2 Implement child instance completion: mark parent step passed/failed based on child outcome
- [ ] 3.4.3 Unit tests: child instance created and linked; parent step waits for child

### 3.5 Per-Step Model Override

- [ ] 3.5.1 Read `step.model` field in engine; pass to `RunRequest.Model` overriding agent's `preferred_models[0]`
- [ ] 3.5.2 Unit test: step with `model` override uses correct model

### 3.6 Remove Feature Flag

- [ ] 3.6.1 Remove `settings.experimental.workflow_mode` flag; engine always active
- [ ] 3.6.2 Update `apiary validate` and `apiary status` to show workflow info unconditionally

### 3.7 Tests

- [ ] 3.7.1 Integration test: 3-step sequential workflow completes, memory populated correctly
- [ ] 3.7.2 Integration test: parallel steps run concurrently, fan-in step waits for both
- [ ] 3.7.3 Integration test: split routes to correct branch based on memory field
- [ ] 3.7.4 Integration test: `on_fail.goto` loops back, respects `max_retries`
- [ ] 3.7.5 Run full test suite: `go test ./...`

---

## Phase 4 — Approvals

Approval steps. Requires source-write-back polling (new `PollTask` on `SourceAdapter`).

### 4.1 Source Adapter Extension

- [ ] 4.1.1 Add `PollTask(ctx, cellID string) (Cell, error)` to `SourceAdapter` interface — see [source-adapter-watch spec](specs/source-adapter-watch/spec.md)
- [ ] 4.1.2 Implement `PollTask` in Plane adapter
- [ ] 4.1.3 Implement `PollTask` in GitHub Issues adapter
- [ ] 4.1.4 Add stub `PollTask` (returns `ErrNotSupported`) to all other adapters

### 4.2 Approval Engine

- [ ] 4.2.1 Implement approval step execution: post message via `WriteResult`, set instance to `approval_waiting`
- [ ] 4.2.2 Implement approval polling loop in `WorkflowEngine`: on each poll cycle, call `PollTask` for all `approval_waiting` instances and evaluate `resume_on` / `abort_on` conditions
- [ ] 4.2.3 Implement approval timeout: abort instance when `timeout` expires
- [ ] 4.2.4 Unit tests: approval posts message, waits, resumes on matching comment, aborts on abort condition, aborts on timeout

### 4.3 Resume Command

- [ ] 4.3.1 Implement `apiary resume <instance-id>` CLI command — see [CLI spec](../../specs/cli/spec.md)
- [ ] 4.3.2 Implement `apiary instances` CLI command
- [ ] 4.3.3 Show resume confirmation prompt listing skipped steps and their side effects

### 4.4 TUI Updates

- [ ] 4.4.1 Add step-progress panel to Task Detail view (step ID, agent, state, duration)
- [ ] 4.4.2 Show `approval_waiting` state with message in Task Detail
- [ ] 4.4.3 Update Runs tab to show workflow instances with nested step runs

### 4.5 Tests

- [ ] 4.5.1 Integration test: approval step posts comment, workflow pauses, resumes on matching comment
- [ ] 4.5.2 Integration test: approval timeout aborts workflow
- [ ] 4.5.3 Integration test: `apiary resume` replays cached steps, continues from failure point
- [ ] 4.5.4 Run full test suite: `go test ./...`

---

## Cross-Cutting

- [ ] X.1 Update `openspec/specs/plugin-api/spec.md` with new `SourceAdapter.PollTask` and updated `RunRequest`/`RunResult`
- [ ] X.2 Update `openspec/specs/schema/spec.md` with `workflows:` block and new step types
- [ ] X.3 Update `openspec/specs/cli/spec.md` with `apiary instances` and `apiary resume` commands
- [ ] X.4 Update example `apiary.yaml` in `.apiary/` to show a sample workflow
- [ ] X.5 Run `npx gitnexus analyze` after each phase to keep the knowledge graph current
