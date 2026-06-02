# Apiary — CLI Design

## Command Structure

```
apiary <command> [flags]
```

## Commands

### `apiary run`

Start the Apiary daemon. Reads `apiary.yaml` from the current directory (or `--config`).

```
apiary run [--config path] [--dry-run] [--once] [--source id] [--worker id]
```

| Flag | Description |
|---|---|
| `--config` | Path to config file (default: `./apiary.yaml`) |
| `--dry-run` | Fetch and match tasks but do not invoke any runners |
| `--once` | Poll all sources once, process pending tasks, then exit |
| `--source` | Restrict run to a single source id |
| `--worker` | Restrict run to a single worker id |

---

### `apiary status`

Show the current daemon state, active runs, and recent history.

```
apiary status [--watch]
```

Example output:

```
Apiary v0.1.0  ·  config: ./apiary.yaml

Sources
  main-plane    plane      polling (last: 12s ago)  3 pending
  main-jira     jira       webhook                  1 pending

Active runs
  #PLANE-142  backend-bugs   backend-dev   openai/gpt-4o          running  01:23
  #PLANE-137  docs-tasks     docs-writer   mistral/mistral-large  running  00:45

Recent
  #PLANE-134  ✓  frontend-features  frontend-dev  01:12  done
  #PLANE-130  ✗  backend-bugs       backend-dev   00:34  error: max_turns reached
```

---

### `apiary validate`

Validate `apiary.yaml` schema and (optionally) test source connectivity.

```
apiary validate [--config path] [--connectivity]
```

| Flag | Description |
|---|---|
| `--connectivity` | Also attempt to connect to each configured source |

---

### `apiary cells`

List tasks currently visible to Apiary, before routing.

```
apiary cells [--source id] [--unmatched] [--limit n]
```

| Flag | Description |
|---|---|
| `--source` | Filter by source id |
| `--unmatched` | Show only tasks that match no route |
| `--limit` | Max rows (default: 20) |

---

### `apiary dispatch`

Manually dispatch a specific task to a worker, bypassing routing rules.

```
apiary dispatch --cell <source-id>/<task-id> --worker <worker-id>
```

Useful for testing a worker configuration or replaying a failed run.

---

### `apiary logs`

Stream or tail structured run logs.

```
apiary logs [--run-id id] [--follow] [--level debug|info|warn|error]
```

---

### `apiary init`

Interactively scaffold a new `apiary.yaml`.

```
apiary init
```

Prompts for source type, runner, basic routing rules, and generates a starter config file.

---

## Environment Variables

| Variable | Description |
|---|---|
| `APIARY_CONFIG` | Default config file path |
| `APIARY_LOG_LEVEL` | Override `settings.log_level` |
| `APIARY_CONCURRENCY` | Override `settings.concurrency` |
| `APIARY_DRY_RUN` | Set `true` to globally enable dry-run mode |
| `APIARY_DB_PATH` | Path to the SQLite run history database |

## Exit Codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | General error |
| `2` | Config validation error |
| `3` | Source connection error |
| `4` | One or more runs failed (only in `--once` mode) |
