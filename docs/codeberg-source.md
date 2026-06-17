# Codeberg Source

The `codeberg` source adapter polls issues from a [Codeberg](https://codeberg.org)
repository — or **any self-hosted [Forgejo](https://forgejo.org) or Gitea
instance** — and maps them to Apiary tasks for routing and dispatch. The Forgejo
API closely mirrors GitHub's, so this adapter behaves like the
[GitHub source](github-source.md) with a few platform differences noted below.

Pull requests are **not** ingested: Forgejo's issues endpoint also returns PRs
(a PR is an issue in the API), but the adapter skips them — PRs are
implementation artifacts, not work items.

## Configuration

```yaml
sources:
  - id: my-codeberg
    type: codeberg
    config:
      repo: my-org/my-repo
      api_key: ${CODEBERG_TOKEN}
      # base_url: https://git.example.org/api/v1   # any Forgejo/Gitea host
    poll_interval: 120s
    filters:
      states: [open]
      labels: [ai-ready]
```

### `config` fields

| Field | Required | Default | Description |
|---|---|---|---|
| `repo` | yes | — | Repository in `owner/repo` format |
| `api_key` | no | — | Codeberg/Forgejo access token (see permissions below) |
| `base_url` | no | `https://codeberg.org/api/v1` | API base for a self-hosted Forgejo/Gitea instance (include the `/api/v1` suffix) |

### `filters`

| Field | Description |
|---|---|
| `states` | Filter by issue state: `open` or `closed` |
| `labels` | Only poll issues that have **all** of these labels |

## What the adapter does

| Operation | Forgejo API call | When |
|---|---|---|
| **Poll** | `GET /repos/{owner}/{repo}/issues?type=issues&state=open` | Every `poll_interval` |
| **Acknowledge** | `POST /repos/{owner}/{repo}/issues/{index}/labels` (adds `in-progress`) | When `settings.state_lock: true` |
| **WriteResult** | `POST /repos/{owner}/{repo}/issues/{index}/comments` | When `settings.result_comment: true` |
| **SetState** | `PATCH /repos/{owner}/{repo}/issues/{index}` (sets `state`) | Via `workflow.on_complete.set_state` |
| **AddLabels** | `POST /repos/{owner}/{repo}/issues/{index}/labels` (by label id) | Via `workflow.on_complete.add_labels` |
| **RemoveLabels** | `DELETE /repos/{owner}/{repo}/issues/{index}/labels/{id}` (one per label) | Via `workflow.on_complete.remove_labels` |
| **CI wait** | `GET /repos/{owner}/{repo}/commits/{sha}/status` | Via `wait_for` step, `kind: ci` |
| **Dependency wait** | `GET /repos/{owner}/{repo}/issues/{index}/dependencies` | Via `wait_for` step, `kind: dependency` |

## Differences from GitHub

The Forgejo/Gitea API diverges from GitHub in ways the adapter handles transparently:

- **Auth header** — Forgejo uses `Authorization: token <TOKEN>` (not `Bearer`).
- **Labels are operated by id, not name.** The adapter lists the repo's labels,
  creates any missing label (Forgejo requires a color — a neutral grey is used),
  and applies labels by their numeric id.
- **CI status comes from commit statuses, not check-runs.** Forgejo has no
  check-runs API; Forgejo Actions and any external CI both report through commit
  statuses, which the adapter aggregates into an overall verdict.
- **Merge conflicts** are detected via the PR's boolean `mergeable` flag (Forgejo
  has no GitHub-style `mergeable_state`). A conflict surfaces as the `conflict`
  status so a `wait_for` step's `on_conflict` edge can hand the PR back to the
  engineer.
- **Blocking relationships** use Forgejo's native issue **dependencies** (the
  issues a task depends on). Unlike Jira there is no link-type to configure.
- **No sub-issues.** Forgejo has no parent/child issue API, so the `codeberg`
  source cannot materialize spawned tasks as sub-issues (`materialize: sub_issue`).
  Model parent/child with a dependency edge or a label convention instead.

## Token permissions

The `api_key` is a Codeberg/Forgejo access token, created at
**Settings → Applications → Generate New Token**
(`https://codeberg.org/user/settings/applications`).

| Scope | Why |
|---|---|
| `read:repository` | Poll issues, read PR status for CI waits, list dependencies |
| `write:issue` | Update state, add/remove labels, post comments |
| `write:repository` | Required if agents open pull requests with this token |

Without a token the adapter falls back to unauthenticated requests, which only
see public repositories and are rate-limited — insufficient for regular polling.

## Per-agent source identity (`source_token`, `source_email`, `source_name`)

Like the GitHub source, each agent can use its own Forgejo account for issue
operations and git commits:

```yaml
agents:
  - id: engineer
    model: claude-sonnet-4-6
    source_token: ${CODEBERG_TOKEN_ENGINEER}
    source_email: engineer@company.com
    source_name: Engineer Bot
```

When `source_token` is set, the adapter's **write operations** (Acknowledge,
WriteResult, SetState, AddLabels, RemoveLabels) use it instead of the
source-level `api_key`. **Poll** always uses the source-level `api_key`.

`source_email` / `source_name` are injected as `GIT_AUTHOR_*` / `GIT_COMMITTER_*`
in the runner subprocess so commits carry the correct identity.

## Environment variables

Apiary auto-loads `.env` from the config directory:

```bash
# .env (next to apiary.yaml)
CODEBERG_TOKEN=...
CODEBERG_TOKEN_ENGINEER=...
```

## Setting up locally

1. Create a token at `https://codeberg.org/user/settings/applications`.
2. Set it in `.env` or export: `export CODEBERG_TOKEN=...`
3. Run: `apiary run --config apiary.yaml`

For a self-hosted Forgejo/Gitea instance, also set
`config.base_url: https://your-host/api/v1`.
