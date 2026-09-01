# Proposal: Task Progress Column and Board View

> **Depends on `unified-task-state-model`.** Every column in the board is a canonical state, and
> the progress column is only readable once `blocked` carries a reason. Landing this first would
> mean building a board whose columns are the four disjoint vocabularies — the exact problem the
> other change exists to remove.

## Why

The Tasks tab row is:

```
▶  NUM   TITLE                        AGENT      STATUS    WHEN
```

Two problems, one of them structural.

**The agent column is a category error.** An agent is a property of a *step*, not of a task. A
task that runs five steps across three agents shows one of them — whichever the underlying query
happened to surface. It is an approximation presented as a fact, and it is the same conflation
that `unified-task-state-model` removes from the state column: an attribute of an inner object
hoisted onto an outer one, losing the information that made it meaningful.

**Nothing answers "where has it got to".** `STATUS` says whether the task is healthy; `WHEN` says
how stale it is. Neither says which of a workflow's steps is executing. For a workflow of eight
steps, a task can sit in `running` for forty minutes and the list cannot distinguish "started" from
"nearly done". The information exists — `step_runs` has a row per step, with `state`, `started_at`
and `finished_at` — but it is reachable only by drilling into the workflow monitor
(`TaskViewWorkflow`), one task at a time.

**There is no fleet-level view.** The Tasks tab is a reverse-chronological list. It answers "what
happened recently" well and "what is the hive doing right now" badly: running work, work parked on
an approval, and work that failed last Tuesday are interleaved by timestamp. The one question an
operator asks most — *what needs me?* — requires scanning the whole list and reading every badge.

A board answers that question by construction, and once states are canonical a board is not a new
derivation: it is a group-by on a single column.

---

## What Changes

| Area | Before | After |
|---|---|---|
| **Tasks list columns** | `NUM TITLE AGENT STATUS WHEN` | `NUM TITLE STEP STATUS WHEN` |
| **Agent visibility** | A column that shows one arbitrary step's agent | Shown per step in the detail and monitor views, where it is true |
| **Progress** | Only inside the workflow monitor, one task at a time | `review 3/5` in every list row |
| **Fan-out** | Silently picks one instance | Explicit `⑂ 3 steps` marker |
| **Views on the Tasks tab** | list / detail / logs / monitor / transcript | + **board** (`TaskViewBoard`), toggled with `b` |
| **Default view** | list only | list, or board via `--view=board` / `settings.dashboard.default_view` |
| **Acting on a task** | list → detail → monitor, then act | act directly on the selected card with the same keys |
| **Board columns** | — | One per canonical state: `QUEUED` `RUNNING` `BLOCKED` `DONE`, with `FAILED` as a separate lane |
| **Query cost** | 1 list query | 1 list query + 1 batched progress query per visible page |

---

## New Concepts

| Concept | Description |
|---|---|
| **Step progress** | A `(step_id, position, total)` triple resolved for a task's live instance: which step is executing and where it sits in the workflow's step sequence. |
| **Fan-out marker** | The explicit rendering used when a task has more than one live instance, so the column never silently picks a winner. |
| **Board view** | A column-per-state sub-view of the Tasks tab. Cards are tasks; columns are canonical states; a card occupies exactly one column. |
| **Attention lane** | `FAILED` rendered as a full-width lane beneath the board rather than as a column, because it is not a stage of progress. |

---

## Design

### 1. The progress column

Replaces `AGENT`, keeping the same fixed width so the row layout is unchanged.

| Situation | Rendering | Note |
|---|---|---|
| One live instance, one running step | `review 3/5` | step id, position, total |
| One live instance, step is parked | `review 3/5` | the *why* is in `STATUS` (`blocked:approval`) — not duplicated here |
| More than one live instance | `⑂ 3 steps` | never picks a winner |
| One instance, `parallel` / `for_each` running several steps | `⑂ 3 steps` | same rule; fan-out is fan-out |
| No live instance, task terminal | `5/5` or `2/5` | where it stopped, no step name |
| No live instance, task never dispatched | `—` | |

`position` is the 1-based index of the step in the instance's step sequence; `total` is the
sequence length. Both come from the workflow definition snapshot the instance already stores, so a
workflow edited after dispatch does not retroactively change a historical row's denominator.

Truncation: step ids are user-authored and can be long. The column truncates the step id, never
the `n/m` suffix — losing the position is worse than losing the tail of a name.

### 2. Resolving progress without an N+1

The list renders up to a page of rows per tick. A per-row query would issue one query per visible
task on every refresh, on the same connection the daemon is writing to.

One batched query per page instead, keyed on the visible task ids:

```sql
SELECT sr.workflow_instance_id, wi.task_id, sr.step_id, sr.state
FROM step_runs sr
JOIN workflow_instances wi ON wi.id = sr.workflow_instance_id
WHERE wi.task_id IN (?, ?, …)
  AND wi.state NOT IN ('done','failed','canceled')
ORDER BY sr.rowid
```

Position and total are computed in Go from the returned rows plus the instance's step sequence.
The filter is the vocabulary-agnostic negative form introduced in `unified-task-state-model`
Release N, so it cannot miss a live row.

New fields on `TaskItem`, zero-valued for legacy Agents-tab rows exactly as the Phase 9 internal
task fields already are:

```go
StepID       string // current step id; empty when not resolvable
StepPosition int    // 1-based; 0 when unknown
StepTotal    int    // 0 when unknown
LiveInstances int   // >1 drives the fan-out marker
```

### 3. Board view

A sub-view of the Tasks tab, not a new tab — it is the same data with a different projection, and
sharing the tab means the existing filter, sort, and selection machinery is reused rather than
duplicated.

```
  QUEUED (2)        RUNNING (3)        BLOCKED (4)        DONE (12)
 ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐
 │ #418         │  │ #412         │  │ #401         │  │ #396         │
 │ Add plugin…  │  │ Fix approval…│  │ Port pgsink… │  │ Docs: plugins│
 │ —            │  │ review 3/5   │  │ ci 4/6       │  │ 5/5          │
 │ 2m           │  │ 4m           │  │ approval 1h  │  │ 20m          │
 └──────────────┘  └──────────────┘  └──────────────┘  └──────────────┘

  FAILED (1)  — needs attention
 ┌───────────────────────────────────────────────────────────────────┐
 │ #407  Improve advisor evidence pack   build 2/7   exhausted  3h   │
 └───────────────────────────────────────────────────────────────────┘
```

**Columns are canonical states, one to one.** No derivation, no mapping table — the thing
`unified-task-state-model` buys. A card is in `BLOCKED` because its state column says `blocked`.

**`FAILED` is a lane, not a column,** because it is not a stage work passes through. Terminal
failure is an exception awaiting a human, and giving it a column implies work flows into it and
onward. The lane sits below the board, full width, and is empty (collapsed to a single line) when
nothing has failed — which is the point: it is a visible zero.

**`CANCELED` is not shown by default.** It is terminal and operator-initiated; the operator
already knows. Reachable through the existing filter.

**Card contents**: number, truncated title, progress (§1), the blocked reason where applicable, and
age. The agent is deliberately absent for the reason in Why — it is per-step, and the card is
per-task.

**`DONE` is capped and time-boxed** — the newest N within the list's existing time window. Without
a cap the column grows without bound and squeezes the columns that matter. The header shows the
true count, so the cap never hides the number, only the cards.

### 4. Keybinding and navigation

`b` toggles list ↔ board. It is unbound today (taken single-letter keys in `app.go` are
`a d f g l m r s t w y R S G X`).

| Key | Board behaviour |
|---|---|
| `←` `→` | move between columns |
| `↑` `↓` | move within a column |
| `enter` | open the selected card's task detail — the same `TaskViewDetail` the list opens |
| `esc` | back to the list |
| `/` | the existing filter, applied to cards |
| `b` | back to the list |

Sort keys (`title`, `status`, `updated`, …) do not apply in the board: column membership *is* the
grouping, and within a column cards sort by age, newest first. The sort indicator hides rather
than showing a control that does nothing.

### 5. Card actions — the board is operational, not a wallboard

The board is a working surface. Every operator verb the Tasks tab and workflow monitor already
bind is available on the selected card, using the **same key as elsewhere**, so nothing has to be
re-learned:

| Key | Action | Already bound in |
|---|---|---|
| `enter` / `d` | open task detail | tasks list |
| `l` | open logs for the task's current step | monitor (`app.go:1409`) |
| `a` / `y` / `n` | answer the approval gate the card is blocked on | monitor (`app.go:1439`) |
| `R` | restart the workflow | monitor (`app.go:1434`) |
| `X` | stop the workflow instance | monitor (`app.go:1429`) |
| `r` | refresh | tasks list |

The `a`/`y`/`n` case is the one that makes the board more than a nicer list: an operator opens the
board, sees `BLOCKED (4)`, and answers four approval gates without ever drilling into a task. That
is the workflow the current UI makes tedious — today the same four approvals require finding each
task in a reverse-chronological list, entering it, opening the monitor, and answering.

Keys act on the selected card only; there is no multi-select in this change.

**What the board deliberately does not do is let you move a card by hand.** A drag from `RUNNING`
to `DONE` would assert something about the hive that never happened. State is a *consequence* of
execution, and the board's honesty depends on that staying true — every action above asks the
engine to do something and lets the resulting state move the card. This is the one interaction a
kanban UI normally has that Apiary's board must not.

### 6. Board or list, as a real choice

The board is not a peek-and-return mode. An operator who runs a hive all day and wants the board
as their normal view should get it without pressing `b` every session.

Three levels, cheapest first:

| Mechanism | Scope |
|---|---|
| `b` | toggles for the current session |
| `apiary dashboard --view=board` | per invocation; no config needed |
| `settings.dashboard.default_view: board \| list` | persistent, per hive |

`Settings` (`internal/config/config.go:357`) has no `dashboard` block today; this adds one with a
single field. It is an additive, optional config key with `list` as the default, so no existing
`apiary.yaml` changes meaning — the same shape as every other setting added since the beta.

The list stays the default for a new hive: it is the view that degrades to any terminal size and
carries the full history, and the board's value shows up once a hive has enough concurrent work to
need grouping.

### 7. Narrow terminals

Four columns need roughly 60 characters. Below that the board degrades rather than wraps: columns
are dropped from the right (`DONE` first, then `QUEUED`) and their counts move into the header as
`+12 done`. Below ~40 characters the board refuses to render and falls back to the list with a
one-line notice, which is honest about not fitting instead of producing an unreadable grid.

---

## What Stays

- **The list remains the default for a new hive.** It degrades to any terminal size and carries
  the full history; the board becomes the better default once a hive runs enough concurrent work
  to need grouping, which is why it is selectable rather than imposed (§6).
- **The workflow monitor** (`TaskViewWorkflow`) is unchanged and remains the per-task drill-down.
  The board is a fleet view; the monitor is a task view. They are not competing.
- **Filtering and the task detail view** are reused as-is.
- **The Agents tab** keeps its agent-centric rows — that is the tab where an agent column is the
  correct primary key.
- **`TaskItem`'s existing fields**, including `Agent`, which continues to populate the detail view
  and the Agents tab.

---

## Implementation Plan

### Phase 1 — Progress column

1. Add `StepID` / `StepPosition` / `StepTotal` / `LiveInstances` to `TaskItem`.
2. Add the batched progress query (§2), scoped to the visible page.
3. Resolve position and total against the instance's stored step sequence.
4. Replace the `AGENT` column with the progress column in the list renderer (`app.go:3549`).
5. Fan-out rule and truncation rule, with tests for each row in the §1 table.

### Phase 2 — Board view

6. Add `TaskViewBoard` and the `b` binding.
7. Column layout, card rendering, selection model.
8. `FAILED` lane, including its collapsed-when-empty form.
9. `DONE` cap and time-boxing.
10. Card actions (§5): the existing `l` / `a` / `y` / `n` / `R` / `X` verbs, acting on the
    selected card.
11. Narrow-terminal degradation (§7).
12. `--view` flag and `settings.dashboard.default_view` (§6), with config validation.

### Phase 3 — Surfaces

13. `docs/dashboard.md`: the new column, the board, the keymap, the default-view setting.
14. `docs/configuration.md` and the JSON Schema: the new `settings.dashboard` block.
15. Screenshots in `docs/screenshots`.

---

## Out of Scope

- **Drag-and-drop / moving cards between columns.** State is a consequence of execution, not
  something an operator sets by moving a card. A board that let you drag a task into `DONE` would
  be lying about what the hive did. Operator actions that *do* change state (restart, cancel,
  approve) stay on their existing keys.
- **A board for workflow instances or steps.** Cards are tasks in this change.
- **Multi-select on the board.** Card actions apply to the selected card only; answering ten
  approvals is ten keypresses, not a bulk operation. Worth revisiting once the board exists.
- **Swimlanes by source, agent, or workflow.** Worth having; needs the board to exist first.
- **Colour themes for the board.** Reuses the existing `Style*` palette unchanged.
- **Replacing the workflow monitor.** Explicitly not the goal (see What Stays).
