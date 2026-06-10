# Agent Memory

Apiary agents are stateless between executions by default. The only built-in
context is the [workflow memory](workflows.md#workflow-memory) document — and it
dies with the workflow instance.

**Agent memory** adds two persistent tiers on top, written by agents themselves
through an output marker and stored as plain markdown files you can read, edit,
and delete by hand:

| Tier | What it holds | Lifetime | Scope |
|---|---|---|---|
| Instance (built-in) | Step data + handoff summaries | One workflow instance | The instance |
| **Task** | Working notes: decisions, findings, what was already tried | Until the task is terminal + retention | The task, its retries and fan-out instances, and its spawned descendants |
| **Global** | Durable facts: project gotchas, tooling quirks, conventions | Forever (curated) | The whole daemon — every agent, source, and workflow |

Memory is **opt-in**:

```yaml
settings:
  memory:
    enabled: true
    # path: /custom/root         # default: <data-dir>/memory, beside apiary.db
    # max_inject_chars: 4000     # prompt budget for the recall sections
    # max_entry_bytes: 16384     # cap on a single memorized fact
    # task_retention: 720h       # prune task notes this long after the task ends
```

With memory disabled (the default) nothing is persisted or injected, and any
`APIARY_MEMORIZE` markers agents emit are stripped from output and dropped.

## Writing memory — `APIARY_MEMORIZE`

An agent saves a memory by emitting a marker block in its step output, exactly
like `APIARY_PUBLISH` and `APIARY_SPAWN`. The block carries one JSON object or
an array of them:

```
APIARY_MEMORIZE_BEGIN
{
  "scope": "global",
  "name": "erp-precommit-stale-binary",
  "description": "pre-commit lint fails when the apiary binary is stale",
  "content": "When the local binary predates the config schema, pre-commit lint\nhard-errors. Rebuild the binary, or commit with --no-verify and note it in the PR."
}
APIARY_MEMORIZE_END
```

| Field | Required | Meaning |
|---|---|---|
| `scope` | no | `task` (default) or `global` |
| `name` | global only | kebab-case slug — the upsert key and the entry's filename |
| `description` | global only | one line, shown in the injected index |
| `content` | yes | markdown body |

- **Global** requests upsert `global/<name>.md`: re-emitting the same `name`
  updates the fact (the original `created` timestamp is kept).
- **Task** requests append a timestamped, provenance-stamped note to the task's
  notes file. A retry re-emitting identical content is deduplicated.
- A malformed block, a rejected name, or oversized content is a **warning** in
  the daemon log — a memorize can never fail a step.

To suppress writes for a sensitive step (mirroring `publish: off`):

```yaml
steps:
  - id: untrusted-analysis
    agent: analyzer
    memory:
      memorize: off        # drop APIARY_MEMORIZE requests from this step
      recall: [task]       # inject task notes only, no global index
```

## Recall — what agents see

When memory is enabled, every agent step's prompt is prefixed with two sections
(ahead of the workflow memory document, on the same channel):

```
[Long-term Memory]
Persistent memory lives at $APIARY_MEMORY_DIR. The index is below; read the full
entry from $APIARY_MEMORY_DIR/global/<name>.md when one looks relevant. ...
- erp-precommit-stale-binary — pre-commit lint fails when the apiary binary is stale
- ci-duration — CI takes ~12 minutes on this project

[Task Memory]
- 2026-06-10T14:02:11Z [triage/analyze] (triage) Root cause is in the ADF renderer
- 2026-06-10T14:31:09Z [engineer/implement] (engineer) Chose lipgloss; tests in markdown_test.go
```

Key properties:

- **The index is injected, not full entries.** Agents read full files from
  `$APIARY_MEMORY_DIR` (set for every step), so the prompt budget stays flat as
  memory grows. Both sections together are capped by
  `settings.memory.max_inject_chars` — task notes drop oldest-first, the index
  truncates with an explicit `(… N more entries)` marker.
- **Task memory follows lineage.** A task spawned via `APIARY_SPAWN` sees its
  own notes first, then its parent's, then its grandparent's — so a child
  inherits the context its ancestors collected.
- `memory: {read: false}` on a step suppresses everything, recall included;
  `memory.recall: [task]` / `[global]` filters tiers.

## On-disk layout

```
.apiary/                     # the project data dir, beside apiary.yaml
  memory/
    MEMORY.md                # global index — one line per entry, regenerated on write
    global/
      erp-precommit-stale-binary.md
    tasks/
      01JXKQ8Z3F.md          # working notes for one task (append-only)
```

Each global entry is frontmatter + markdown body, recording provenance (agent,
task, workflow, created/updated). The files are the source of truth: edit or
delete them freely — `MEMORY.md` is derived and self-heals on the next write.

By default the memory root is runtime state; gitignore it like `apiary.db`
(`**/.apiary/memory/`). Because entries are plain markdown, a team can instead
point `settings.memory.path` at a committed directory to version and share
memory deliberately.

## Lifecycle and curation

- **Task notes** are pruned by a daemon sweep (at startup, then every 6 hours)
  once the task has been terminal longer than `task_retention` — unless a
  spawned descendant is still running. Unknown task IDs (after a DB reset) are
  pruned by file age.
- **Global entries are never auto-pruned.** Curate them via the CLI or your editor:

```bash
apiary memory path               # print the memory root
apiary memory list               # index view: name, description, updated, agent
apiary memory show <name>        # print one entry
apiary memory rm <name>          # delete one entry
apiary memory prune [--dry-run]  # run the task-notes sweep on demand
```

## Security notes

- **Recalled memory is untrusted agent output.** It is injected as inert
  context; markers are only ever parsed out of live step output, so a memory
  entry containing `APIARY_SPAWN_BEGIN…` cannot trigger anything by being
  recalled.
- **Global means daemon-wide.** Every agent served by the daemon can read every
  global entry. The store is project-scoped (it lives in the project's data
  dir), so different projects never share memory.
- **Don't memorize secrets.** Instruct agents (in soul files) to never store
  credentials or tokens in memory.
