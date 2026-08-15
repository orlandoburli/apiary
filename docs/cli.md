# CLI Reference

```
apiary <command> [flags]
```

Global flags, accepted by every command:

| Flag | Default | Description |
|---|---|---|
| `--config` | `apiary.yaml` | Config file path |
| `--env-file` | `.env` | Path to a `.env` file (silently skipped if not found) |
| `--verbose` | off | Verbose (debug) output |

## Daily driving

### `apiary run`

Start the daemon: poll sources, route tasks, dispatch agents. Runs until
interrupted.

```sh
apiary run [--debug] [--once] [--dry-run] [--source id] [--worker id] [--profile name]
```

| Flag | Description |
|---|---|
| `--debug` | Verbose DEBUG logging: per-task prompt, live agent conversation, and routing decisions (view in the dashboard) |
| `--once` | Poll once, dispatch all matching tasks, then exit (exit code 4 if any run failed) |
| `--dry-run` | Connect to sources and match tasks, but never invoke a runner |
| `--source` | Restrict to a single source id |
| `--worker` | Restrict to a single worker id |
| `--profile` | Activate a named [runner profile](configuration.md#profiles) from `profiles.<name>` |

### `apiary dashboard`

Open the [terminal dashboard](dashboard.md) — a read-only live view of tasks,
agents, and logs. Run it in a second terminal, from the same directory as
`apiary run` (they share the `.apiary/` state).

### `apiary status`

Show daemon status and active runs.

```sh
apiary status [--watch]    # --watch refreshes every 2 seconds
```

The status payload includes durable job counts and each registered worker's
readiness, drain state, capacity, active jobs, and last heartbeat.

### `apiary worker`

Connect a separate worker process to a control plane. The worker loads the same
runner and workflow configuration but does not poll sources.

```sh
apiary worker --control-plane https://apiary.example \
  --token "$APIARY_WORKER_TOKEN" --id build-01 --pool build --capacity 4
```

Use repeatable `--label` and `--capability` flags to advertise scheduling
attributes. On SIGTERM/SIGINT the worker enters drain mode, stops claiming, lets
active jobs finish while extending their leases, then becomes unready.

### `apiary validate`

Validate `apiary.yaml` — schema, reference integrity (agents → runners,
steps → agents, goto targets), workflow graph rules, and condition expression
syntax (`if:`, `reject_when:`, split branches, `${{ }}` joins). Local
subworkflow `uses` references are resolved recursively relative to their
declaring file; typed contracts, output mappings, and reference cycles are
validated before connectivity checks run.

```sh
apiary validate [--connectivity]    # --connectivity also tests each source
```

## Inspecting work

### `apiary cells`

List the tasks currently visible to Apiary, before routing.

```sh
apiary cells [--source id] [--unmatched] [--limit n]
```

`--unmatched` shows only items matching no trigger — useful when an issue
isn't being picked up.

### `apiary instances`

List workflow instances, or show one in step-level detail.

```sh
apiary instances [--workflow id] [--state s] [--limit n] [--json]
apiary instances <instance-id>
apiary instances <instance-id> --cancel
```

States: `pending`, `running`, `approval_waiting`, `interrupted`, `done`,
`failed`.

`--cancel` stops one running instance: its in-flight step is cancelled and the
instance is marked `interrupted`, along with any queued or leased dispatch job
for the same task and workflow. The source item is left alone — no labels are
stripped and no state is reset — so unlike [`apiary restart`](#apiary-restart)
nothing is re-dispatched, and the run can be picked up later with
[`apiary resume <instance-id>`](#apiary-resume).

Use it when a task has more than one live instance and you want to keep one of
them: `restart` acts on the whole cell, and stopping the run from the dashboard
targets the cell too.

### `apiary profile`

Show where a run's wall clock actually went — per step, plus the slowest
individual calls across the whole run:

```sh
apiary profile <instance-id> [--json]
```

```
STEP               TOTAL     THINK     WRITE     TOOLS     OTHER
plan               3m0s      67%       33%       —         —
implement          1h22m42s  6%        24%       63%       7%
  63% with background work outstanding (overlaps the above)

Slowest calls
  9m54s      background  implement       workflow:verify  ·  run the full test suite
  8m0s       tool        implement       Bash  ·  ./gradlew test
```

Use it before tuning a slow step: the fix for a thinking-heavy step (model or
effort), a writing-heavy one (a tighter prompt) and a wait-heavy one (fix the
thing being waited on) have nothing in common, and guessing wrong costs another
full-length run to disprove.

Steps recorded before this data existed, and runners that stream no events,
report as `not measured` rather than as a breakdown of zeros. See
[Wall-clock attribution](data-model.md#wall-clock-attribution) for what each
bucket covers and why the background figure overlaps the others.

`--json` emits the full breakdown, for the analysis this table does not do.

### `apiary task`

Show a task's full workflow history — all instances, steps, and scoped logs.
Resolve by internal task id, or by source item:

```sh
apiary task <internal-task-id>
apiary task --source github --item 1948
apiary task <id> --json
```

### `apiary improve`

Analyse the execution history and propose configuration changes. Standalone —
it opens the database read-only, works with the daemon stopped, and takes no
dispatch slot when it is running.

```sh
apiary improve                          # analyse; print findings and a diff
apiary improve --effort deep --since 30d
apiary improve --apply                  # write the accepted changes
apiary improve --dump-evidence          # just the metrics, as JSON, no model
apiary improve --dump-prompt            # the composed prompt, no model
```

The evidence is computed entirely in Go, so `--dump-evidence` needs no advisor
and costs nothing. Everything else needs an agent to reason with: `--advisor`,
an ad-hoc `--runner`/`--model` pair, `settings.improve.agent`, or an agent named
`improver`.

Past runs are recorded so their effect can be measured later:

```sh
apiary improve history
apiary improve show <run-id>
apiary improve effect <run-id>
```

See [Self-Improvement](improve.md) for the evidence pack, effort levels, the
validation gate and what applying does.

## Intervening

### `apiary resume`

Replay a failed or interrupted workflow instance as a new immutable descendant.
Cached steps are copied into the new attempt (their outputs and memory restored),
then execution continues. The source attempt is never modified.

```sh
apiary resume <instance-id> [--yes]
apiary resume --workflow <workflow-id> [--yes]   # most recent failed/interrupted instance
apiary resume <instance-id> --from implement     # rerun this step and later steps
apiary resume <instance-id> --definition original # use the snapshotted definition
```

`--definition current` is the default. `--definition original` requires an instance
created after workflow snapshots were introduced.

Compare any two attempts step by step:

```sh
apiary instances compare <before-id> <after-id>
apiary instances compare <before-id> <after-id> --json
```

The comparison reports state, input/output changes, token and cost deltas, and
model/runner changes. Use `apiary run --dry-run` to evaluate source matching and
routing without starting an agent.

### `apiary dispatch`

Start one named workflow right now, whether or not anything would have triggered
it. Same action as `W` in the dashboard.

```sh
apiary dispatch triage --item PSP-199     # run `triage` on a source item
apiary dispatch nightly-audit             # run standalone, with no source item
apiary dispatch report --input scope=q3   # standalone, with structured input
```

A manual run skips **every** gate the poll loop applies:

| Skipped | Meaning |
|---------|---------|
| the trigger's `match` block | states, labels, types, `title_regex`, source — the item does not have to look like something the trigger would select |
| exclusive-trigger suppression | a higher-priority `exclusive: true` trigger does not claim the task away |
| the live-instance guard | a workflow already running on the task starts a **second concurrent instance** |
| `once: true` | a spent one-shot workflow runs again |
| the consecutive-failure cap | `settings.max_attempts` does not block the run |

Every bypass is printed, so none of them is silent:

```
✓ Started workflow triage on PSP-199 (10042) in jira
  ! this workflow was already running on the task — a second instance is now live
  ! bypassed guard: trigger match (state/labels/filters)
  ! bypassed guard: exclusive trigger suppression
  ! bypassed guard: active instance / in-flight
  ! bypassed guard: once
  ! bypassed guard: consecutive-failure cap
  → follow it with: apiary instances
```

**With `--item`** the run binds a source item and behaves exactly like an
automatic dispatch of that workflow: it sees the item's live labels and state,
and side effects (comments, state locks, sub-issues) write back to the source.
The value is the item's human reference (`PSP-199`, `#1953`) or its cell id — the
same vocabulary [`apiary restart`](#apiary-restart) accepts, matched
case-insensitively with a leading `#` ignored.

The item does **not** have to be one apiary is already tracking. A reference it
has never polled — a ticket outside the source's `filters`, in an excluded state,
or created since the last tick — is fetched from the source and bound on the
spot, exactly as a poll would have bound it:

```sh
apiary dispatch triage --item PSP-199                 # one source: inferred
apiary dispatch triage --item PSP-199 --source jira   # several: name it
```

`--source` is optional when exactly one configured source can fetch a single
item. When several can, apiary asks rather than guesses — fetching a different
project's `PSP-199` and running a workflow over it is not a mistake anyone
catches quickly:

```
Error: dispatch failed: source required: 2 sources could hold it (jira, my-repo)
  — pass --source to say which
```

`--source` also disambiguates a reference that exists in more than one source,
which previously required looking up the cell id by hand.

A reference no source can produce an item for still fails and creates nothing.
The source that answered is named in the output and on the binding — with Jira
the key you typed (`PSP-199`) and the cell id it resolved to (the opaque numeric
issue id) are both reported, since only one of them appears in Jira's UI.

**Without `--item`** the workflow runs standalone on a fresh internal task with no
source binding. Nothing writes back to a source: comment and state-lock steps are
no-ops and sub-issues cannot be materialized. Pass `--input key=value` (repeatable)
for values the steps read as `${{ input.<key> }}`, and `--title` to name the task.

This is what makes a **trigger-less workflow** useful: a workflow with no
`trigger:` block never starts on its own — `apiary validate` warns about exactly
that — but `apiary dispatch <id>` runs it on demand.

Because guards are skipped rather than overridden, nothing in the daemon prevents
two runs of the same workflow on the same task from racing. Where that matters —
steps that mutate the same branch, comment, or item state — start the second run
after the first settles.

### `apiary restart`

Force-restart a stale task: cancel its running dispatch, cancel its queued jobs,
interrupt its non-terminal instances, strip its control labels — then re-route the
item and dispatch it **immediately**, without waiting for the next poll. Same
action as `R` in the dashboard.

```sh
apiary restart CDT-123     # Jira key
apiary restart '#1953'     # GitHub issue number (quote it — # starts a comment)
apiary restart 1953        # the cell id also works
```

The argument is the item's **human reference** (a Jira key, a GitHub issue number)
or its **cell id** (the raw source item id). The reference is usually what you
want: on Jira the cell id is the opaque numeric issue id, which appears in no
interface — the key is the only thing you can see.

Matching ignores case and a leading `#`. An exact cell id always wins over a
number. A reference that matches items in **two different sources** is rejected
rather than guessed at, and names both candidates so you can restart the cell id
directly. An argument that resolves to nothing fails and touches nothing; if it is
an internal task id, the error names the item to use instead.

Restart overrides the `once` and failure-cap (`settings.max_attempts`) guards,
since a task wedged behind either is what restart exists for; any override is
printed. It does not override the in-flight guard, so a live workflow is never run
twice. The command reports what it dispatched:

```
✓ Restarted CDT-123 (10042) (control labels cleared)
  ! overrode guard: implement (failure cap)
  → dispatched 1 workflow(s): implement
```

The item is echoed as `reference (cell id)` when the two differ, so it is always
clear which item was acted on. `dispatched 0` means the cleanup ran but no
workflow matches the item in its current state — usually a label the triggers
don't match.

### `apiary delete`

Delete a task and all its workflow instances from the database.

```sh
apiary delete <task-id> [--yes]
apiary delete --source github --item 1953 [--yes]
```

### `apiary clear`

Reset the project's SQLite database (asks for confirmation; `--yes` skips).

## Setup & service

### `apiary init`

Scaffold a starter `apiary.yaml` in the current directory.

### `apiary service`

Manage Apiary as a system service — systemd (Linux), launchd (macOS), or
Windows Service — so the daemon starts at boot and restarts on failure:

```sh
apiary service install
apiary service start
apiary service status
apiary service stop
apiary service uninstall
```

### `apiary update`

Update apiary to the latest GitHub release in place:

```sh
apiary update          # download, verify the checksum, and replace the binary
apiary update --check  # only report whether a newer version exists
```

Downloads are validated against the release's `checksums.txt` before the swap.
Installs managed by Homebrew or Scoop are detected and redirected to
`brew upgrade --cask apiary` / `scoop update apiary` instead of self-updating.
After an update, restart the daemon (`apiary service stop && apiary service start`)
to pick up the new version.

Interactive commands also check for a new release at most once every 24 hours
and print a short notice when one is available. Set `APIARY_NO_UPDATE_CHECK=1`
to disable the check.

### `apiary version`

Print the version.

## Environment variables

| Variable | Description |
|---|---|
| `GITHUB_TOKEN` | GitHub source polling + write fallback (see [GitHub source](github-source.md)); also raises the API rate limit for `apiary update` |
| `PLANE_API_KEY` | Plane source API key (see [Plane source](plane-source.md)) |
| `APIARY_NO_UPDATE_CHECK` | Disable the daily update-check notice |

Any variable referenced as `${VAR}` in the config must be set in the daemon's
environment or in the auto-loaded `.env` file.
