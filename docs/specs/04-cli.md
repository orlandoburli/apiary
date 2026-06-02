# Apiary — CLI Design

## Command Structure

```
apiary <command> [flags]
```

## Commands

### `apiary run`

Start the Apiary daemon. Reads `apiary.yaml` from the current directory (or `--config`).

```
apiary run [--config path] [--dry-run] [--once]
```

| Flag | Description |
|---|---|
| `--config` | Path to config file (default: `./apiary.yaml`) |
| `--dry-run` | Fetch and match tasks but do not invoke any runners |
| `--once` | Poll all sources once, process pending tasks, then exit |
| `--source` | Restrict run to a single source id |
| `--worker` | Restrict run to a single worker id |

### `apiary status`

Show the current state of the daemon, active runs, and recent history.

```
apiary status [--watch]
```

Output:

```
Apiary v0.1.0  ·  config: ./apiary.yaml

Sources
  main-plane    plane      polling (last: 12s ago)  3 pending
  main-jira     jira       webhook                  1 pending

Active runs
  #AP-142  backend-bugs   backend-dev   claude-opus-4-8   running  01:23
  #AP-137  docs-tasks     docs-writer   claude-haiku-4-5  running  00:45

Recent
  #AP-134  ✓  frontend-features  frontend-dev  01:12  done
  #AP-130  ✗  backend-bugs       backend-dev   00:34  error: max_turns reached
```

### `apiary validate`

Validate `apiary.yaml` without connecting to any external system.

```
apiary validate [--config path]
```

### `apiary cells`

List tasks currently visible to Apiary (before routing).

```
apiary cells [--source id] [--unmatched] [--limit n]
```

| Flag | Description |
|---|---|
| `--source` | Filter by source id |
| `--unmatched` | Show only tasks that match no route |
| `--limit` | Max rows (default: 20) |

### `apiary dispatch`

Manually dispatch a specific task to a worker, bypassing routing rules.

```
apiary dispatch --cell <source-id>/<task-id> --worker <worker-id>
```

Useful for testing a worker or replaying a failed run.

### `apiary logs`

Stream or tail structured logs.

```
apiary logs [--run-id id] [--follow] [--level debug|info|warn|error]
```

### `apiary init`

Scaffold a new `apiary.yaml` interactively.

```
apiary init
```

Prompts for source type, runner, and generates a starter config.

## Environment Variables

| Variable | Description |
|---|---|
| `APIARY_CONFIG` | Default config file path |
| `APIARY_LOG_LEVEL` | Override `settings.log_level` |
| `APIARY_CONCURRENCY` | Override `settings.concurrency` |
| `APIARY_DRY_RUN` | Set `true` to globally enable dry-run mode |

## Exit Codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | General error |
| `2` | Config validation error |
| `3` | Source connection error |
| `4` | One or more runs failed (only in `--once` mode) |
