# GitHub Source

The `github` source adapter polls issues from a GitHub repository and maps them
to Apiary Cells for routing and dispatch. Pull requests are **not** ingested:
GitHub's issues endpoint also returns PRs (every PR is an issue in the API), but
the adapter skips them — PRs are implementation artifacts, not work items.

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
| **AddLabels** | `PATCH /repos/{owner}/{repo}/issues/{number}` (replaces labels) | Via `route.on_complete.add_labels` |

GitHub's `/issues` endpoint also returns pull requests, but the adapter filters
them out during polling — only plain issues become cells (always
`Type: "issue"`).

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
- **AddLabels** — adds labels via `on_complete.add_labels`

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
| `types` | `[string]` | Only match cells whose type is in this list (GitHub cells are always `"issue"`) |
| `title_regex` | `string` | Cell title must match this Go regexp |
| `priority` | `[string]` | Only match cells whose priority is in this list |

### `on_complete` fields

| Field | Type | Description |
|---|---|---|
| `set_state` | `string` | Transition the cell to this state after a successful run |
| `add_labels` | `[string]` | Add these labels to the cell after a successful run |

### Example: route by label

```yaml
routes:
  - id: engineer-implement
    priority: 20
    match:
      source: my-repo
      labels: [agent:engineer]
    agent: engineer
    on_complete:
      set_state: closed
```

The route matches any cell carrying the `agent:engineer` label and dispatches
it to the engineer agent, closing the issue when the run succeeds.

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

## Setting up locally

1. Create a [fine-grained PAT](https://github.com/settings/tokens?type=beta)
2. Set it in `.env` or export: `export GITHUB_TOKEN=ghp_...`
3. Run: `apiary run --config apiary.yaml`
