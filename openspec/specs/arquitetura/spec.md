# Apiary — Architecture

## Technology Choices

| Concern | Choice | Rationale |
|---|---|---|
| Runtime | Go | Single static binary, cross-platform, easy OSS distribution, excellent subprocess management |
| Config format | YAML | Familiar to DevOps users, rich tooling ecosystem |
| Plugin system | Go interfaces (v1) + gRPC plugin protocol (v2) | Built-in adapters first; external plugin API in v2 |
| Observability | Structured JSON logs + optional OTLP traces | Pipes into any stack without vendor lock-in |
| Run history | Embedded SQLite | Zero-dependency persistence for a local-first tool |
| Distribution | GitHub Releases, Homebrew, Docker | Cover macOS/Linux dev + container deployments |

## Architecture Decision Records

### ADR-001: Go as the runtime language

**Status:** Accepted

**Decision:** Go. Compiles to a static binary, no runtime required, excellent concurrency for parallel polling + runner invocations, strong subprocess management, great CLI ecosystem (cobra, viper).

**Alternatives considered:** Rust (steeper learning curve for OSS contributors), Node.js/TypeScript (requires runtime), Python (same issue + packaging overhead).

---

### ADR-002: Poll-first, webhook-optional

**Status:** Accepted

**Decision:** Polling is the default and always works without infrastructure. Webhook support is additive — sources that support it register an HTTP handler, and users can optionally expose it. The webhook server activates only when at least one source is configured for push mode.

**Rationale:** Webhook setup requires a public endpoint and secret management, creating friction for local/dev use. Polling works anywhere.

---

### ADR-003: Priority-ordered routing, first-match-wins

**Status:** Accepted

**Decision:** Routes have an explicit `priority` integer (lower = evaluated first). The first matching route wins. No implicit scoring or ML-based routing in v1.

**Rationale:** Deterministic, auditable, and fully user-controlled. Users can trace exactly why a task was dispatched to a given worker.

---

### ADR-004: Runners are thin wrappers around CLI tools

**Status:** Accepted

**Decision:** Runner adapters shell out to CLI binaries rather than embedding SDKs directly.

**Rationale:** Keeps Apiary decoupled from SDK version updates. Users control which agent version they run. Runner adapter code stays simple — just responsible for building the CLI invocation and streaming stdout/stderr.

**Trade-off:** Users must have the CLI tool installed and on PATH. Apiary detects and reports missing binaries at startup.

---

### ADR-005: SQLite for run history (v0.3+)

**Status:** Proposed

**Decision:** Embedded SQLite via `modernc.org/sqlite` (pure Go, no CGO). History stored at `~/.apiary/runs.db` by default, overridable with `APIARY_DB_PATH`.

**Rationale:** No external database dependency for a developer tool. SQLite is battle-tested and sufficient for the local run history use case.

---

### ADR-006: Provider-agnostic model identifiers

**Status:** Accepted

**Decision:** Model identifiers in `apiary.yaml` are opaque strings passed directly to the runner adapter. Apiary does not validate, normalise, or interpret them — each runner adapter is responsible for accepting the model ID format its underlying tool expects.

**Rationale:** Prevents Apiary from being coupled to any specific LLM provider's naming scheme. As providers and runners evolve, users update model IDs in their config without Apiary changes.

**Example formats runners may accept:**
```
openai/gpt-4o
deepseek/deepseek-r1
mistral/mistral-large-2411
meta/llama-3.3-70b
```
