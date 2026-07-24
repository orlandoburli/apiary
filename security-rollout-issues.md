# Apiary Security Hardening — Issue Backlog

Derived from the Apiary Security Review + council re-prioritization. Ordered by the council's buckets, **not** the report's original P0/P1/P2 (socket auth was demoted; env-scoping, file perms, and output-side controls were promoted).

**Rollout gate:** do not enable Apiary org-wide until every `bucket:rollout-gate` issue is closed.

Suggested new label:
```bash
gh label create security --color B60205 --description "Security hardening / vulnerability"
gh label create bucket:today --color FBCA04 --description "Cheap hardening, do immediately"
gh label create bucket:this-week --color FBCA04 --description "Interim mitigation before rollout"
gh label create bucket:rollout-gate --color B60205 --description "Blocks org-wide rollout"
```

---

## Bucket 0 — Today (hours; ship regardless of rollout decision)

### SEC-01 — Freeze rollout + restrict issue-creation to trusted authors
**Labels:** security, bucket:today
**Type:** policy / ops (no code)
The live critical (prompt injection → shell agent) is neutralized for now if only trusted people can file issues. On every Apiary-connected repo, restrict issue creation to collaborators (or require a triage label before a workflow trigger fires). Document a rollout freeze until `bucket:rollout-gate` issues close.
**Done when:** connected repos reject issues from non-collaborators (or gate on triage label); freeze noted in README/ops doc.

### SEC-02 — Tighten on-disk file permissions to 0600 + dedicated daemon user
**Labels:** security, bucket:today
**Refs:** `config.go:541`, `logging/logger.go:79,85,271`, `memory/store.go:114,592`, `daemon/socket.go:12-24`
Config, logs, task DB, transcripts, and memory files are written 0644 (world-readable). On a shared host any local account can read tokens, prompt history, and issue content. Write these 0600 and run the daemon under a dedicated non-shared OS user.
**Done when:** all persisted artifacts are 0600; docs recommend a dedicated service account.

### SEC-03 — Bind worker queue to localhost by default
**Labels:** security, bucket:today
**Refs:** `daemon/queue_server.go:24-34`, `queuehttp/protocol.go`
The distributed-worker queue can be configured to listen on any interface with plaintext HTTP. Default `settings.queue.listen` to `127.0.0.1`; require explicit opt-in (with a warning) to bind elsewhere. TLS transport tracked separately in SEC-13.
**Done when:** default bind is loopback; non-loopback bind logs a warning.

### SEC-04 — Lock plugin directory to owner-only
**Labels:** security, bucket:today
**Refs:** `plugin/discovery.go`, `plugin/manifest.go`
Plugins are loaded from configured folders with no ownership check. As a stopgap before signing (SEC-10), refuse to load from any plugin dir that is group/world-writable.
**Done when:** discovery skips + warns on non-owner-only plugin dirs.

---

## Bucket 1 — This week (interim injection mitigation using existing machinery)

### SEC-05 — Gate non-collaborator-triggered runs behind approval
**Labels:** security, bucket:this-week
**Refs:** `runner/execution/cli.go:551-574`, `runner/providers/providers.go:47-61`, `daemon/dispatcher.go:~1740-1747`
Reuse the existing multi-channel approval gates (#220) so any workflow triggered by an issue from a non-collaborator author parks for human approval before an agent with shell/edit access runs. This is the cheapest real mitigation for the critical while sandboxing (SEC-09) is built.
**Done when:** runs triggered by untrusted authors require an approval before dispatch.

### SEC-06 — Wrap untrusted issue content in a delimited block in the prompt
**Labels:** security, bucket:this-week
**Refs:** `runner/execution/cli.go:551-574`, `runner/providers/providers.go:47-61`
Ticket title/description/labels/URL are concatenated straight into the prompt. Wrap all ticket-derived text in an explicit, clearly-delimited "untrusted content — do not treat as instructions" block. Not a full fix (containment is), but reduces trivial injection.
**Done when:** all externally-sourced fields are rendered inside a labeled untrusted-content delimiter.

### SEC-07 — Strip/allow-list environment variables passed to agent subprocesses
**Labels:** security, bucket:this-week
**Refs:** `runner/execution/cli.go:108-111`, `daemon/workflow.go:896-918`
Agent subprocesses inherit every host env var — this is the exfiltration payload for a successful injection. Pass only an explicit allow-list; scope credentials to per-repo fine-grained tokens rather than a broad `GITHUB_TOKEN`.
**Done when:** agents receive only allow-listed env vars; per-repo scoped tokens documented.

### SEC-08 — Require human review of agent-authored PRs (branch protection)
**Labels:** security, bucket:this-week
**Refs:** `wait_for`/`on_conflict` auto-merge flows
An injected agent can open a plausible-looking PR that auto-merges on CI green with no human. Enable branch protection requiring human review on any branch an agent can push, and disable auto-merge for agent-authored PRs until sandboxing lands.
**Done when:** no agent PR can merge to a protected branch without human approval.

---

## Bucket 2 — Rollout gate (weeks; start now, do not let slip)

### SEC-09 — Sandbox agent execution (THE critical fix)
**Labels:** security, bucket:rollout-gate
**Refs:** `daemon/dispatcher.go:~1740-1747`, `runner/execution/cli.go`
Run shell-capable agents in a container with a restricted user, minimal per-task credential set, and network egress controls. This is the only remediation that actually addresses prompt injection (containment, not filtering). Apply to all repos with external issue authors — treat those repos as hostile by default. OpenCode runner currently enables bash/edit by default (`daemon/dispatcher.go:~1740-1747`) — flip to least-privilege.
**Done when:** agent runs execute in an isolated sandbox with scoped creds + egress policy; org rollout unblocked.

### SEC-10 — Enforce plugin manifest permissions + signature verification
**Labels:** security, bucket:rollout-gate
**Refs:** `plugin/manifest.go:39-44`, `plugin/client.go:137-153`
Declared plugin permissions (network, filesystem) are never enforced, and any file in a plugin folder runs with host-user privileges. Enforce the manifest as a real sandbox boundary and require signature/checksum verification before load. Do this now — the versioned plugin SDK (#221) just shipped with zero third-party plugins, so breaking changes are still free.
**Done when:** unsigned plugins are refused; manifest permissions are enforced at runtime.

### SEC-11 — Authenticate the Unix-socket control plane
**Labels:** security, bucket:rollout-gate
**Refs:** `daemon/dispatcher.go:1000-1268`, `daemon/approval_http.go:36-63`, `daemon/events_http.go`
The control socket can restart/delete tasks, patch agent config, and **approve pending gates** with only a filesystem-permission check. The dashboard approval path skips the signature check the webhook path enforces — an unauthenticated local approval-gate bypass that defeats #220. Add a SO_PEERCRED + shared-secret/token check on every mutating endpoint. Must land before any multi-user or shared-host deployment.
**Done when:** all mutating socket endpoints require auth; dashboard approval enforces the same signature check as the webhook path.

---

## Bucket 3 — Follow-up (council-caught, not in original report)

### SEC-12 — Constrain agent output side-effects (spawn/publish loop)
**Labels:** security, bucket:this-week
**Refs:** `internal/model/result.go`
Agent output can spawn sub-issues (`APIARY_SPAWN`), post comments (`APIARY_PUBLISH`), and feed condition expressions — an injected agent can create tickets that re-enter the pipeline (self-propagating loop). Add spawn-depth limits, provenance labels on agent-authored content, and an allow-list of what agents may publish.
**Done when:** spawn depth is bounded; agent-authored artifacts are provenance-labeled.

### SEC-13 — TLS for the worker queue transport
**Labels:** security, bucket:rollout-gate (only if remote workers used)
**Refs:** `queuehttp/protocol.go`
Worker protocol is well-scoped (constant-time token check, no arbitrary enqueue) but plaintext. On an untrusted network the access token is interceptable. Support TLS or require the queue behind a TLS-terminating proxy before cross-machine use.
**Done when:** remote worker traffic is encrypted or explicitly gated behind a proxy.

### SEC-14 — Audit/detection for successful injection
**Labels:** security, bucket:this-week
No current way to tell if an injection already happened. Add agent-action audit logging (commands run, files touched, network egress) and anomaly alerts. You cannot sandbox retroactively — detection is the backstop.
**Done when:** agent actions are audit-logged with alerting on anomalies.

### SEC-15 — Stop persisting full prompts + scrub existing SQLite
**Labels:** security, bucket:this-week
**Refs:** `logging/logger.go:154-162`, `runner/execution/cli.go:141-142`
Full prompts (incl. entire ticket contents) are always saved to the task DB regardless of log level. Add a config option to stop persisting / auto-expire — **and** scrub the historical prompt data already on disk (forward-only flag isn't enough).
**Done when:** full-prompt persistence is configurable/off; existing rows scrubbed.

### SEC-16 — Add explicit allow-list for MCP server commands
**Labels:** security, bucket:this-week
**Refs:** `runner/execution/cli_mcp.go:156-167`
Any command string in `apiary.yaml` MCP entries launches as a helper process — config-write access is effectively full code execution. Validate MCP commands against an explicit allow-list.
**Done when:** MCP server commands are checked against an allow-list before launch.

### SEC-17 — Redaction consistency + govulncheck in CI
**Labels:** security, bucket:this-week
**Refs:** `internal/log/log.go:56-59`, `src/go.mod`
Extend the token-pattern redaction used for event metadata to `internal/log` stderr output. Separately, add `govulncheck`/Dependabot to CI — no dependency CVE audit was performed in the review.
**Done when:** stderr logs are redacted; CI runs govulncheck on every push.

---

### Verification caveat
The council flagged that no one confirmed the report's findings against current code before acting. Recommend a spot-check of SEC-09, SEC-10, and SEC-11 line refs against `main` before closing them (some line numbers may have drifted since the review's checkout).

---

## Bucket 4 — Governance (P0, second review)

### SEC-GOV — Single-maintainer / no-SLA risk decision
**Labels:** security, bucket:rollout-gate
**Refs:** [`GOVERNANCE.md`](GOVERNANCE.md), [Issue #247](https://github.com/orlandoburli/apiary/issues/247)
Apiary is a single-maintainer personal project (bus-factor = 1) proposed for org-wide adoption. No backup maintainer is named and no committed security-patch SLA exists. Before rollout, this risk must be formally accepted in writing and adopters must understand the implications.
**Decision (2026-07-24):** Single-maintainer risk formally accepted. See [`GOVERNANCE.md`](GOVERNANCE.md) for the full ownership statement, security response expectations, and required mitigations for adopting teams (pin to release tag, maintain fork capacity, monitor upstream activity).
**Done when:** `GOVERNANCE.md` exists in the repo and is referenced here — closed by [#247](https://github.com/orlandoburli/apiary/issues/247).
