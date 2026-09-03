# Implementation Plan: Usage and Cost Export

Companion to [proposal.md](proposal.md). GitHub issue #485.

Two phases, each a shippable PR. Phase 1 delivers the whole user-facing
feature; Phase 2 is documentation and the schema-drift guard, split out so the
first PR stays reviewable.

**Conventions.** One feature branch per phase via `git worktree`, squash merge.
No Go CI in this repo: `make check` locally before every PR. `impact` on every
touched symbol before editing; `detect_changes` before committing. No schema
changes are expected; if one turns out to be needed it goes in the idempotent
`migrations` slice in `internal/db/schema.go`.

---

## Phase 1 — Query and command

### 1.1 Read path — `internal/db/usage_export.go`

- [ ] `UsageFilter` struct: `Since`, `Until time.Time`; `Workflows`, `Agents`,
      `Models`, `Sources`, `Statuses []string`; `IncludeTranscripts`,
      `IncludeSlowTools bool`.
- [ ] `UsageRow` struct mirroring the proposal's column table, with
      `sql.Null*` fields for everything that can be NULL.
- [ ] `ListUsageRows(ctx, UsageFilter, func(UsageRow) error) error`, a
      callback-per-row API so the CLI streams. Single query:
      `task_executions te LEFT JOIN workflow_instances wi ON wi.id =
      te.workflow_instance_id`, ordered by `te.started_at, te.id`. Filters
      compile to `WHERE` clauses with bound parameters; repeatable flags become
      `IN (...)`.
- [ ] NULL `started_at` rows excluded unless `Statuses` contains `pending`.
- [ ] Transcript and slow-tools columns are only selected when requested, so
      the default export never reads the blob columns off disk.
- [ ] Tests against a seeded in-memory store: every filter individually, the
      pending rule, pre-workflow rows (NULL instance) surviving the join,
      ordering, and that a default export does not touch `input_prompt`
      (assert via a column-projection helper or `EXPLAIN`-free approach:
      scan the built SQL string).

### 1.2 Window parsing

- [ ] Extend or wrap `improve.ParseWindow` so `--since` accepts an absolute
      date (`2026-09-01`, RFC3339) in addition to durations. Decide by
      `impact` whether to touch `ParseWindow` in place or add
      `ParseSinceUntil` next to it; the improve callers must be unaffected.
- [ ] `--until` parses the same forms and defaults to now.

### 1.3 Writers — `internal/cli/export_usage.go`

- [ ] `csvWriter`: header row from the fixed column list, `encoding/csv`,
      RFC3339 UTC timestamps, six-decimal `cost_usd`, `true`/`false` for
      `credit_exhausted`, `error_message` newlines collapsed to spaces.
- [ ] `jsonWriter`: streams `[`, comma-separated objects, `]` so the file is
      valid JSON without buffering; explicit `null` for NULL columns.
- [ ] Both writers take an `io.Writer`; `-o` opens the file, default stdout.
      Write to a temp file and rename when `-o` is given, so an interrupted
      export never leaves a truncated file at the target path.

### 1.4 Command wiring — `internal/cli/export.go`

- [ ] `apiary export` parent command with `usage` subcommand and the flag set
      from the proposal. Register in `root.go`.
- [ ] Open the store read-only the same way `improve.go` does (no migration;
      probe for post-release columns rather than assuming them).
- [ ] Row count and elapsed time on stderr at the end; nothing else on
      stderr in a clean run so `apiary export usage | ...` pipelines stay quiet.
- [ ] Exit codes: 0 on success, 1 on any error, 2 on flag misuse (matches the
      CLI spec).
- [ ] Tests: golden CSV and JSON for a small seeded store, transcript opt-in,
      `-o` atomic write, filter flags reaching the query.

### 1.5 Verify

- [ ] `make check`.
- [ ] Run against a copy of a real hive database with the daemon running;
      confirm no lock contention and that a 100k-row export streams under
      constant memory (`/usr/bin/time -l`).
- [ ] `detect_changes()` shows only `internal/db`, `internal/cli`, and the
      improve window parser if touched.

---

## Phase 2 — Docs and drift guard

### 2.1 Documentation

- [ ] `docs/cli.md`: "Exporting usage" section with the flag table, the
      column table, and three worked examples (spend by workflow in a
      spreadsheet, duplicate-dispatch detection by `task_number`, one ticket's
      full cost).
- [ ] `docs/improve.md`: one paragraph pointing readers who want the raw rows
      to `apiary export usage`.
- [ ] `openspec/specs/cli/spec.md`: new `### apiary export` section.
- [ ] Regenerate anything derived from the CLI flag set, if such a generator
      exists for docs; otherwise none.

### 2.2 Schema-drift guard

- [ ] Test in `internal/db` that reads the `task_executions` column list from
      the live schema and asserts every column is either in the export
      column set or in an explicit `unexported` allowlist (`pid`,
      `heartbeat_at`, `heartbeat_count`, `can_retry`, `next_retry_at`,
      `created_at`). A new column then fails the build until someone decides.

### 2.3 Close out

- [ ] Archive this change in `openspec/CHANGELOG.md` with the PR numbers.
- [ ] Close #485 from the Phase 2 PR.
