# Apiary Dashboard — Data Reference

What each tab shows, where every value comes from, and when it updates.

Open with:

```bash
apiary dashboard
```

The dashboard is **read-only**. It opens `~/.apiary/apiary.db` (the same SQLite
file the dispatcher writes to) and renders whatever is there. If the file does
not exist it exits with a hint to run `apiary run` first.

---

## Refresh model

- Only the **active tab** is queried — switching tabs triggers an immediate fetch.
- The active tab is re-queried automatically every **2 seconds**.
- Press **`r`** to force a refresh; the footer shows how long ago data last loaded.
- Every query is bounded by a **2-second timeout**, so a momentarily locked
  database slows a refresh but never freezes or crashes the UI.

Keys: `←/→` switch tabs · `↑/↓` move selection · `r` refresh · `q` quit.

---

## Source tables

All data comes from four tables in `~/.apiary/apiary.db`:

| Table             | Written by                          | Feeds tabs            |
|-------------------|-------------------------------------|-----------------------|
| `task_executions` | dispatcher, on every run attempt    | Overview, Agents      |
| `tasks`           | dispatcher, on task create/run      | Tasks                 |
| `service_logs`    | dispatcher/router/runner logging    | Logs                  |
| `agents`          | dispatcher, on agent registration   | *(not yet read — see Known gaps)* |

---

## Overview tab

Dispatcher health and 24-hour rollups.

| Field          | Meaning                                              | Source |
|----------------|------------------------------------------------------|--------|
| **Status**     | Dispatcher health indicator                          | Currently hard-coded to `Healthy` (placeholder until `dispatcher_state` is read) |
| **Concurrency**| Configured worker count                              | Static (`4`) for now |
| **Agents**     | Distinct agents that ran something in the last hour  | `COUNT(DISTINCT agent_id)` from `task_executions` where `created_at >= now-1h` |
| **Running**    | Execution attempts currently in progress             | `COUNT(*)` from `task_executions` where `status='running'` |
| **Queued**     | Failed attempts waiting on a scheduled retry         | `COUNT(*)` from `task_executions` where `status='failed' AND can_retry AND next_retry_at > now` |
| **Completed**  | Attempts that succeeded **today**                    | `COUNT(status='success')` from `task_executions` where `created_at >= today 00:00` |
| **Failed**     | Attempts that failed **today**                       | `COUNT(status='failed')` from `task_executions` where `created_at >= today 00:00` |
| **Throughput** | Rough tasks/min = today's completed ÷ 24             | Derived from Completed |
| **Avg Duration**| Mean `duration_ms` of today's attempts              | `AVG(duration_ms)` (today) |
| **Success Rate**| today's successes ÷ today's total attempts          | computed in SQL |

> "Today" means since local midnight. "24h"/"last hour" windows are rolling.
> The Completed/Failed/Avg/Success figures **reset at midnight**; Running and
> Queued are point-in-time.

---

## Tasks tab

Tasks the dispatcher is **actively running right now** — one block per task,
with a simulated progress bar and elapsed time. `↑/↓` moves the `▶` cursor.

| Field        | Meaning                          | Source |
|--------------|----------------------------------|--------|
| **Title**    | Task title (truncated to width)  | `tasks.title` |
| **Agent**    | Agent assigned to the task       | `tasks.agent_id` |
| **Elapsed**  | Time since the run started       | now − `tasks.started_at` |
| **Progress** | Visual bar only — **not** a real completion metric | derived from elapsed time (`(duration_ms % 10000) / 100`) |

Query: `tasks` where `state='running'`, newest `started_at` first.

> The progress bar is cosmetic — the runner does not report real percent-complete.
> A task only appears here while its `state` is `running`; finished tasks drop off.

---

## Agents tab

Per-agent performance, aggregated across **all** execution attempts (not just
today). Table columns, `▶` cursor moves with `↑/↓`.

| Column      | Meaning                                       | Source |
|-------------|-----------------------------------------------|--------|
| **AGENT**   | Agent id                                       | `task_executions.agent_id` (grouped) |
| **STATUS**  | Status dot                                     | Currently always `idle` (placeholder — live status not tracked yet) |
| **COMPLETED**| Lifetime successful attempts                  | `COUNT(status='success')` |
| **AVG**     | Mean attempt duration                          | `AVG(duration_ms)` |
| **SUCCESS** | successes ÷ total attempts (all time)          | computed in SQL |

Query: `task_executions` grouped by `agent_id`, ordered by id.

> Agents are derived from **execution history**, so an agent only shows up once
> it has run at least once. A freshly-configured agent with no runs will not
> appear here yet (see Known gaps).

---

## Logs tab

Recent service-level log lines, newest at the bottom. Color-coded by level:
`ERROR` red · `WARN` yellow · `INFO` blue · other grey. `↑/↓` scrolls.

| Field       | Meaning                                  | Source |
|-------------|------------------------------------------|--------|
| **Time**    | Log timestamp (`HH:MM:SS`)               | `service_logs.timestamp` |
| **Level**   | `DEBUG` / `INFO` / `WARN` / `ERROR`      | `service_logs.level` |
| **Message** | Log text (truncated to width)            | `service_logs.message` |

Query: most recent **100** rows from `service_logs`, shown chronologically.
The `component` field (`dispatcher`, `router`, `runner`, …) is captured in the
DB but not yet displayed in the row.

---

## Known gaps (data that will be empty / static today)

These are limitations of what the dispatcher currently **writes**, not the
dashboard's ability to read it:

- **Tasks** and **Logs** tabs stay empty until the dispatcher populates the
  `tasks` and `service_logs` tables. Today it primarily writes `task_executions`,
  so Overview and Agents have data first.
- **Overview › Status** is a fixed `Healthy`; **Concurrency** is a fixed `4`.
  Both will become live once the dashboard reads the `dispatcher_state` table.
- **Agents › STATUS** is always `idle` and the list is built from execution
  history, so configured-but-never-run agents (and the `agents` registry table)
  are not reflected yet.

---

## Inspecting the raw data

Useful for confirming what the dashboard *should* show:

```bash
# Row counts per source table
sqlite3 ~/.apiary/apiary.db \
  "SELECT 'executions', COUNT(*) FROM task_executions
   UNION ALL SELECT 'tasks', COUNT(*) FROM tasks
   UNION ALL SELECT 'logs', COUNT(*) FROM service_logs
   UNION ALL SELECT 'agents', COUNT(*) FROM agents;"

# What the Agents tab aggregates
sqlite3 -header -column ~/.apiary/apiary.db \
  "SELECT agent_id, status, COUNT(*) FROM task_executions GROUP BY agent_id, status;"

# Currently-running tasks (Tasks tab)
sqlite3 -header -column ~/.apiary/apiary.db \
  "SELECT id, title, agent_id, started_at FROM tasks WHERE state='running';"
```

---

## Where the code lives

| Concern                | File |
|------------------------|------|
| TUI (tabs, render, refresh) | `src/internal/dashboard/app.go` |
| View-model structs          | `src/internal/dashboard/models.go` |
| Colors / progress bar       | `src/internal/dashboard/styles.go` |
| SQL queries behind each tab  | `src/internal/db/dashboard.go` |
| `apiary dashboard` command  | `src/internal/cli/dashboard_cmd.go` |
