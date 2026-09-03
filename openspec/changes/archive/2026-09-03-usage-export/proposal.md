# Proposal: Usage and Cost Export

GitHub issue #485.

## Why

Apiary already records everything needed to answer "how much is this costing
me": every runner attempt writes a `task_executions` row with tokens (input,
output, cache creation, cache read), `cost_usd`, model, runner, turns, tool
calls, duration, the six `time_*_ms` wall-clock buckets, `failure_kind` and
`credit_exhausted`. The workflow and step the attempt belongs to are one join
away through `workflow_instance_id`.

None of it is reachable without opening `.apiary/apiary.db` in `sqlite3` and
hand-writing that join. The two places that do read it, the dashboard and
`apiary improve`, answer their own questions ("what is this task doing",
"which step should I fix") and neither hands the rows to the user.

The cost of that gap is concrete. A dedup-log bug produced a runaway of more
than $200 a day on the project-erp hive, and it was only found because someone
knew the schema well enough to query it by hand. Spend by workflow, by model,
by ticket, trend over time, and "the same ticket dispatched twice" are all
spreadsheet questions. A user should get there by exporting a file, not by
learning the schema.

## What Changes

| Area | Before | After |
|---|---|---|
| **CLI** | no export path | `apiary export usage` writes CSV or JSON |
| **Scope** | n/a | `--since`, `--until`, `--workflow`, `--agent`, `--model`, `--source`, `--status` filters |
| **Columns** | n/a | a fixed, documented column set; `input_prompt` / `output_text` only with `--include-transcripts` |
| **Context** | attempt rows only | each row carries `workflow_id`, `step_id`, `instance_state`, `task_number`, `task_url` |
| **Daemon** | n/a | untouched. The command opens the database read-only and runs with the daemon up or down |
| **Docs** | n/a | new "Exporting usage" section in `docs/cli.md`, cross-linked from `docs/improve.md` |

Nothing changes in the schema, the engine, the dispatcher, or the dashboard.

## New Concepts

### `apiary export`

A new command group. `usage` is its first and only subcommand; the group exists
so later exports (events, approvals, transcripts) have a home without adding
top-level verbs.

```
apiary export usage [flags]

  --format csv|json       default csv
  -o, --output PATH       default stdout
  --since DUR|DATE        window start; DUR as in improve (7d, 24h), DATE as RFC3339 or YYYY-MM-DD
  --until DATE            window end, default now
  --workflow ID           repeatable
  --agent ID              repeatable
  --model NAME            repeatable, exact match
  --source ID             repeatable
  --status STATUS         repeatable: success|failed|running|pending
  --include-transcripts   add input_prompt and output_text columns
  --include-slow-tools    add slow_tools (JSON) column
```

The window filters on `started_at`. Rows with a NULL `started_at` (dispatched,
never started) are included only when `--status pending` is given explicitly.

### Column set

Ordered as they appear in the export. This is the contract; adding a column is
a minor change, removing or reordering one is a breaking change and needs a
`--schema-version` bump.

| Column | Source |
|---|---|
| `execution_id` | `task_executions.id` |
| `task_id` | `task_executions.task_id` |
| `task_number` | `task_executions.task_number` |
| `title` | `task_executions.title` |
| `task_url` | `task_executions.task_url` |
| `source_id` | `workflow_instances.source_id` |
| `workflow_id` | `workflow_instances.workflow_id` |
| `workflow_instance_id` | `task_executions.workflow_instance_id` |
| `instance_state` | `workflow_instances.state` |
| `step_id` | `task_executions.step_id` |
| `agent_id` | `task_executions.agent_id` |
| `runner` | `task_executions.runner` |
| `model` | `task_executions.model` |
| `attempt` | `task_executions.attempt` |
| `status` | `task_executions.status` |
| `failure_kind` | `task_executions.failure_kind` |
| `credit_exhausted` | `task_executions.credit_exhausted` as `true`/`false` |
| `started_at`, `completed_at` | RFC3339 UTC |
| `duration_ms` | `task_executions.duration_ms` |
| `input_tokens`, `output_tokens`, `cache_creation_tokens`, `cache_read_tokens`, `total_tokens` | as stored |
| `num_turns`, `num_tool_calls` | as stored |
| `cost_usd` | as stored, six decimals |
| `time_thinking_ms`, `time_writing_ms`, `time_model_ms`, `time_tool_wait_ms`, `time_other_ms`, `time_background_ms` | as stored |
| `error_message` | `task_executions.error_message`, single-line (newlines collapsed) |
| `slow_tools` | opt-in, raw JSON string |
| `input_prompt`, `output_text` | opt-in, raw |

`instance_state` is the canonical vocabulary from `internal/state`. The
`workflow_*` and `instance_state` columns are empty for pre-workflow rows
(`workflow_instance_id IS NULL`); those rows are still exported so historical
totals stay correct.

JSON output is an array of objects with the same keys. Numbers are numbers,
booleans are booleans, timestamps are strings, nulls are `null` rather than
omitted, so a column list derived from any row is complete.

### Read path

The query lives in `internal/db` as `ListUsageRows(ctx, UsageFilter) (iter)`,
streaming rows rather than loading the table, because a busy hive accumulates
hundreds of thousands of executions and the transcript columns alone run to
gigabytes. The command opens the store the same way `apiary improve` does
(read-only, no migration) so it is safe to run against a live daemon.

## Out of Scope

- **Aggregation.** No `--group-by`; the export is row-level and the
  spreadsheet does the pivoting. Aggregates already exist in
  `apiary improve --dump-evidence` for the questions it asks.
- **Scheduled export.** A routine in `dev.apiary.routines` can shell out to
  this command; nothing in core needs to schedule it.
- **Dashboard export button.** The TUI dashboard could invoke the same
  `ListUsageRows`; that is a separate, small follow-up once the column
  contract has settled.
- **Other tables.** `execution_events`, approvals, and step runs are not
  exported here. They are candidates for later `apiary export` subcommands.
- **Currency and pricing.** `cost_usd` is exported as stored. Re-pricing
  historical rows against a rate table is not attempted.

## Risks

- **Column contract drift.** Every future column added to `task_executions`
  is a decision: export it or not. The proposal treats the list above as the
  contract and adds a test that fails when a new column appears in the schema
  without a corresponding decision in the export.
- **Transcript size.** With `--include-transcripts` a full export can exceed
  memory if buffered. Streaming rows and writing incrementally is a hard
  requirement, not an optimisation.
- **Time zones.** SQLite stores whatever the writer produced. The export
  normalises to RFC3339 UTC and documents that; rows whose stored value
  cannot be parsed are exported verbatim with a warning on stderr rather than
  dropped.
