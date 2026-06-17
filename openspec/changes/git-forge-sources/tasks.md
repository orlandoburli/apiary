# Tasks: Git Forge Sources

## Phase 1 — Codeberg / Forgejo source (this PR)

- [x] 1.1 `codeberg` package: `types.go` (Forgejo issue/PR/label/status/timeline DTOs)
- [x] 1.2 `client.go`: `Authorization: token` auth, per-agent token override, Link-header pagination, `base_url` for self-hosted hosts
- [x] 1.3 `adapter.go`: core `Adapter` (Connect/Poll/Acknowledge/WriteResult/WebhookHandler), `type=issues` poll + PR skip
- [x] 1.4 `StateSetter`, `LabelAdder`, `LabelRemover` — labels resolved/created by id (mutex-guarded cache)
- [x] 1.5 `TaskPoller` — issue + comments for approval steps
- [x] 1.6 `CIStatusPoller` — commit-status aggregation; `mergeable == false` → conflict
- [x] 1.7 `PullRequestLister` + `BlockerLister` — timeline PR discovery; native `/dependencies` blockers
- [x] 1.8 Register factory + blank-import in `cmd/apiary/main.go`
- [x] 1.9 Unit tests (httptest) mirroring the github suite + capability assertions
- [x] 1.10 Schema enum (`github`/`codeberg`/`jira`/`plane`), example config, `docs/codeberg-source.md`, integrations matrix, mkdocs nav
- [x] 1.11 `go vet` + `gofmt` clean; full `go build ./...` and package tests green

## Phase 2 — GitLab source (follow-up PR)

- [ ] 2.1 `gitlab` package skeleton (client v4 API, `PRIVATE-TOKEN` auth, project-id resolution)
- [ ] 2.2 Poll issues, notes (comments), labels, `state_event` transitions
- [ ] 2.3 `PullRequestLister` via `related_merge_requests`; `CIStatusPoller` via pipelines + jobs
- [ ] 2.4 Conflict via MR `has_conflicts`; `BlockerLister` via issue links with label-convention fallback (Premium gate)
- [ ] 2.5 Register, schema enum, example config, `docs/gitlab-source.md`, matrix, nav
- [ ] 2.6 Unit tests

## Phase 3 — Agent credential generalization (follow-up PR)

- [ ] 3.1 Make `agentIdentityEnv` source-type-aware (per-forge token env)
- [ ] 3.2 Document per-forge CLI / git-remote auth in agent souls and docs

## Phase 4 — Live E2E (follow-up)

- [ ] 4.1 Codeberg E2E against a real repo (operator token)
- [ ] 4.2 GitLab E2E against a real project (operator token)
