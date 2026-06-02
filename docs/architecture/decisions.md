# Architecture Decision Records

## ADR-001: Go as the runtime language

**Status:** Accepted

**Context:** Apiary needs to be distributed as a single binary for macOS and Linux, easy to install without a language runtime, and capable of shelling out to CLI tools reliably.

**Decision:** Go. Compiles to a static binary, excellent subprocess/process management, strong concurrency model for polling + parallel runs, good CLI tooling ecosystem (cobra, viper).

**Alternatives considered:** Rust (steeper learning curve for OSS contributors), Node.js/TypeScript (requires runtime, heavier binary), Python (same runtime issue).

---

## ADR-002: Poll-first, webhook-optional

**Status:** Accepted

**Context:** Webhook setup requires infrastructure (public endpoint, secret configuration) which creates friction for local/dev use. Polling is simpler to set up but less real-time.

**Decision:** Polling is the default and always works. Webhook support is additive — sources that support it can register an HTTP handler, and users can optionally expose it. Apiary includes a webhook server that activates only when at least one source configures a webhook.

---

## ADR-003: Priority-ordered routing, first-match-wins

**Status:** Accepted

**Context:** Tasks may match multiple routes (e.g., a task labelled both `frontend` and `bug`). A deterministic, user-controlled tie-break is needed.

**Decision:** Routes have an explicit `priority` integer (lower = higher priority). The first matching route wins. Users control the order explicitly, which is predictable and auditable. No implicit scoring or machine-learning-based routing in v1.

---

## ADR-004: Runners are thin wrappers around CLI tools

**Status:** Accepted

**Context:** Claude Code and OpenCode are CLI tools. We could embed their SDK/API directly, but that couples Apiary to specific SDK versions and increases binary size.

**Decision:** Runner adapters shell out to CLI binaries. This makes Apiary independent of SDK updates, allows users to control their agent version, and keeps the adapter code simple. The runner adapter is responsible for building the CLI invocation (flags, env vars) and streaming stdout/stderr.

**Trade-off:** Users must have the CLI tool installed and on PATH. Apiary will detect and report missing binaries at startup.

---

## ADR-005: SQLite for run history (v0.3+)

**Status:** Proposed

**Context:** Run history needs to persist across restarts. An external database adds an operational dependency that's too heavy for a local-first tool.

**Decision:** Use an embedded SQLite database (via `modernc.org/sqlite`, pure Go, no CGO). History is stored at `~/.apiary/runs.db` by default, configurable via `APIARY_DB_PATH`.
