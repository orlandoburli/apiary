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

Active instances
  wf_a1b2c3  PLANE-142  feature-development  step 2/4: implement (backend-dev)  02:14
  wf_d4e5f6  PLANE-137  feature-development  approval_waiting (awaiting human)  18:00

Recent
  wf_j1k2l3  ✓  PLANE-121  feature-development  4 steps  14:32  done
  wf_m4n5o6  ✗  PLANE-118  feature-development  failed at step "review"  08:11
  wf_g7h8i9  ⚠  PLANE-130  backend-bug-fix      interrupted (resumable)  —
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

Manually dispatch a specific task to a worker or workflow, bypassing routing rules.

```
apiary dispatch --cell <source-id>/<task-id> [--worker <worker-id>] [--workflow <workflow-id>]
```

Useful for testing a worker configuration or replaying a failed run. Exactly one of `--worker` or `--workflow` must be provided.

---

### `apiary instances`

List workflow instances with their state, steps, and resume eligibility.

```
apiary instances [--workflow id] [--state state] [--limit n] [--json]
```

| Flag | Description |
|---|---|
| `--workflow` | Filter by workflow ID |
| `--state` | Filter by state: `pending`, `running`, `approval_waiting`, `interrupted`, `done`, `failed` |
| `--limit` | Max rows (default: `20`) |
| `--json` | Output as JSON (one object per line) |

Example output:

```
ID            WORKFLOW              CELL          STATE               STARTED       DURATION
wf_a1b2c3    feature-development   PLANE-142     running             2m 14s ago    2m 14s
wf_d4e5f6    feature-development   PLANE-137     approval_waiting    18m ago       18m
wf_g7h8i9    backend-bug-fix       PLANE-130     interrupted         1h ago        —
wf_j1k2l3    feature-development   PLANE-121     done                3h ago        14m 32s
wf_m4n5o6    feature-development   PLANE-118     failed              5h ago        8m 11s
```

Use `apiary instances <id>` to show step-level detail for a single instance:

```
apiary instances wf_a1b2c3
```

```
Instance:  wf_a1b2c3
Workflow:  feature-development
Cell:      PLANE-142 — Implement user auth
State:     running
Started:   2m 14s ago

Steps
  ✓  plan          architect       2m 14s   passed
  ●  implement     backend-dev     running  (0m 42s)
  ○  review        code-reviewer   waiting  —
  ○  finalize      backend-dev     waiting  —
```

Exit codes: `0` success, `1` general error, `2` instance ID not found.

---

### `apiary resume`

Resume a failed or interrupted workflow instance from the last completed step.

```
apiary resume <instance-id> [--yes]
apiary resume --workflow <workflow-id> [--yes]
```

| Flag | Description |
|---|---|
| `--yes` | Skip the confirmation prompt |
| `--workflow` | Resume the most recent failed or interrupted instance of this workflow |

Without `--yes`, prints a confirmation listing which steps will be skipped and their side effects, then prompts before proceeding:

```
Resuming instance wf_g7h8i9 (feature-development / PLANE-130)

Steps to skip (already completed):
  ✓ plan       architect     — no on_complete hooks
  ✓ implement  backend-dev   — on_complete: set_state=in_progress (already applied)

Steps to run:
  ○ review     code-reviewer
  ○ finalize   backend-dev

Proceed? [y/N]
```

On confirmation, the instance is re-queued. The daemon picks it up on its next cycle.

Exit codes: `0` resume queued, `1` general error, `2` instance not found, `3` instance not resumable (state is `done` or `running`), `4` workflow definition changed since instance was created.

---

### `apiary logs`

Stream or tail structured run logs.

```
apiary logs [--run-id id] [--instance-id id] [--step id] [--follow] [--level debug|info|warn|error]
```

| Flag | Description |
|---|---|
| `--run-id` | Filter logs by a specific step run ID |
| `--instance-id` | Filter logs by workflow instance ID (shows all step logs for that instance) |
| `--step` | Combined with `--instance-id`: show logs for a specific step only |
| `--follow` | Stream new log lines as they arrive |
| `--level` | Minimum log level to show |

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
| `5` | One or more workflow instances are in `interrupted` state on startup (warning, not fatal) |
