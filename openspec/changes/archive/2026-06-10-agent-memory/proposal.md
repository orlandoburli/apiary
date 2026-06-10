# Proposal: Agent Memory

## Why

Apiary agents are stateless between executions. The only memory that exists today is the **instance memory document** (`MemoryBuilder`, `step.memory.read/write`): structured fields and handoff summaries accumulated across the steps of a *single workflow instance*, injected as `SystemPrepend` and discarded when the instance reaches a terminal state.

This creates three compounding problems as agents take on longer-running and recurring work:

1. **Agents relearn the same lessons forever.** An engineer agent that discovers "this repo's pre-commit hook needs `--no-verify` when the binary is stale" or "CI takes ~12 minutes on this project" loses that knowledge the moment the instance ends. The next task pays the same exploration cost — every time.

2. **Work on the same task is amnesiac across instances.** A task that fans out to multiple workflows, retries after a failure, or spawns children via `APIARY_SPAWN` produces several workflow instances — none of which can see what the others learned or decided. A retry repeats the failed approach; a spawned child re-derives context its parent already collected (the `input` payload helps, but is fixed at spawn time and write-once).

3. **There is no curation surface.** Operators cannot inspect, correct, or prune what agents "know". Knowledge lives only in transient prompts and step outputs scattered across `step_runs` rows.

The fix is a **tiered, persistent memory system**: a daemon-wide long-term store of durable facts, plus per-task working memory that survives across instances and follows task lineage — both written by agents through a new `APIARY_MEMORIZE` output marker and stored as human-readable markdown files on disk.

---

## What Changes

| Area | Before | After |
|---|---|---|
| **Memory lifetime** | One workflow instance | Three tiers: instance (unchanged), task (across instances + lineage), global (forever) |
| **Memory storage** | In-memory, rebuilt per step | Markdown files under a memory root on disk |
| **Write path** | `step.memory.write` (declared fields only) | Plus agent-driven `APIARY_MEMORIZE` marker (like `APIARY_PUBLISH` / `APIARY_SPAWN`) |
| **Recall** | Instance doc via `SystemPrepend` | Plus task notes + global index injected; full entries readable via `APIARY_MEMORY_DIR` |
| **Curation** | None | `apiary memory` CLI (list, show, rm, prune) + plain files editable by hand |
| **Spawned children context** | Fixed `input` payload at spawn time | Plus inherited task memory from ancestor chain |

---

## New Concepts

| Term | Description |
|---|---|
| **Memory tier** | Where a memory lives and how long: `instance` (existing, unchanged), `task`, `global`. |
| **Task memory** | Append-only working notes attached to an `InternalTask`. Visible to every workflow instance of that task **and of its descendants** (via `parent_task_id` lineage). Pruned after the task is terminal + retention period. |
| **Long-term (global) memory** | One daemon-wide store of durable facts, shared by all agents, sources, and projects. One markdown file per fact, upserted by slug. Never auto-pruned — curated by humans or by agents updating entries. |
| **`APIARY_MEMORIZE` marker** | Agent-emitted block (single JSON object or array, mirroring `APIARY_SPAWN`) instructing the engine to persist a memory to a tier. |
| **Memory root** | The on-disk directory holding all persistent memory. Defaults to `<data-dir>/memory` — i.e. `.apiary/memory/` beside the config file, next to `apiary.db` (per `config.DataDir`, the single source of truth for project state). Configurable via `settings.memory.path`. Because the data dir is project-scoped, "global" means **daemon-wide**: shared across all agents, sources, and workflows served by that daemon, but naturally isolated between projects. |
| **Memory index** | `MEMORY.md` at the memory root — one line per global entry (name + description). This index, not the full entries, is what gets injected into prompts; agents read full entries from disk when needed. |
| **Memory provenance** | Frontmatter on every entry recording which agent, task, and workflow wrote it, and when. Enables future per-agent/per-project partitioning without changing the store. |

---

## What Stays

- **Instance memory** — `MemoryBuilder`, the `[Cell]` / `[Step Data]` / `[Summaries]` document, `step.memory.read` and `step.memory.write` are all unchanged. The persistent tiers compose with it; they do not replace it.
- **Markers** — `APIARY_OUTPUT`, `APIARY_SUMMARY`, `APIARY_PUBLISH`, `APIARY_SPAWN` unchanged. `APIARY_MEMORIZE` is a sibling, parsed by the same `extractStructured` pass.
- **Engine, router, sources, runners** — no behavioral change for configs that don't enable memory. The feature is **opt-in** (`settings.memory.enabled: false` by default in v1).
- **InternalTask model** — no schema change to `internal_tasks`; task memory is keyed by task ID on disk, and lineage reads reuse the existing ancestor chain (`GetTaskAncestors`).

---

## Core Behaviors

### 1. Writing memory — `APIARY_MEMORIZE`

An agent emits the marker in its step output. Single object or JSON array (same convention as `APIARY_SPAWN`):

```
APIARY_MEMORIZE_BEGIN
{
  "scope": "global",
  "name": "erp-precommit-stale-binary",
  "description": "project-erp pre-commit lint fails when the apiary binary is stale",
  "content": "When the local apiary binary predates the config schema, pre-commit lint hard-errors.\nFix: rebuild the binary or commit with --no-verify and note it in the PR."
}
APIARY_MEMORIZE_END
```

Fields:

- `scope` — `"task"` (default) or `"global"`.
- `name` — kebab-case slug. **Required for `global`** (it is the upsert key); ignored for `task`.
- `description` — one line, shown in the index. Required for `global`.
- `content` — markdown body. Required.

Engine handling, after step completion (alongside `publishStep` / `spawnStep`):

- **`global`**: upsert `global/<name>.md` (same name = overwrite body, bump `updated`, keep `created`), regenerate `MEMORY.md` index.
- **`task`**: append a timestamped, provenance-stamped note to `tasks/<task_id>.md`. Identical consecutive content (hash match) is skipped to keep retries from duplicating notes.
- Malformed JSON sets a `MemorizeError` surfaced as a step warning (mirrors `SpawnError`) — it never fails the step.
- If memory is disabled, the marker is stripped from output and silently ignored (same posture as `APIARY_PUBLISH` on a task with no bindings).

### 2. Recall — what gets injected

When memory is enabled, the daemon prepends two sections to the existing instance memory document (same `SystemPrepend` channel, before `[Cell]`):

```
[Long-term Memory]
You have a persistent memory at $APIARY_MEMORY_DIR. Index below; read full
entries from global/<name>.md when relevant. To save a durable fact, emit
APIARY_MEMORIZE (see protocol).
- erp-precommit-stale-binary — project-erp pre-commit lint fails when the binary is stale
- ci-duration-erp — project-erp CI takes ~12min; poll accordingly

[Task Memory]
- 2026-06-10T14:02 [triage/analyze] Root cause is in the ADF table renderer; fix belongs in markdown.go
- 2026-06-10T14:31 [engineer/implement] Chose lipgloss table over manual padding; tests in markdown_test.go
```

- The **index** is injected, not full global entries — agents read full files from `APIARY_MEMORY_DIR` (env var set for every step when enabled). This keeps the prompt budget flat as memory grows.
- **Task memory** for a task includes its own notes plus its ancestors' (walk `parent_task_id` up), so spawned children inherit working context.
- Both sections are size-capped (`settings.memory.max_inject_chars`, default 4000 — same default as the instance doc cap): task notes drop oldest-first, the global index truncates with an explicit `(… N more entries — read MEMORY.md)` line.
- `step.memory.read: false` continues to suppress the entire memory document, persistent sections included. A new `step.memory.recall` list allows finer opt-out per tier.

### 3. Lifecycle and curation

- **Task memory** is pruned by a daemon sweep: notes whose task has been terminal (done/failed) longer than `settings.memory.task_retention` (default `720h` = 30 days) are deleted. Notes of ancestors are retained while any descendant is non-terminal.
- **Global memory** is never auto-pruned. Curation paths:
  - Files are plain markdown — operators edit or delete them directly; the index is regenerated from frontmatter on the next write (self-healing).
  - `apiary memory list | show <name> | rm <name> | prune [--dry-run] | path` for inspection and cleanup.
  - Agents can update entries by re-emitting the same `name`, and a curation workflow (e.g. a periodic consolidation agent) can be built with existing primitives — out of scope for v1.

### 4. Suppression

Mirroring `publish: off`:

```yaml
steps:
  - id: untrusted-analysis
    agent: analyzer
    memory:
      memorize: off        # marker stripped and ignored for this step
      recall: [task]       # inject task notes only; no global index
```

Defaults: `memorize: auto`, `recall: [task, global]`.

---

## New Config Schema

```yaml
# top-level in apiary.yaml
settings:
  memory:
    enabled: true                 # default false (opt-in in v1)
    path: ~/.apiary/memory        # default: <data-dir>/memory
    max_inject_chars: 4000        # per-prompt budget for the two persistent sections
    max_entry_bytes: 16384        # cap on a single APIARY_MEMORIZE content
    task_retention: 720h          # prune task notes this long after task is terminal
```

```yaml
# per step (extends existing memory block)
steps:
  - id: implement
    agent: engineer
    memory:
      read: true                  # existing — gates the whole memory doc
      write: ["pr_url"]           # existing — instance-tier declared fields
      recall: [task, global]      # new — which persistent tiers to inject
      memorize: auto              # new — auto | off
```

Config lint validates the block strictly (unknown fields rejected, `recall` values restricted to `task`/`global`, durations parsed).

---

## On-disk Layout

```
.apiary/                     # config.DataDir — beside apiary.yaml, same dir as apiary.db
  memory/                    # memory root (override: settings.memory.path)
    MEMORY.md                # global index — one line per entry, regenerated on write
    global/
      erp-precommit-stale-binary.md
      ci-duration-erp.md
    tasks/
      01JXKQ8Z3F.md          # working notes for task 01JXKQ8Z3F (append-only)
```

Versioning: by default memory is runtime state and should be gitignored like `apiary.db` (`**/.apiary/memory/`). Because entries are plain markdown, a team can deliberately point `settings.memory.path` at a committed directory to version and share long-term memory — supported, but not the default posture.

Entry file format:

```markdown
---
name: erp-precommit-stale-binary
description: project-erp pre-commit lint fails when the apiary binary is stale
created: 2026-06-10T14:02:11Z
updated: 2026-06-10T14:02:11Z
agent: engineer
task: 01JXKQ8Z3F
workflow: engineer-workflow
---

When the local apiary binary predates the config schema, pre-commit lint hard-errors.
Fix: rebuild the binary or commit with --no-verify and note it in the PR.
```

---

## Concrete Use Case: ERP Project Workflows

1. **Triage** runs on issue #2140. The analyze step discovers the bug lives in the Jira ADF renderer and emits a `task`-scoped memorize. It routes to the engineer workflow.
2. **Engineer** workflow starts as a *different workflow instance on the same task* — its first step already sees `[Task Memory]` with triage's note and skips re-investigation.
3. Mid-implementation the agent hits the stale-binary pre-commit failure, burns 10 minutes diagnosing it, and emits a `global` memorize.
4. The step's CI wait fails; **retry** instance starts — task notes tell it exactly what was already tried and decided.
5. A week later, a different agent on a different task hits pre-commit. Its prompt's `[Long-term Memory]` index shows `erp-precommit-stale-binary`; it reads the entry from `$APIARY_MEMORY_DIR/global/` and resolves it in seconds.
6. The operator runs `apiary memory list`, spots a stale entry from before a tooling change, and deletes the file.

---

## Migration Notes

- Purely additive. With `settings.memory.enabled: false` (default), behavior is byte-for-byte identical to today; `APIARY_MEMORIZE` blocks are stripped from output (so prompts that mention the protocol don't leak markers into publishes) but nothing is persisted.
- No database schema changes. The memory root is created lazily on first write.
- Existing `step.memory.read` / `step.memory.write` semantics are unchanged; the new `recall` / `memorize` keys are optional with backward-compatible defaults.
- Memory content is **agent-generated and untrusted**: it is injected with explicit section delimiters and never interpreted by the engine (no markers are parsed out of recalled memory). Documented in the security notes of `design.md`.
