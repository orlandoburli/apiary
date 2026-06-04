# GitHub Issues Source

The `github` source adapter polls issues from a GitHub repository and maps them to Apiary Cells for routing and dispatch.

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
| `base_url` | no | `https://api.github.com` | API base URL for GHES self-hosted instances |

### `filters`

| Field | Description |
|---|---|
| `states` | Filter by issue state: `open` or `closed` |
| `labels` | Only poll issues that have **all** of these labels |

## Token permissions

The `api_key` is a GitHub personal access token. Without it, the adapter falls back to unauthenticated requests (rate limit of 60 requests/hour — insufficient for regular polling).

### Classic PAT

Scope **`repo`** (for private repositories). This grants full access to issues, labels, and comments.

### Fine-grained PAT

Repository-scoped token with the following permissions:

| Permission | Level | Why |
|---|---|---|
| `Issues` | **Read & Write** | Poll issues, update state, add labels, post comments |
| `Metadata` | **Read** (auto-granted) | Read repository metadata, list labels |

Generate one at `https://github.com/settings/tokens?type=beta` → select the target repository → set `Issues: Read and Write`.

## What the adapter does

| Operation | GitHub API call | When |
|---|---|---|
| **Poll** | `GET /repos/{owner}/{repo}/issues?state=all&sort=updated` | Every `poll_interval` |
| **Acknowledge** | `PATCH /repos/{owner}/{repo}/issues/{number}` (adds label `in-progress`) | When `settings.state_lock: true` |
| **WriteResult** | `POST /repos/{owner}/{repo}/issues/{number}/comments` | When `settings.result_comment: true` |
| **SetState** | `PATCH /repos/{owner}/{repo}/issues/{number}` (sets `state`) | Via `route.on_complete.set_state` |
| **AddLabels** | `PATCH /repos/{owner}/{repo}/issues/{number}` (replaces labels) | Via `route.on_complete.add_labels` or `assign_from_output` |

Pull requests are automatically filtered out (the GitHub Issues API returns both issues and PRs).

## Runners

Providers are configured via `type` (execution mode) + `provider` (AI provider):

```yaml
runners:
  # Claude CLI — type=cli, provider=claude
  - id: claude
    type: cli
    provider: claude
    config:
      args: ["--output-format", "stream-json", "--verbose"]
    models:
      - claude-opus-4-8
      - claude-sonnet-4-6
      - claude-haiku-4-5

  # OpenCode CLI — type=cli, provider=opencode
  - id: opencode
    type: cli
    provider: opencode
    models:
      - opencode-go/deepseek-v4-pro
      - opencode-go/deepseek-v4-flash

  # OpenCode API — type=api, provider=opencode
  - id: opencode-api
    type: api
    provider: opencode
    config:
      subscription: go
      api_key: ${OPENCODE_API_KEY}
```

The `type` field determines the execution engine (`cli` = subprocess, `api` = HTTP).
The `provider` field selects the AI provider defaults (command, flags, endpoint).
The internal adapter name is resolved as `{provider}-{type}` (e.g. `claude-cli`).

Each runner can declare a `models` list used by the dashboard for model cycling.

## Per-agent source identity (`source_token`, `source_email`, `source_name`)

Each agent can use its own GitHub account for write operations and git commits
by setting source identity fields:

```yaml
agents:
  - id: engineer
    description: "Implements tasks"
    model: claude-sonnet-4-6
    source_token: ${GITHUB_TOKEN_ENGINEER}    # issue comments/labels as engineer-bot
    source_email: engineer@company.com
    source_name: Engineer Bot

  - id: reviewer
    description: "Reviews code"
    model: claude-sonnet-4-6
    source_token: ${GITHUB_TOKEN_REVIEWER}    # issue comments/labels as reviewer-bot
    source_email: reviewer@company.com
    source_name: Reviewer Bot
```

### `source_token`

When set, the adapter's **write operations** use this GitHub token instead
of the source-level `api_key`:
- **Acknowledge** (adds `in-progress` label)
- **WriteResult** (posts run output as comment)
- **SetState** (closes/re-opens issue via `on_complete.set_state`)
- **AddLabels** (adds labels via `on_complete.add_labels` or `assign_from_output`)

**Poll** always uses the source-level `api_key` — one account reads all issues.

Token permissions are the same as the source-level token (see above).

### `source_email` / `source_name`

When set, these are passed to the runner as git environment variables:
- `GIT_AUTHOR_NAME` / `GIT_COMMITTER_NAME`
- `GIT_AUTHOR_EMAIL` / `GIT_COMMITTER_EMAIL`

This ensures commits made by the agent use the correct author identity
instead of a shared system user. `source_name` defaults to the token's
GitHub username if not set; `source_email` is required for proper commit
attribution.

## Per-agent concurrency (`max_workers`)

Each agent runs independently — one agent's long-running tasks don't starve
another. Set `max_workers` to control how many tasks an agent can handle at
once:

```yaml
agents:
  - id: engineer
    description: "Implements tasks"
    soul_file: .apiary/agents/engineer.md
    preferred_models: [claude-sonnet-4-6]
    max_workers: 3       # up to 3 engineers in parallel

  - id: reviewer
    description: "Reviews code"
    soul_file: .apiary/agents/reviewer.md
    preferred_models: [claude-sonnet-4-6]
    max_workers: 2       # reviewers don't block on engineers
```

Default is 1 if not set. The global `settings.concurrency` now only limits
concurrent polls (one source at a time).

## Routing with `agent:*` labels

A common pattern is to label GitHub issues with `agent:<role>` and configure routes matching that label:

```yaml
routes:
  - id: engineer-implement
    priority: 20
    match:
      source: my-repo
      labels: [agent:engineer]
    agent: engineer
    worker: engineer-worker
    on_complete:
      set_state: closed

  - id: new-issue-classify
    priority: 100
    match:
      source: my-repo
      exclude_label_prefix: "agent:"
    agent: investigator
    worker: investigator-worker
    on_complete:
      assign_from_output: true
```

The fallback route catches issues without an `agent:*` label. The investigator agent classifies them and outputs `APIARY-ASSIGN: <role>`, which Apiary converts to the label `agent:<role>`. On the next poll, the matching agent route fires.

## Setting up locally

1. Create a [fine-grained PAT](https://github.com/settings/tokens?type=beta) with `Issues: Read & Write`
2. Export it: `export GITHUB_TOKEN=ghp_...`
3. Run: `apiary run --config apiary.yaml`
