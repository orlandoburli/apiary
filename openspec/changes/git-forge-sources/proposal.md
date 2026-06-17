# Proposal: Git Forge Sources (Codeberg, GitLab)

## Why

Apiary's source layer already supports three backends (GitHub, Jira, Plane)
through a clean plugin abstraction: a `source.Adapter` interface plus opt-in
capability interfaces (`CIStatusPoller`, `BlockerLister`, `PullRequestLister`,
`SubIssueCreator`, `StateSetter`, `LabelAdder`/`Remover`, `TaskPoller`). The
actual git operations (clone, branch, commit, push, open PR) are **already
host-agnostic** — they run inside the agent subprocess, not in Apiary core.

But the only *git forge* Apiary can drive end-to-end is **GitHub**. Teams on
**Codeberg/Forgejo** (and self-hosted Gitea) or **GitLab** cannot use Apiary to
triage issues, wait on CI, gate on blockers, or write results back — even though
those platforms expose the same primitives (issues, comments, labels, states,
pull/merge requests, CI status, issue dependencies).

This change adds Codeberg/Forgejo and GitLab as first-class sources, proving the
forge abstraction generalizes beyond GitHub and unlocking Apiary for the large
population of teams not on github.com.

## What Changes

| Area | Before | After |
|---|---|---|
| **Forges supported** | GitHub only | GitHub + Codeberg/Forgejo/Gitea + GitLab |
| **`source.type` values** | `github`, `jira`, `plane` | + `codeberg`, `gitlab` |
| **CI waits** | GitHub check-runs + commit status | + Forgejo commit statuses; + GitLab pipelines |
| **Blockers** | GitHub `blocked_by`, Jira/Plane links | + Forgejo native dependencies; + GitLab issue links |
| **Agent credentials** | `GITHUB_TOKEN`/`GH_TOKEN` only | source-type-aware token/CLI env (Phase 3) |
| **Schema/docs** | github/plane (jira undocumented in schema) | enum + per-source docs for all forges |

## Scope & Phasing

The work ships incrementally; each phase is independently mergeable.

- **Phase 1 — Codeberg/Forgejo source (this PR).** New `codeberg` adapter:
  poll, acknowledge, write-back, state, labels, approval polling, CI waits
  (commit statuses), conflict detection (`mergeable` flag), and native
  dependency blockers. No sub-issues (Forgejo has none). Works against any
  Forgejo/Gitea host via `base_url`. Schema enum, example config, and docs.
- **Phase 2 — GitLab source.** New `gitlab` adapter: issues, notes, labels,
  state events, merge requests via `related_merge_requests`, pipeline CI waits,
  conflict detection (`has_conflicts`), and issue-link blockers (with a graceful
  fallback when link-type relations require GitLab Premium).
- **Phase 3 — Agent credential generalization.** Make the runner's identity env
  source-type-aware so agents receive the right token env var and the right CLI
  (`glab`/`tea`/raw git) per forge, not just `GITHUB_TOKEN`.
- **Phase 4 — Live E2E.** Validate each adapter against a real Codeberg repo and
  a real GitLab project (needs operator tokens).

## New Concepts

| Term | Description |
|---|---|
| **Forge** | A git hosting platform exposing issues + pull/merge requests + CI: GitHub, Codeberg/Forgejo, GitLab. |
| **`codeberg` source** | A `source.Adapter` for Codeberg and any Forgejo/Gitea instance (`base_url` selects the host). |
| **`gitlab` source** | A `source.Adapter` for gitlab.com and self-managed GitLab (Phase 2). |

## Out of Scope

- Bitbucket, Gitea-incompatible forks, and other forges.
- Sub-issue materialization on forges without a native parent/child API
  (Forgejo): spawned tasks still dispatch by label; they are just not linked as
  sub-issues.
- Changing the workflow engine, which already calls the capability interfaces
  generically — no engine change is needed for new forges.
