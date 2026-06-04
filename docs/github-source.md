# GitHub Source

The `github` source adapter polls issues from a GitHub repository and maps them
to Apiary Cells for routing and dispatch. Pull requests are also included as
cells with `Type: "pull_request"`.

## Configuration

```yaml
sources:
  - id: my-repo
    type: github
    config:
      repo: my-org/my-repo
      api_key: ${GITHUB_TOKEN}
    poll_interval: 120s
    filters:
      states: [open]
      labels: [ai-ready]
```

### `config` fields

| Field | Required | Default | Description |
|---|---|---|---|
| `repo` | yes | — | Repository in `owner/repo` format |
| `api_key` | no | — | GitHub personal access token (see permissions below) |
| `base_url` | no | `https://api.github.com` | API base URL for GHES |

### `filters`

| Field | Description |
|---|---|
| `states` | Filter by issue state: `open` or `closed` |
| `labels` | Only poll issues that have **all** of these labels |

## What the adapter does

| Operation | GitHub API call | When |
|---|---|---|
| **Poll** | `GET /repos/{owner}/{repo}/issues?state=open&sort=updated&direction=desc` | Every `poll_interval` |
| **Acknowledge** | `POST /repos/{owner}/{repo}/issues/{number}/labels` (adds `in-progress`) | When `settings.state_lock: true` |
| **WriteResult** | `POST /repos/{owner}/{repo}/issues/{number}/comments` | When `settings.result_comment: true` |
| **SetState** | `PATCH /repos/{owner}/{repo}/issues/{number}` (sets `state`) | Via `route.on_complete.set_state` |
| **AddLabels** | `PATCH /repos/{owner}/{repo}/issues/{number}` (replaces labels) | Via `route.on_complete.add_labels` or `assign_from_output` |

Both issues and pull requests are returned by the poll. PRs are not filtered
out — they get `Type: "pull_request"` and can be routed independently via
`match.types: ["pull_request"]`.

## Token permissions

The `api_key` is a GitHub personal access token. Without it, the adapter falls
back to unauthenticated requests (60 req/h — insufficient for regular polling).

### Classic PAT

Scope **`repo`** (for private repos).

### Fine-grained PAT

| Permission | Level | Why |
|---|---|---|
| `Issues` | **Read & Write** | Poll, update state, add labels, post comments |
| `Metadata` | **Read** (auto-granted) | Read repo metadata, list labels |

## Per-agent source identity (`source_token`, `source_email`, `source_name`)

Each agent can use its own GitHub account for issue operations and git commits:

```yaml
agents:
  - id: engineer
    description: "Implements tasks"
    model: claude-sonnet-4-6
    source_token: ${GITHUB_TOKEN_ENGINEER}
    source_email: engineer@company.com
    source_name: Engineer Bot

  - id: reviewer
    description: "Reviews code"
    model: claude-sonnet-4-6
    source_token: ${GITHUB_TOKEN_REVIEWER}
    source_email: reviewer@company.com
    source_name: Reviewer Bot
```

### `source_token`

When set, the adapter's **write operations** use this token instead of the
source-level `api_key`:
- **Acknowledge** — adds `in-progress` label
- **WriteResult** — posts comment with run output
- **SetState** — closes/re-opens issue via `on_complete.set_state`
- **AddLabels** — adds labels via `on_complete.add_labels` or `assign_from_output`

**Poll** always uses the source-level `api_key` — one account reads all issues.

### `source_email` / `source_name`

These are injected as git environment variables in the runner subprocess:
- `GIT_AUTHOR_NAME` / `GIT_COMMITTER_NAME`
- `GIT_AUTHOR_EMAIL` / `GIT_COMMITTER_EMAIL`

Ensures commits use the correct author identity instead of a shared system user.

## Per-agent concurrency (`max_workers`)

Each agent has its own semaphore — one agent's long-running tasks don't starve
another:

```yaml
agents:
  - id: engineer
    description: "Implements tasks"
    soul_file: .apiary/agents/engineer.md
    model: claude-sonnet-4-6
    max_workers: 3

  - id: reviewer
    description: "Reviews code"
    soul_file: .apiary/agents/reviewer.md
    model: claude-sonnet-4-6
    max_workers: 2
```

Default is 1 if not set. Polls are serialized (one source at a time) regardless
of `settings.concurrency`.

## Routing (`routes`)

Routes are evaluated in `priority` ascending order. The first match wins.

### `match` fields

| Field | Type | Description |
|---|---|---|
| `source` | `string` | Only match cells from this source ID |
| `labels` | `[string]` | Cell must have **all** of these labels (case-insensitive) |
| `exclude_labels` | `[string]` | Cell must NOT have any of these labels |
| `exclude_label_prefix` | `string` | Cell must not have a label starting with this prefix |
| `states` | `[string]` | Only match cells whose state is in this list |
| `types` | `[string]` | Only match cells whose type is in this list (e.g. `["pull_request"]`) |
| `title_regex` | `string` | Cell title must match this Go regexp |
| `priority` | `[string]` | Only match cells whose priority is in this list |

### `on_complete` fields

| Field | Type | Description |
|---|---|---|
| `set_state` | `string` | Transition the cell to this state after a successful run |
| `add_labels` | `[string]` | Add these labels to the cell after a successful run |
| `assign_from_output` | `bool` | Parse `APIARY-ASSIGN: <agent>` from output and add label `agent:<agent>` |
| `assign_label_prefix` | `string` | Label prefix for assign_from_output (default: `"agent:"`) |

### Example: issue classification flow

```yaml
routes:
  # Specific agent routes — evaluated first (lower priority wins)
  - id: engineer-implement
    priority: 20
    match:
      source: my-repo
      labels: [agent:engineer]
    agent: engineer
    on_complete:
      set_state: closed

  # Fallback: classify unassigned issues
  - id: new-issue-classify
    priority: 100
    match:
      source: my-repo
      exclude_label_prefix: "agent:"
    agent: investigator
    on_complete:
      assign_from_output: true
```

The fallback catches any cell without an `agent:*` label. The investigator
classifies it and outputs `APIARY-ASSIGN: engineer`, which Apiary converts
to the label `agent:engineer`. On the next poll, the matching route fires.

## PR Review

When a cell has `Type: "pull_request"` and the agent's output contains an
`APIARY-REVIEW` directive, the dispatcher submits a formal GitHub PR review.

### Directive

The agent outputs one of these (last directive wins):

```
APIARY-REVIEW: approve
APIARY-REVIEW: request-changes
APIARY-REVIEW: comment
```

### Route

```yaml
routes:
  - id: review-pr
    priority: 12
    match:
      source: my-repo
      types: [pull_request]
    agent: reviewer
    on_complete:
      set_state: closed
```

### How it works

1. Poll returns the PR as a cell with `Type: "pull_request"`
2. Route matches by `types: [pull_request]` → dispatches to reviewer agent
3. Reviewer agent gets the PR number (`cell.ID`), description, labels
4. Agent reviews the code (e.g. via `gh pr diff {number}`)
5. Agent outputs `APIARY-REVIEW: approve`
6. Dispatcher calls `POST /repos/{owner}/{repo}/pulls/{number}/reviews`
   using the agent's `source_token` — the review comes from the agent's
   GitHub identity

PR review is independent of the source adapter. The repo is parsed from the
cell's URL, and the token comes from `agent.source_token` (with fallback to
the source-level `api_key`).

## Environment variables

### `.env` auto-load

When loading a config file, Apiary automatically reads `.env` from the same
directory and calls `os.Setenv` for each entry. Already-set env vars (e.g.
from shell `export`) take priority.

```bash
# .env (next to apiary.yaml)
GITHUB_TOKEN=ghp_abc123
GITHUB_TOKEN_ENGINEER=ghp_def456
GITHUB_TOKEN_REVIEWER=ghp_ghi789
```

No manual sourcing needed — `Config.Load()` handles it.

### Full list

| Variable | Used By |
|---|---|
| `GITHUB_TOKEN` | Source adapter — poll + fallback for write operations |
| `GITHUB_TOKEN_<AGENT>` | Agent's `source_token` — overrides source token for writes |
| `GIT_AUTHOR_NAME` | Runner — set from `agent.source_name` |
| `GIT_AUTHOR_EMAIL` | Runner — set from `agent.source_email` |
| `GIT_COMMITTER_NAME` | Runner — same as `GIT_AUTHOR_NAME` |
| `GIT_COMMITTER_EMAIL` | Runner — same as `GIT_AUTHOR_EMAIL` |

## `on_complete` directives

Agents can output directives in their final output to trigger side effects:

| Directive | Example | Effect |
|---|---|---|
| `APIARY-ASSIGN` | `APIARY-ASSIGN: engineer` | Adds label `agent:engineer` (via `assign_from_output`) |
| `APIARY-REVIEW` | `APIARY-REVIEW: approve` | Submits PR review (approve/request-changes/comment) |

## Setting up locally

1. Create a [fine-grained PAT](https://github.com/settings/tokens?type=beta)
2. Set it in `.env` or export: `export GITHUB_TOKEN=ghp_...`
3. Run: `apiary run --config apiary.yaml`
