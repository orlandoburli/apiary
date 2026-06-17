# Design: Git Forge Sources

## Where the seams already are

Adding a forge is implementing `source.Adapter` (`src/internal/source/source.go`)
plus the optional capability interfaces, registering a factory, and blank-importing
the package in `cmd/apiary/main.go`. The dispatcher resolves `source.New(type)` per
configured source; the workflow engine calls capability interfaces by type
assertion, so a forge that implements `CIStatusPoller` can host `wait_for kind: ci`
and one that implements `BlockerLister` can host `kind: dependency` — with no
engine change. Config validation already gates dependency waits behind
`config.SourceSupportsDependencyWait`, which instantiates the adapter and checks
for `BlockerLister`.

The GitHub adapter (`src/internal/source/github/`) is the structural template.

## Forge primitive mapping

| Concept | GitHub | Codeberg / Forgejo | GitLab |
|---|---|---|---|
| Auth header | `Authorization: Bearer` | `Authorization: token` | `PRIVATE-TOKEN` or Bearer |
| API base | `api.github.com` | `{host}/api/v1` | `{host}/api/v4` |
| List issues (excl. PRs) | `/issues` + skip `pull_request` | `/issues?type=issues` + skip | `/projects/{id}/issues` (issues only natively) |
| Comment | issue comment | issue comment | issue **note** |
| State change | `state` open/closed | `state` open/closed | `state_event` close/reopen |
| Labels | by name | **by id** (resolve/create) | comma-separated names |
| PR/MR for issue | timeline cross-ref | timeline (`ref_issue.pull_request`) | `/issues/{i}/related_merge_requests` |
| Merge conflict | `mergeable_state == dirty` | `mergeable == false` | MR `has_conflicts == true` |
| CI status | check-runs + commit status | **commit status only** | **pipelines** + jobs |
| Blockers | `dependencies/blocked_by` | `/issues/{i}/dependencies` | issue links `blocks` (Premium) |
| Sub-issues | native | **none** | epics / child issues |

## Phase 1 — Codeberg/Forgejo (implemented here)

Package `src/internal/source/codeberg/` — `adapter.go`, `client.go`, `types.go`,
`adapter_test.go`. Capabilities: `StateSetter`, `LabelAdder`, `LabelRemover`,
`TaskPoller`, `CIStatusPoller`, `PullRequestLister`, `BlockerLister`. **Not**
`SubIssueCreator`.

Key decisions, grounded in the verified Forgejo/Gitea API:

- **Auth.** `Authorization: token <TOKEN>` — the universally supported PAT scheme
  across Gitea/Forgejo versions. Per-agent override via `source.SourceTokenCtxKey`,
  same as GitHub.
- **Host-agnostic.** `base_url` defaults to `https://codeberg.org/api/v1`; any
  Forgejo/Gitea host works by overriding it. The browser host is derived by
  stripping the API suffix.
- **Labels are ids, not names.** Forgejo's add/remove endpoints and create-issue
  body work in numeric label ids (name support is version-gated). The adapter
  caches the repo's labels (`name → id`, mutex-guarded), creates a missing label
  (Forgejo requires a `color`; a neutral grey is used), and applies/removes by id.
  `AddLabels` POSTs ids (Forgejo merges into the set); `RemoveLabels` issues one
  DELETE per id and skips labels absent from the repo or the issue (404).
- **CI = commit statuses.** Forgejo has no check-runs API; Forgejo Actions and
  external CI both post commit statuses. `PollCIStatus` finds the PR via timeline,
  reads `GET /commits/{sha}/status`, and aggregates per-context statuses
  (`success`/`warning → passed`, `failure`/`error → failed`, `pending`,
  `skipped`); an empty set is `pending` (CI not started).
- **Conflict detection.** Forgejo exposes a boolean `mergeable` (no
  `mergeable_state`). An unmerged PR with `mergeable == false` surfaces as
  `conflict`. Forgejo computes mergeability lazily, so a freshly pushed PR can
  report a stale value briefly — acceptable because the `wait_for` step's
  `on_conflict` edge has its own retry budget.
- **Blockers = native dependencies.** `GET /issues/{i}/dependencies` returns the
  issues this one depends on. Closed → `done`; an open blocker's `Merged` is read
  from its PR marker when it is itself a PR, else best-effort via its referenced
  PRs.
- **PR discovery is best-effort.** Forgejo's timeline schema is not guaranteed
  stable; a cross-referencing PR appears as `ref_issue` with a non-null
  `pull_request` marker. `findPRNumber` keeps the most recent match. (A future
  hardening could fall back to listing open PRs and matching "Closes #N".)

## Phase 2 — GitLab (planned, not in this PR)

Package `src/internal/source/gitlab/`. Notable differences to handle: project-id
resolution (numeric id or URL-encoded path), notes instead of comments,
`state_event` for transitions, `related_merge_requests` for issue→MR (cleaner
than timeline scraping), pipeline + job aggregation for CI, `has_conflicts` for
conflict, and issue links for blockers — with a label-convention fallback when
the link-type `blocks` relation is Premium-gated (log the downgrade, never fail).

## Phase 3 — Agent credentials (planned, not in this PR)

`agentIdentityEnv` in `src/internal/daemon/workflow.go` injects only
`GITHUB_TOKEN`/`GH_TOKEN`. Generalize to emit the per-forge token env
(`CODEBERG_TOKEN`, `GITLAB_TOKEN`/`CI_JOB_TOKEN`) and document the CLI/git-remote
auth pattern per forge in agent souls. This is what makes agents actually open
PRs/MRs against non-GitHub forges, versus Apiary merely reading their issues.

## Testing

Phase 1 ships table + `httptest` unit tests mirroring the github suite: field
mapping, filters, PR skip on poll, label resolve-to-id (add + create), remove by
id with skip-missing, CI conflict short-circuit, CI status aggregation, blocker
state normalization, and a capability assertion that `codeberg` does **not**
implement `SubIssueCreator`. Live E2E against a real Codeberg repo is Phase 4
(needs an operator token).
