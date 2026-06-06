# Specification: Source Adapter — PollTask

Extend `SourceAdapter` with a `PollTask` method so the workflow engine can check the current state of a specific task during approval steps.

## Problem

The current `SourceAdapter` interface only fetches *new* tasks via `Poll(ctx, since)`. Approval steps require the engine to detect changes on an *existing* task (a new comment, a label addition, a state change) after the workflow has paused. There is no mechanism for this today.

## Solution

Add a single new method `PollTask` to the interface. It returns the current state of one task by ID. The engine calls it on each poll cycle for every workflow instance in `approval_waiting` state and evaluates the approval trigger conditions against the returned Cell.

This fits the existing poll-first model (ADR-002) and requires no streaming infrastructure.

## Interface Change

```go
type SourceAdapter interface {
    ID() string
    Connect(ctx context.Context, config map[string]any) error
    Poll(ctx context.Context, since time.Time) ([]Cell, error)
    PollTask(ctx context.Context, cellID string) (Cell, error)   // NEW
    Acknowledge(ctx context.Context, cell Cell, action AckAction) error
    WriteResult(ctx context.Context, cell Cell, result RunResult) error
    WebhookHandler() http.Handler
}
```

### `PollTask(ctx, cellID string) (Cell, error)`

Returns the current state of the task with the given native ID. The returned Cell must include:
- `State` — current state name in the source system
- `Labels` — current label list
- `Comments []Comment` — comments added since the workflow instance was created (see below)

Adapters that do not support per-task polling return `ErrNotSupported`. The engine treats `ErrNotSupported` as "approval cannot proceed on this source" and fails the workflow instance immediately with a descriptive error.

## Cell Extension: Comments

To support `comment_contains` approval triggers, `Cell` gains a `Comments` field:

```go
type Cell struct {
    // ... existing fields ...
    Comments []Comment   // NEW: populated only by PollTask, not by Poll
}

type Comment struct {
    ID        string
    Body      string
    CreatedAt time.Time
}
```

`Poll` implementations leave `Comments` empty (not needed for task discovery). `PollTask` implementations populate it with comments added after the workflow instance `created_at` timestamp, to avoid matching comments that pre-date the approval step.

## Engine Behavior

On each source poll cycle, after calling `Poll` for new tasks, the engine:

1. Queries SQLite for all `workflow_instances` in state `approval_waiting` for this source.
2. For each instance, calls `source.PollTask(ctx, instance.CellID)`.
3. Evaluates the approval step's `resume_on` and `abort_on` conditions against the returned Cell.
4. If `resume_on` matches: marks the approval step `passed`, resumes the workflow.
5. If `abort_on` matches: marks the instance `failed`, applies `on_fail` hooks.
6. If `timeout` is set and elapsed: same as `abort_on`.
7. Otherwise: no action; checked again on the next cycle.

`PollTask` is called once per `approval_waiting` instance per poll cycle. The poll interval for approval checking is the source's configured `poll_interval` — no separate interval.

## Approval Condition Evaluation

Conditions are evaluated against the Cell returned by `PollTask`:

| Trigger field | Evaluated against |
|---|---|
| `comment_contains: "approve"` | Any comment body containing the string (case-insensitive) |
| `label_added: "approved"` | `cell.Labels` contains the label |
| `state_changed: "in_review"` | `cell.State == "in_review"` |

Multiple fields within a single `resume_on` or `abort_on` block are OR-conditions: any one match is sufficient to trigger.

## Adapter Implementations

### Plane

Use the Plane REST API: `GET /api/v1/workspaces/{slug}/projects/{id}/issues/{issue_id}/` for state/labels, and `GET /api/v1/workspaces/{slug}/projects/{id}/issues/{issue_id}/comments/` for comments.

### GitHub Issues

Use the GitHub REST API: `GET /repos/{owner}/{repo}/issues/{issue_number}` for state/labels, and `GET /repos/{owner}/{repo}/issues/{issue_number}/comments` for comments. Filter comments created after `instance.created_at`.

### Other Adapters (Jira, Linear, future)

Return `ErrNotSupported` until implemented. The workflow engine logs a warning and fails the instance.

## Validation

At config load time: if any workflow contains a `type: approval` step and the workflow's trigger source does not support `PollTask`, emit a warning (not an error — the source may be added later). At runtime when an approval step is reached: if `PollTask` returns `ErrNotSupported`, fail the instance immediately.
