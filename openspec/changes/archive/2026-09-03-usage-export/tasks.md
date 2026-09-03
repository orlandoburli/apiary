# Implementation Plan: Usage and Cost Export

Companion to [proposal.md](proposal.md). GitHub issue #485.

**Status: complete.** Phase 1 shipped in PR #487, Phase 2 in the docs PR that archived this change. Two deviations from the plan below, both made
on contact with the code: the query lives in a new `internal/export` package
rather than `internal/db`, because `db.New` migrates on open and the export
must run read-only against a live daemon (it reuses `improve.OpenReadOnly`
and probes columns the way `improve` does, so an older database exports with
NULLs instead of failing). And the schema-drift guard (2.2) landed in Phase 1,
since the real schema was already under test.

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

### 1.1 Read path — `internal/export/usage.go` (was `internal/db`)

- [x] `UsageFilter` struct: `Since`, `Until time.Time`; `Workflows`, `Agents`,
      `Models`, `Sources`, `Statuses []string`; `IncludeTranscripts`,
      `IncludeSlowTools bool`.
- [x] `UsageRow` struct mirroring the proposal's column table, with
      `sql.Null*` fields for everything that can be NULL.
- [x] `ListUsageRows(ctx, UsageFilter, func(UsageRow) error) error`, a
      callback-per-row API so the CLI streams. Single query:
      `task_executions te LEFT JOIN workflow_instances wi ON wi.id =
      te.workflow_instance_id`, ordered by `te.started_at, te.id`. Filters
      compile to `WHERE` clauses with bound parameters; repeatable flags become
      `IN (...)`.
- [x] NULL `started_at` rows excluded unless `Statuses` contains `pending`.
- [x] Transcript and slow-tools columns are only selected when requested, so
      the default export never reads the blob columns off disk.
- [x] Tests against a seeded in-memory store: every filter individually, the
      pending rule, pre-workflow rows (NULL instance) surviving the join,
      ordering, and that a default export does not touch `input_prompt`
      (assert via a column-projection helper or `EXPLAIN`-free approach:
      scan the built SQL string).

### 1.2 Window parsing

- [x] `export.ParseBound` accepts a duration (`7d`, `24h`), a date
      (`2026-09-01`) or RFC3339. `improve.ParseWindow` was left untouched
      rather than widened; the two packages do not share code.
- [x] `--until` parses the same forms and defaults to now.

### 1.3 Writers — `internal/export/writers.go`

- [x] `csvWriter`: header row from the fixed column list, `encoding/csv`,
      RFC3339 UTC timestamps, six-decimal `cost_usd`, `true`/`false` for
      `credit_exhausted`, `error_message` newlines collapsed to spaces.
- [x] `jsonWriter`: streams `[`, comma-separated objects, `]` so the file is
      valid JSON without buffering; explicit `null` for NULL columns.
- [x] Both writers take an `io.Writer`; `-o` opens the file, default stdout.
      Write to a temp file and rename when `-o` is given, so an interrupted
      export never leaves a truncated file at the target path.

### 1.4 Command wiring — `internal/cli/export.go`

- [x] `apiary export` parent command with `usage` subcommand and the flag set
      from the proposal. Register in `root.go`.
- [x] Open the store read-only the same way `improve.go` does (no migration;
      probe for post-release columns rather than assuming them).
- [x] Row count and elapsed time on stderr at the end; nothing else on
      stderr in a clean run so `apiary export usage | ...` pipelines stay quiet.
- [ ] Exit codes: 0 on success, 1 on any error, 2 on flag misuse (matches the
      CLI spec). Not done: cobra exits 1 on flag errors for every command;
      an export-only exit 2 would be inconsistent. Revisit CLI-wide.
- [x] Tests: CSV and JSON writer formatting, transcript opt-in, `-o` atomic
      write (`openOutput`), every filter against the real schema. No golden
      files: the writer tests assert per-cell so a column addition does not
      churn a fixture.

### 1.5 Verify

- [x] `go build ./... && go test ./...` clean.
- [x] Run against a copy of a real hive database, padded to 100k rows:
      streams under constant memory (`/usr/bin/time -l`). Found and fixed on
      the way: `improve.OpenReadOnly` sets `journal_mode(WAL)`, which fails
      on any non-WAL file (a `VACUUM INTO` backup), so the export has its own
      `export.OpenReadOnly` without it. The same latent failure remains in
      `apiary improve`.
- [x] `detect_changes()`: no indexed symbol changed; the only edit to existing
      code is the `AddCommand` list in `root.go`.

---

## Phase 2 — Docs and drift guard

### 2.1 Documentation

- [x] `docs/cli.md`: "Exporting usage" section with the flag table, the
      column table, and three worked examples (spend by workflow in a
      spreadsheet, duplicate-dispatch detection by `task_number`, one ticket's
      full cost).
- [x] `docs/improve.md`: one paragraph pointing readers who want the raw rows
      to `apiary export usage`.
- [x] `openspec/specs/cli/spec.md`: new `### apiary export` section.
- [x] Regenerate anything derived from the CLI flag set, if such a generator
      exists for docs; otherwise none. None exists; nothing to regenerate.

### 2.2 Schema-drift guard

- [x] Test in `internal/db` that reads the `task_executions` column list from
      the live schema and asserts every column is either in the export
      column set or in an explicit `unexported` allowlist (`pid`,
      `heartbeat_at`, `heartbeat_count`, `can_retry`, `next_retry_at`,
      `created_at`). A new column then fails the build until someone decides.

### 2.3 Close out

- [x] Archive this change in `openspec/CHANGELOG.md` with the PR numbers.
- [x] Close #485 from the Phase 2 PR.
