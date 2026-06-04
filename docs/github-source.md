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

The `cli` runner type is commonly used with the GitHub source to execute AI agents (Claude, OpenCode, etc.).

```yaml
runners:
  - id: claude-cli
    type: cli
    config:
      command: claude
      model_flag: --model
      prompt_flag: -p
      args: ["--output-format", "stream-json", "--verbose"]

  - id: opencode-cli
    type: opencode
    config:
      mode: cli
      subscription: go
      binary: opencode
      agent: backend-dev
      model_flag: --model
      prompt_flag: --prompt
      turns_flag: --max-turns

  - id: script-runner
    type: script
    config:
      command: /bin/sh
```

See `apiary.yaml` schema docs for full runner options.

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
