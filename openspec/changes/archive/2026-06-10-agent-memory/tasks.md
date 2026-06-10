# Tasks: Agent Memory

Implementation follows 5 phases. Each phase produces a shippable state and must pass `go test ./...` before the next begins. Read `proposal.md` and `design.md` before starting.

**Key references:**
- `design.md` — store API, integration points with file:line anchors, security notes, edge cases
- `proposal.md` — behavioral spec, marker protocol, config schema, on-disk layout

---

## Phase 1 — Store & Config Foundation

New package and config plumbing; nothing wired into execution yet.

### 1.1 Memory store

- [x] 1.1.1 Create `src/internal/memory/store.go`: `Store`, `Open`, `Entry`/`EntryMeta`/`Note` types, slug validation regex, atomic write helper (tmp + rename)
- [x] 1.1.2 Implement `UpsertGlobal` (frontmatter render, create-vs-update `created`/`updated` handling) and index regeneration into `MEMORY.md`
- [x] 1.1.3 Implement `AppendTaskNote` with last-note content-hash dedup
- [x] 1.1.4 Implement `RebuildIndex` (parse `global/*.md` frontmatter; tolerate hand-edited/broken files by skipping with a warning)
- [x] 1.1.5 Implement `List` / `Read` / `Delete`
- [x] 1.1.6 Unit tests: round-trip, upsert semantics, slug rejection (traversal attempts), size cap, dedup, index self-healing after manual file deletion

### 1.2 Config

- [x] 1.2.1 Add `MemorySettings` to `Settings` in `src/internal/config/config.go` with defaults (enabled=false, path=`<data-dir>/memory`, max_inject_chars=4000, max_entry_bytes=16384, task_retention=720h)
- [x] 1.2.2 Extend `MemoryConfig` in `src/internal/config/workflow.go` with `Recall []string` and `Memorize string`; default helpers (`RecallTiers()`, `MemorizeEnabled()`)
- [x] 1.2.3 Lint: recall enum (`task`/`global`), memorize enum (`auto`/`off`), retention duration parse, positive size caps
- [x] 1.2.4 Regenerate `schema/apiary.json` and update `.apiary/example-*.yaml` mirrors
- [x] 1.2.5 Unit tests: lint accept/reject cases; defaults applied

---

## Phase 2 — Marker Parsing

- [x] 2.1 Add `APIARY_MEMORIZE_BEGIN/END` constants and block extraction in `src/internal/runner/execution/structured.go` (object-or-array, mirroring spawn)
- [x] 2.2 Add `model.MemorizeRequest`; extend `RunResult` with `MemorizeRequests` / `MemorizeError`; wire in `applyStructured`
- [x] 2.3 Markers always stripped from visible output, even when memory is disabled
- [x] 2.4 Unit tests in `structured_test.go`: single object, array, malformed JSON → `MemorizeError`, marker stripping, defaults (`scope` omitted → `task`)

---

## Phase 3 — Engine Write Path

- [x] 3.1 Construct `*memory.Store` in daemon startup when enabled; pass to engine (capability-style, like `TaskTracker`)
- [x] 3.2 Implement `memorizeStep()` in `src/internal/workflow/engine.go` settle path, ordered before `publishStep`; gate on enabled + `memory.memorize`
- [x] 3.3 Validation failures and `MemorizeError` surface as step warnings; provenance (agent, task, workflow, step) stamped from step context
- [x] 3.4 Tests: end-to-end engine test — step output with marker → file on disk with correct frontmatter; disabled → no file; `memorize: off` → no file; oversized content → warning, no file

---

## Phase 4 — Recall & Env

- [x] 4.1 Implement `RenderRecall` in the store: `[Long-term Memory]` (protocol line + index, truncation marker) and `[Task Memory]` (self + ancestors via `GetTaskAncestors`, oldest-dropped budget)
- [x] 4.2 Prepend recall to `req.MemoryDoc` in `ExecuteStep` (`src/internal/daemon/workflow.go`); degrade to instance doc on store error
- [x] 4.3 Respect `step.memory.read=false` (suppresses everything) and `step.memory.recall` tier filtering
- [x] 4.4 Add `APIARY_MEMORY_DIR` to `stepEnv()` identity base when enabled
- [x] 4.5 Tests: budget truncation, ancestor inheritance order, empty-store zero-overhead, env var presence/override precedence

---

## Phase 5 — Lifecycle, CLI & Docs

### 5.1 Pruning

- [x] 5.1.1 Implement `Prune` (terminal + retention, descendant-aware keep, mtime fallback for unknown task IDs)
- [x] 5.1.2 Wire sweep at daemon `Start()` (next to startup reconcile) + 6h ticker
- [x] 5.1.3 Tests: terminal-old pruned, in-flight descendant retained, unknown-id mtime path

### 5.2 CLI

- [x] 5.2.1 `apiary memory path|list|show|rm|prune [--dry-run]` subcommands
- [x] 5.2.2 `RebuildIndex` on CLI store open; human-readable list output

### 5.3 Docs & rollout

- [x] 5.3.1 New docs page `docs/memory.md` (tiers, marker protocol, config, curation) + mkdocs nav entry
- [x] 5.3.2 Update `docs/workflows.md` memory section to name the instance tier and link out
- [x] 5.3.3 Update apiary-guide skill + example soul-file snippet teaching the memorize protocol and "no secrets" rule
- [x] 5.3.4 Best-effort secret-pattern warning on memorize content (`ghp_`, `xoxb-`, `AKIA`)
- [x] 5.3.5 Gitignore guidance: add `**/.apiary/memory/` to this repo's `.gitignore` and to whatever `apiary init` emits; document the "commit it deliberately" alternative
- [x] 5.3.6 Archive change: move CHANGELOG entry from Ativas to Arquivadas
