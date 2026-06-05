# Tasks: Workflow Mode

Implementation follows the 4-phase plan from `design.md`. Complete each phase fully before starting the next — each phase produces a shippable state.

---

## Phase 1 — Schema Parsing

Parse the `workflows:` block alongside existing `routes:`. No execution changes. A defined workflow that is not yet executed emits a startup warning.

### 1.1 Config Structs

- [x] 1.1.1 Add `WorkflowConfig`, `StepConfig`, `TriggerConfig`, `SplitBranch`, `ApprovalTrigger`, `StepOutcome`, `MemoryConfig`, `OutputSchema`, `SchemaField` structs (in new file `src/internal/config/workflow.go` to keep `config.go` focused)
- [x] 1.1.2 Add `Workflows []WorkflowConfig` field to top-level `Config` struct
- [x] 1.1.3 Update YAML unmarshaling to parse `workflows:` block (struct tags + parse round-trip test)

### 1.2 Config Validation

(in new files `workflow_validate.go` + `workflow_graph.go`, hooked into `Config.Validate()`)

- [x] 1.2.1 Validate workflow IDs are unique and do not conflict with route IDs
- [x] 1.2.2 Validate step graph: all `depends_on` references point to existing step IDs within the same workflow
- [x] 1.2.3 Validate step graph is a DAG (cycle detection); allow only declared `on_fail.goto` back-edges pointing to ancestors
- [x] 1.2.4 Validate all `agent` fields in steps reference a defined agent ID
- [x] 1.2.5 Validate all `branches[].goto`, `on_fail.goto`, and `on_pass.next` references point to existing step IDs
- [x] 1.2.6 Validate `memory.write` fields exist as top-level properties in the step's `output_schema`
- [x] 1.2.7 Validate `output_schema` is a supported JSON Schema subset (top-level object; string, number, integer, boolean, array, object properties; enum on string only; arrays require `items` — arrays ARE supported because foreach consumes them; no `$ref`)
- [x] 1.2.8 Validate split steps: `multi: false` requires exactly one `else`/fallback branch; `type: agent` steps require `agent` field; `type: approval`/`split`/`workflow` steps must not have `agent` field
- [x] 1.2.9 Validate `on_fail.max_retries` is present and ≥ 1 when `on_fail.goto` is set
- [x] 1.2.10 Validate `foreach.items` dot-path resolves to an `array` type in the referenced step's `output_schema`; concurrency 1–16; max_items 1–200; inner step must be `agent`
- [x] 1.2.11 Validate sub-workflow references: `type: workflow` step must reference an existing workflow ID; referenced workflow must not contain `type: workflow` steps; no self-reference
- [x] 1.2.12 Validate `resume: auto` only when all steps are `idempotent: true`
- [x] 1.2.13 Write validation unit tests covering all new cases (44 cases)

### 1.3 `apiary validate` Update

- [x] 1.3.1 Update `apiary validate` to report workflow validation errors with step-level context (e.g. `workflows[1] "bad-wf": step "s1": agent "ghost-agent" not defined`)
- [x] 1.3.2 Update `apiary validate` to warn when a workflow is defined but no trigger is set and it is not referenced as a sub-workflow (`Config.WorkflowWarnings()`)

### 1.4 Tests

- [x] 1.4.1 Unit tests: valid workflow parses correctly (`workflow_parse_test.go` — full YAML round-trip)
- [x] 1.4.2 Unit tests: all validation error cases produce correct messages
- [x] 1.4.3 Run full test suite: `go test ./...`

---

## Phase 2 — Single-Step Workflow Engine

Implement `WorkflowEngine` behind `settings.experimental.workflow_mode: true`. Single-step workflows only (no DAG, no split, no approval, no foreach). Plain `routes:` are synthesized as single-step workflows and run through the same engine.

> **Delivery note:** Phase 2 is shipped as two PRs. **PR-2a (foundations)** delivers the SQLite store, runner-interface changes, and the memory builder — all additive, fully unit-tested, zero behavior change. **PR-2b (engine)** delivers the `WorkflowEngine` and its daemon wiring (2.4–2.5), isolated because it touches the mature dispatcher. Real layout differs from the original task notes: the store lives in the existing `internal/db` package (not `internal/store`), and schema is defined inline in `db/schema.go`.

### 2.1 SQLite Schema — PR-2a

- [x] 2.1.1 Add `workflow_instances` table (in `src/internal/db/schema.go`)
- [x] 2.1.2 Add `step_runs` table
- [x] 2.1.3 `step_runs` includes `summary` and `structured_output` columns
- [x] 2.1.4 Implement `WorkflowStore` in `src/internal/db/workflow_store.go`: CRUD for instances and step runs (+ `ReconcileOrphanWorkflowInstances`)

### 2.2 Runner Interface Changes — PR-2a

- [x] 2.2.1 Add `SystemPrepend`, `SummaryPrompt`, `StepID`, `WorkflowInstanceID` fields to `RunRequest`
- [x] 2.2.2 Add `StructuredOutput map[string]any`, `Summary string` fields to `RunResult`
- [x] 2.2.3 Inject `SystemPrepend` before cell details in `buildPrompt`; parse `APIARY_OUTPUT:` into `StructuredOutput` and `APIARY_SUMMARY_START/END` into `Summary` (shared `structured.go`, applied by cli + api runners)
- [~] 2.2.4 Script runner env vars (`APIARY_SYSTEM_PREPEND`, etc.) — deferred; the shared prompt/parse path already covers cli + api runners
- [x] 2.2.5 Runner interface tests (`structured_test.go`)

### 2.3 Memory Builder — PR-2a

- [x] 2.3.1 Implement `MemoryBuilder` in `src/internal/workflow/memory.go`: builds the memory document from cell + completed step contributions
- [x] 2.3.2 Implement `memory_max_chars` truncation (oldest summaries first; Cell and Step Data never truncated)
- [x] 2.3.3 Unit tests for `MemoryBuilder` with various step combinations

### 2.4 Workflow Engine — PR-2b

- [x] 2.4.1 Implement `Engine` in `src/internal/workflow/engine.go` (pure core; `StepExecutor`/`SideEffects`/`Store` interfaces so it's testable in isolation)
- [x] 2.4.2 Implement route synthesis: `SynthesizeWorkflow(route)` produces a single-step `WorkflowConfig`
- [x] 2.4.3 Implement instance lifecycle: create instance → run step(s) sequentially with memory threading → mark done/failed → apply on_complete/on_fail hooks
- [~] 2.4.4 Concurrency semaphore — deferred to Phase 3 (DAG/parallel); Phase 2 reuses the dispatcher's existing per-agent concurrency since each cell still runs one agent step
- [x] 2.4.5 Implement `state_lock` (once at workflow start) and `result_comment` (`on_complete`/`per_step`/`off`) — see [workflow-hooks spec](specs/workflow-hooks/spec.md)
- [x] 2.4.6 Wire engine into daemon behind `settings.experimental.workflow_mode` (new `daemon/workflow.go`; single gated branch at top of `dispatch()` — legacy path untouched when off)
- [x] 2.4.7 Integration test: engine persists instance + step_run to real SQLite (`engine_integration_test.go`)

### 2.5 Tests

- [x] 2.5.1 Unit tests: engine creates instance, runs step, marks done
- [x] 2.5.2 Unit tests: `state_lock` fires at workflow start; `result_comment` on_complete/per_step/off
- [x] 2.5.3 Integration test: engine against real `db.Client` (real-source end-to-end deferred — needs a fake source adapter, follow-up)
- [x] 2.5.4 Run full test suite: `go test ./...`

---

## Phase 3 — Full DAG Execution

Multi-step workflows, parallel steps, split steps, foreach, sub-workflows, per-step model override. Enable by default (remove feature flag).

> **Delivery note:** Phase 3 ships across PRs. **PR-3a** delivers the DAG scheduler (depends_on ordering, split routing, on_fail.goto loops, skip propagation) + the expression evaluator. **PR-3b** foreach; **PR-3c** sub-workflows + remove the experimental flag (default-on).

### 3.1 DAG Executor — PR-3a

- [x] 3.1.1 Dependency-driven scheduler over `depends_on` (`dag.go`), replacing the sequential loop in `RunInstance`
- [~] 3.1.2 Parallel step dispatch — deferred. The scheduler runs ready steps one-at-a-time in deterministic order; concurrent execution of independent steps is a pure performance optimization (same results), layered on later with the global semaphore
- [x] 3.1.3 `on_fail.goto` loop-back with per-step `retries` tracking and `max_retries` enforcement
- [x] 3.1.4 Cascade reset: a loop-back resets the goto target and all transitive dependents to `pending` (clearing their memory contributions)

### 3.2 Split Steps — PR-3a

- [x] 3.2.1 Expression evaluator in `src/internal/workflow/expr.go` (recursive-descent parser + AST eval; `cell.*`/`memory.*`/`steps.*`, `==`/`!=`/`contains`/`matches`, `and`/`or`/`not`, parens)
- [x] 3.2.2 Split step execution: evaluate branches, activate matching target(s), skip + cascade the rest
- [x] 3.2.3 Support `multi: true` fan-out
- [x] 3.2.4 Unit tests: all operators, precedence, parse + eval errors; first-match, fallback, multi, skip cascade
- [~] 3.2.5 Config-load validation of `branches[].if` syntax — deferred (needs an expr package config can import without a cycle); runtime treats an unparseable condition as non-match and logs it

### 3.3 Foreach Steps — PR-3b

- [x] 3.3.1 Foreach execution (`foreach.go`): resolve the items array from a prior step's structured output, run the inner agent step once per item
- [~] 3.3.2 `max_items` guard implemented (fails the step when exceeded); `concurrency` cap deferred with DAG parallelism — items run sequentially
- [x] 3.3.3 Prompt template rendering: `{{ as }}` / `{{ as.field }}` substitution (`renderItemTemplate`), wired through new `StepRequest.Prompt` → executor `composeSystemAppend`
- [x] 3.3.4 `fail_fast` behavior (stop after the first failing item)
- [~] 3.3.5 Aggregate exposure — `steps.<id>.exit_code` carries the failed count (so `== 0` means all passed) and a summary in memory; full `outputs[i]` array indexing in expressions deferred
- [x] 3.3.6 Unit tests: one-run-per-item, rendered prompt reaches executor, max_items guard, fail_fast, downstream-after-pass, invalid items path, template edge cases

### 3.4 Sub-Workflows — PR-3c

- [x] 3.4.1 `type: workflow` step (`subworkflow.go`): create a linked child instance (`parent_instance_id`), seeded with the parent's memory snapshot; runtime depth guard backs up the config no-nesting rule
- [x] 3.4.2 Child completion: parent step passes iff the child instance reaches `done`; child memory is not merged back (isolated)
- [x] 3.4.3 Unit tests: child runs + links to parent, child failure fails the parent step (downstream skipped), unknown reference fails, parent memory seeds child, nesting beyond depth 1 refused

### 3.5 Per-Step Model Override — done in PR-2b

- [x] 3.5.1 `step.model` resolved in `runStep` (overrides the agent's model); also applies to foreach inner steps
- [x] 3.5.2 Unit test: `TestEngine_PerStepModelOverride`

### 3.6 Remove Feature Flag — DEFERRED (needs legacy parity first)

- [ ] 3.6.1 Remove `settings.experimental.workflow_mode` — **blocked**: flipping default-on would regress legacy `dispatch()` features the engine does not yet replicate (retry scheduling, PR-review directive, classifier `assign_from_output`, `task_executions` dashboard rows). Port these into the engine path before default-on.
- [ ] 3.6.2 Update `apiary validate`/`status` to show workflow info unconditionally (after 3.6.1)

### 3.7 Tests

- [ ] 3.7.1 Integration test: 3-step sequential workflow completes, memory populated correctly
- [ ] 3.7.2 Integration test: parallel steps run concurrently, fan-in step waits for both
- [ ] 3.7.3 Integration test: split routes to correct branch based on memory field
- [ ] 3.7.4 Integration test: `on_fail.goto` loops back, respects `max_retries`
- [ ] 3.7.5 Run full test suite: `go test ./...`

---

## Phase 4 — Approvals

Approval steps that suspend a workflow instance until a human responds, driven by per-task polling. Built on the workflow engine (legacy path is not involved).

### 4.1 Source Adapter Extension — PR-4a

- [x] 4.1.1 `TaskPoller` optional interface (`PollTask(ctx, cellID) (Cell, error)`) — modeled like `StateSetter`/`LabelAdder` so it doesn't break existing adapters; `model.Cell` gains `Comments []Comment`
- [~] 4.1.2 Plane `PollTask` — deferred (GitHub covers the primary case; Plane is a focused follow-up)
- [x] 4.1.3 GitHub Issues `PollTask`: fetches the issue + its comments (`get` client helper, `comment` type), compile-time `TaskPoller` assertion, httptest-backed tests
- [x] 4.1.4 Sources without `TaskPoller` simply cannot host approvals — the engine poll func returns an error and the instance stays parked (no stub needed thanks to the optional-interface pattern)

### 4.2 Approval Engine — PR-4a

- [x] 4.2.1 Approval step suspends the run: posts the message, sets the instance to `approval_waiting`, parks the in-memory run (`enterApproval`; DAG refactored into `initDAG` + re-entrant `driveDAG`)
- [x] 4.2.2 `CheckParkedApprovals(poll)` evaluates each parked instance against the live task (`EvaluateApproval`: `comment_contains`/`label_added`/`state_changed`, resume-wins-over-abort) and resumes/aborts; daemon drives it once per poll cycle via the source's `TaskPoller` against a single long-lived engine
- [x] 4.2.3 Approval timeout: `CheckParkedApprovals` aborts the instance when `step.timeout` elapses
- [x] 4.2.4 Unit tests: condition matrix, suspend, resume-continues, abort-fails, resolve-unknown errors, check-resumes-on-comment, check-times-out, check-waits-when-no-signal
- [~] 4.2.5 Restart survival — parked runs are in-memory; on daemon restart they are reconciled to `interrupted`. DB-reconstruction resume across restarts is a follow-up (acceptable given the "reset and create new flows" direction)

### 4.3 Resume Command

- [x] 4.3.1 Implement `apiary resume <instance-id>` CLI command (and `apiary resume --workflow <id>` to target the most recent failed/interrupted instance) — engine `ResumeInstance` replays already-passed steps from cache (no re-execution, no re-fired side effects), re-evaluates splits, and continues from the failure point; daemon IPC `GET /resume/{id}` (preview) + `POST /resume/{id}` (execute on the daemon-lifetime ctx). Exit codes 2/3/4 for not-found/not-resumable/workflow-changed (PR-4c)
- [x] 4.3.2 Implement `apiary instances` CLI command — list (filters: `--workflow`/`--state`/`--limit`/`--json`) and `apiary instances <id>` step-level detail, served by daemon IPC (`GET /instances`, `GET /instances/{id}`) reading `workflow_instances` + `step_runs` (PR-4b)
- [x] 4.3.3 Show resume confirmation prompt listing skipped steps and their side effects — `ResumePreview` returns skip (already passed) / run lists; CLI prints them and prompts `[y/N]` unless `--yes` (PR-4c)

### 4.4 TUI Updates

- [x] 4.4.1 Add step-progress panel to Task Detail view (step ID, agent, state, duration) — `renderWorkflowSteps` appended to the Tasks-tab detail; `fetchTaskDetail` loads the bound instance via `db.GetLatestInstanceByCell` + `ListStepRuns` (PR-4d)
- [x] 4.4.2 Show `approval_waiting` state with message in Task Detail — instance badge + a `⏸` banner when parked (PR-4d)
- [~] 4.4.3 Workflow instances with nested step runs — surfaced in the Task Detail step panel (a task's instance + its steps) and via the `apiary instances`/`apiary instances <id>` CLI; a dedicated standalone Runs tab was not added since the detail panel + CLI cover the need (PR-4d)

### 4.5 Tests

- [~] 4.5.1 Approval step posts comment, workflow pauses, resumes on matching comment — covered by `approval_test.go` (`SuspendsAtApprovalStep`, `CheckResumesOnComment`, `ResumeContinuesWorkflow`) at engine level rather than a full daemon+source integration test
- [~] 4.5.2 Approval timeout aborts workflow — covered by `approval_test.go` `CheckTimesOut`
- [~] 4.5.3 `apiary resume` replays cached steps, continues from failure point — covered by engine unit tests (`resume_test.go`: skips-cached-and-continues, cached-memory-available-downstream, marks-prior-step-cached, re-evaluates-split) rather than a full daemon integration test (PR-4c)
- [x] 4.5.4 Run full test suite: `go test ./...` — green on `main` after each phase PR

---

## Cross-Cutting

- [x] X.1 Update `openspec/specs/plugin-api/spec.md` — added "Workflow Mode extensions (experimental)": optional `TaskPoller` interface, `Cell.Comments`, and the workflow `RunRequest`/`RunResult` fields (PR-4e)
- [x] X.2 Update `openspec/specs/schema/spec.md` — added `settings.experimental.workflow_mode` and a `workflows[]` (experimental) section with the step-type table (PR-4e)
- [x] X.3 Update `openspec/specs/cli/spec.md` with `apiary instances` and `apiary resume` commands — the spec already documents both (authored ahead of implementation); commands now match it
- [x] X.4 Add a sample workflow config — `.apiary/example-workflow.yaml` (validates with `apiary validate`), demonstrating agent/split/approval steps, memory, and `experimental.workflow_mode` (PR-4e)
- [x] X.5 Run `gitnexus analyze` after each phase to keep the knowledge graph current — done in every phase PR's git workflow
