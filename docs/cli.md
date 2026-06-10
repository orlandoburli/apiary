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
apiary run [--debug] [--once] [--dry-run] [--source id] [--worker id]
```

| Flag | Description |
|---|---|
| `--debug` | Verbose DEBUG logging: per-task prompt, live agent conversation, and routing decisions (view in the dashboard) |
| `--once` | Poll once, dispatch all matching tasks, then exit (exit code 4 if any run failed) |
| `--dry-run` | Connect to sources and match tasks, but never invoke a runner |
| `--source` | Restrict to a single source id |
| `--worker` | Restrict to a single worker id |

### `apiary dashboard`

Open the [terminal dashboard](dashboard.md) — a read-only live view of tasks,
agents, and logs. Run it in a second terminal, from the same directory as
`apiary run` (they share the `.apiary/` state).

### `apiary status`

Show daemon status and active runs.

```sh
apiary status [--watch]    # --watch refreshes every 2 seconds
```

### `apiary validate`

Validate `apiary.yaml` — schema, reference integrity (agents → runners,
steps → agents, goto targets), and workflow graph rules.

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
```

States: `pending`, `running`, `approval_waiting`, `interrupted`, `done`,
`failed`.

### `apiary task`

Show a task's full workflow history — all instances, steps, and scoped logs.
Resolve by internal task id, or by source item:

```sh
apiary task <internal-task-id>
apiary task --source github --item 1948
apiary task <id> --json
```

## Intervening

### `apiary resume`

Resume a failed or interrupted workflow instance from the last completed
step. Cached steps are replayed (their outputs and memory restored), then
execution continues.

```sh
apiary resume <instance-id> [--yes]
apiary resume --workflow <workflow-id> [--yes]   # most recent failed/interrupted instance
```

### `apiary dispatch`

Manually dispatch a task, bypassing routing. Useful for testing a
configuration.

```sh
apiary dispatch --cell <source-id>/<task-id> --worker <worker-id>
```

### `apiary restart`

Force-restart a stale task: cancel its running dispatch, interrupt its
non-terminal instances, and reset it for re-dispatch on the next cycle. Same
action as `R` in the dashboard.

```sh
apiary restart <task-id>
```

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
