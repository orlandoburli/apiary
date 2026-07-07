# Proposal: Credit-Aware Runner Fallback

## Why

The current fallback mechanism triggers only on explicit `rate_limit_event` JSON lines with `status: "rejected"` (e.g., Claude's 5-hour session limit). When the **primary runner exhausts its usage credits** — a distinct failure mode that's not a rate limit — the fallback does not activate:

- **Codex CLI out of credits**: The CLI runs but produces no useful output (or exits with an error unrelated to rate limits). The dispatcher records it as a success or generic failure, the fallback chain is never consulted, and the pipeline appears to succeed with empty/meaningless output.
- **Cursor overage**: The Cursor CLI continues past the subscription cap but output degrades or the provider silently reduces quality — the runner exits `0` and the pipeline continues with degraded results.
- **Claude API 429 "insufficient credits"**: The Anthropic API returns HTTP 429 with a different error shape that does not match the `rate_limit_event` pattern. The CLI runner sees a non-zero exit and records a generic failure — no fallback.

The gap is that **"can't do productive work"** is currently detected only through one narrow signal. We need a general credit/budget-awareness layer that:

1. **Detects credit exhaustion** across providers (not just rate-limit events)
2. **Pre-emptively switches runners** when budgets are projected to be exhausted, rather than reacting after a failed run
3. **Supports multiple fallback strategies** — ordered chain, least-cost routing, fastest-first
4. **Tracks running costs** per runner so that budget caps work across process restarts

---

## What Changes

| Area | Before | After |
|---|---|---|
| **Failure detection** | Single `rate_limit_event` JSON pattern in CLI stdout | Pluggable `FailureDetector` interface; detectors registered per runner type |
| **Fallback trigger** | `RateLimited` only | `RateLimited` OR `CreditExhausted` OR `GenericAborted` (heuristic) |
| **Runner pause** | Keyed by runner adapter type; rate-limit only | Same pause mechanism extended with budget-based pause + configurable cooldown |
| **Budget tracking** | None (Cursor cost back-fill is read-only, post-hoc) | Optional `runners[].budget` with monthly/weekly/daily caps; proactive check before dispatch |
| **Fallback scope** | Per-agent only | Per-agent + global `default_fallbacks` + optional per-workflow `fallback_strategy` |
| **Fallback policy** | Hard-coded ordered chain | `ordered` (default) + `random` + `least_cost` + `fastest` |
| **State continuity** | Failover discards partial primary output | Optionally injects primary's partial output/artifacts into fallback's system prompt |

---

## New Concepts

| Concept | Description |
|---|---|
| **FailureDetector** | A provider-specific function (or regex/hook) registered per runner type that inspects CLI output (stdout, stderr, exit code, duration) and returns a structured failure classification: rate-limited, credit-exhausted, or aborted. |
| **CreditExhausted** | A new `RunResult` field, orthogonal to `RateLimited`. Signals that the runner ran but consumed credits with no useful output — or was rejected due to zero balance. Triggers fallback immediately. |
| **RunnerBudget** | Optional per-runner budget config: `monthly_cost_cap`, `monthly_token_cap`, `reset_period`. Tracked in a new DB table (`runner_budget_ledger`) with per-execution debits. |
| **Proactive budget check** | Before dispatching to a primary runner, the executor checks whether the runner's budget is exhausted. If so, it skips directly to the first non-exhausted fallback — without wasting a run. |
| **FallbackStrategy** | Policy enum on `agents[].fallback_strategy` or `workflows[].fallback_strategy`: `ordered` (default), `random`, `least_cost`, `fastest`. `least_cost` and `fastest` require historical usage data (available from `task_executions`). |
| **Global fallbacks** | `settings.default_fallbacks` — a fallback chain applied to any agent that doesn't specify its own `fallbacks[]`. Resolves before the agent's own chain. |

---

## Design

### 1. Failure Detection — Interface and Built-in Detectors

```go
// FailureKind classifies why a runner invocation failed to produce useful work.
type FailureKind int

const (
    FailureNone          FailureKind = iota // no failure; output is usable
    FailureRateLimited                      // provider rate limit (e.g. 5h session)
    FailureCreditExhausted                  // out of credits / over quota
    FailureAborted                          // runner exited early with no useful output (heuristic)
)

// FailureDetector inspects a completed run and classifies it.
type FailureDetector interface {
    // Detect returns the failure kind and an optional reset time.
    // Called after Run() returns, with access to the full output.
    Detect(req RunRequest, result *RunResult) (kind FailureKind, resetsAt time.Time)
}
```

**Built-in detectors registered by runner type:**

| Runner type | Detector | Heuristic |
|---|---|---|
| `codex-cli` | `codexFailureDetector` | Scans stderr for "out of credits", "credit limit", "insufficient credits", "usage limit exceeded"; checks exit code 1 with empty stdout; checks for `rate_limit_event` → `FailureRateLimited` |
| `claude` | `claudeFailureDetector` | Existing `detectRateLimitRejection` → `FailureRateLimited`; 429 HTTP status in stderr → `FailureRateLimited`; billing-related messages → `FailureCreditExhausted` |
| `cursor-cli` | `cursorFailureDetector` | Checks for degraded mode signals; "overage", "limit reached" in output; Cursor API 402/429 |
| `opencode-cli` | `opencodeFailureDetector` | Scans for credit/billing errors in stderr; `rate_limit_event` passthrough |
| `*` (generic) | `genericFailureDetector` | Fallback: if `ExitCode != 0` AND `Output` is empty or contains only error/limit messages AND `IsOOM / IsTimeout` → `FailureAborted` |

**Plug-and-play architecture**: Runner adapters register their detector at init time alongside the factory. The generic detector runs as default for any runner without a specific registration.

### 2. RunResult — New Fields

```go
type RunResult struct {
    // ... existing fields ...

    RateLimited      bool      // existing — provider rate limit
    RateLimitResetsAt time.Time // existing

    CreditExhausted  bool      // NEW — out of credits / budget exhausted
    // FailureKind is the canonical classification set by the FailureDetector.
    // RateLimited and CreditExhausted are convenience booleans derived from it.
    FailureKind      FailureKind `json:"-"` // NEW — not serialized; used by dispatcher
}
```

The `FailureDetector.Detect()` is called inside `CliRunner.Run()` after the process exits, alongside the existing `applyStructured()` call. The result's `RateLimited`, `CreditExhausted`, and `FailureKind` are set accordingly.

### 3. Fallback Selection — Extended ExecuteStep Logic

Current flow (simplified):

```
for each candidate:
    if paused: skip
    run candidate
    if rate-limited AND not last: pause type, continue
    break
```

New flow:

```
for each candidate:
    if paused (rate-limit OR budget): skip
    if budget-exhausted (proactive): skip  ← NEW
    run candidate
    apply failure detectors
    if Failed (any kind) AND not last:
        pause type with appropriate cooldown  ← NEW: cooldown varies by kind
        continue
    break
```

**Cooldown durations by failure kind:**

| Kind | Default cooldown | Rationale |
|---|---|---|
| `FailureRateLimited` | Provider-reported reset, or 5m default | Same as today |
| `FailureCreditExhausted` | 24h (or until explicit top-up signal) | Credits don't reset every 5 minutes |
| `FailureAborted` | 0 (retry immediately on next candidate) | Could be transient; let the fallback try |

The cooldown for `CreditExhausted` is adjustable via `settings.credit_exhausted_cooldown: "24h"`.

### 4. Budget Tracking — Proactive Cap Enforcement

**Config:**

```yaml
runners:
  - id: codex
    type: cli
    provider: codex
    models: [gpt-5.5]
    budget:                          # NEW — optional
      monthly_cost_cap: 50           # $50/month
      monthly_token_cap: 10_000_000  # 10M tokens/month
      reset_period: monthly          # monthly | weekly | daily | "1st of month"
```

**DB table:**

```sql
CREATE TABLE runner_budget_ledger (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    runner_id     TEXT NOT NULL,          -- runner config id ("codex")
    period_start  TEXT NOT NULL,          -- ISO 8601 start of budget period
    period_end    TEXT NOT NULL,          -- ISO 8601 end of budget period
    total_cost    REAL NOT NULL DEFAULT 0,
    total_tokens  INTEGER NOT NULL DEFAULT 0,
    updated_at    TEXT NOT NULL,
    UNIQUE(runner_id, period_start)
);
```

**Flow:**

1. Each time a `task_executions` row is created (in `finishExecution`), the daemon debits the runner's budget ledger for the current period.
2. Before dispatching to a candidate (in `ExecuteStep`), the executor checks `runnerBudgetExhausted(runnerID)`:
   - Looks up the current budget period's running totals from the DB or in-memory cache
   - If `total_cost >= monthly_cost_cap` or `total_tokens >= monthly_token_cap`, returns true
   - The candidate is skipped as if paused
3. In-memory cache of budget state is refreshed every 60s and updated on each execution write-back, so the hot path avoids a DB read per candidate.

**Cache invalidation**: The in-memory cache is a simple `map[string]*BudgetSnapshot` with a `lastRefreshed` timestamp. If >60s stale, the next check triggers a DB reload. Writes (from `finishExecution`) update both DB and cache atomically.

### 5. Global Fallbacks + Policy

**New config fields:**

```yaml
settings:
  default_fallbacks:                   # NEW — applies to all agents
    - {runner: opencode-go, model: opencode-go/deepseek-v4-pro}
    - {runner: cursor, model: composer-2.5-fast}
  credit_exhausted_cooldown: "24h"     # NEW — default cooldown for credit exhaustion

agents:
  - id: engineer
    fallbacks:                         # per-agent overrides global
      - {runner: opencode-go, model: opencode-go/deepseek-v4-pro}
    fallback_strategy: ordered         # NEW — ordered | random | least_cost | fastest

workflows:
  - id: code-review
    fallback_strategy: fastest         # NEW — per-workflow override
```

**Strategy implementations:**

| Strategy | Behavior | Data source |
|---|---|---|
| `ordered` | Current chain order; tries candidate[0], then candidate[1], etc. | Config order |
| `random` | Shuffles the candidate chain before each dispatch | — |
| `least_cost` | Sorts candidates by historical avg cost per run (ascending) | `task_executions` cost history |
| `fastest` | Sorts candidates by historical avg duration per run (ascending) | `task_executions` duration history |

`least_cost` and `fastest` queries:

```sql
SELECT c.runner_type, AVG(e.cost_usd) AS avg_cost, AVG(e.duration_ms) AS avg_dur
FROM task_executions e
WHERE e.created_at > datetime('now', '-30 days')
  AND e.rate_limited = 0
  AND e.credit_exhausted = 0
  AND e.success = 1
GROUP BY e.runner_type
ORDER BY <avg_cost|avg_dur> ASC
```

If historical data is insufficient (fewer than 3 data points), fall back to `ordered`.

**Resolution order** (lowest number wins):

1. `workflows[].fallback_strategy` (per-workflow)
2. `agents[].fallback_strategy` (per-agent)
3. `ordered` (default)

### 6. State Continuity on Mid-Workflow Fallback

When the primary runner fails mid-step (credit-exhausted after partial work):

- **v1 (with this change):** The fallback runner starts with a fresh prompt. Any files the primary created/modified on disk persist and are visible to the fallback. The primary's partial output is **not** injected into the fallback's context. This is the same behavior as today's rate-limit failover.

- **v2 (future enhancement):** The primary runner's partial output (captured up to the failure point) is appended to the fallback's `SystemPrepend` as `[Previous partial output from <runner>]\n<output>`. This requires distinguishing "partial but useful" output from "empty/error" output — which the `FailureDetector` already classifies.

**Decision**: v1 uses the same semantics as rate-limit failover (no continuity). Document the v2 path.

### 7. Config Validation — New Lint Rules

| Rule | Check |
|---|---|
| `budget` fields validity | `monthly_cost_cap` must be positive float; `reset_period` must be one of `monthly`, `weekly`, `daily`, or a cron expression |
| `fallback_strategy` validity | Must be one of `ordered`, `random`, `least_cost`, `fastest` |
| `default_fallbacks` validity | Same as per-agent fallbacks (must reference defined runner IDs) |
| `credit_exhausted_cooldown` | Must be a valid duration string if set |

---

## What Stays

- **Runner interface** (`runner.Runner`) — unchanged; `Run()` still returns `(RunResult, error)`. The new `FailureKind` is set by the executor after calling `Run()`.
- **Dispatcher construction** — `agentFallbacks` map is still pre-built at startup. Budget data is loaded lazily.
- **Rate-limit pause mechanism** — extended but not replaced. `pauseRunner` and `runnerPausedUntil` remain the concurrency guard.
- **Usage summing across failover attempts** — the existing `summedUsage` accumulation in `ExecuteStep` is unchanged.
- **task_executions schema** — unchanged except new columns (see below).
- **Dashboard** — no UI changes; the new fields (CreditExhausted, FailureKind) are stored but not surfaced in v1.

---

## DB Schema Changes

**New columns on `task_executions`:**

```sql
ALTER TABLE task_executions ADD COLUMN credit_exhausted INTEGER NOT NULL DEFAULT 0;
ALTER TABLE task_executions ADD COLUMN failure_kind TEXT; -- "none" | "rate_limited" | "credit_exhausted" | "aborted"
```

**New table for budget tracking:**

```sql
-- See §4 above for CREATE TABLE runner_budget_ledger
```

---

## Implementation Plan

### Phase 1 — Detection & Extended Fallback (core)

1. Add `FailureKind` + `CreditExhausted` to `model.RunResult`
2. Define `FailureDetector` interface in `runner/execution/`
3. Implement `genericFailureDetector` (exit code + output heuristics)
4. Implement `codexFailureDetector` (credit-exhausted patterns in stderr)
5. Wire `FailureDetector.Detect()` into `CliRunner.Run()` after process exit
6. Extend `ExecuteStep` to handle `FailureCreditExhausted` and `FailureAborted` with appropriate cooldowns
7. Add `settings.credit_exhausted_cooldown` config field
8. Add `default_fallbacks` to settings, with merge logic in dispatcher construction
9. Update config validation

### Phase 2 — Fallback Strategy Engine

10. Add `fallback_strategy` to agent and workflow config
11. Implement `ordered`, `random`, `least_cost`, `fastest` reordering in `ExecuteStep`
12. Add DB queries for historical cost/duration stats
13. Cache strategy results with TTL

### Phase 3 — Budget Tracking (proactive)

14. Add `runners[].budget` config
15. Create `runner_budget_ledger` table
16. Implement budget debit on `finishExecution`
17. Implement `runnerBudgetExhausted()` check in `ExecuteStep`
18. Add in-memory budget cache with 60s refresh
19. Add `apiary budget` CLI command to view/reset budget state

### Phase 4 — State Continuity (v2, deferred)

20. Capture primary's partial output on failure
21. Inject into fallback's `SystemPrepend`
22. Add `continuity: on | off` step flag

---

## Out of Scope

- **Secret management / vault integration** — budget tokens and runner API keys continue to flow through `.env`
- **Real-time budget alerts** (email, Slack) — deferred to a future observability change
- **Automatic top-up** — paying to restore credits is outside APIary's scope
- **Provider-specific credit APIs** — querying Codex/Cursor/Anthropic billing APIs for live balances (the cost/token caps are declarative, configured by the operator)
- **Dashboard UI for budgets** — the `apiary budget` CLI command is sufficient for v1
- **ML-based fallback selection** — `least_cost` and `fastest` are metrics-based, not predictive

---

## Migration

Since all new fields are optional with sensible defaults, existing configs continue to work unchanged:

| Existing config | Behavior after change |
|---|---|
| No `fallbacks[]` | No fallback, as before |
| `fallbacks[]` only | Rate-limit failover works; credit exhaustion still not caught (fixed when the operator adds a `codexFailureDetector` or uses the generic detector) |
| No `budget` | No budget tracking |
| No `default_fallbacks` | Agents without `fallbacks[]` have none |
| No `fallback_strategy` | `ordered` (same as today) |
| No `credit_exhausted_cooldown` | Defaults to 24h |

To activate credit-awareness for Codex, an operator must:

1. Ensure the generic (or codex-specific) failure detector catches the "out of credits" signal
2. Optionally configure a `budget` block on the Codex runner for proactive protection
3. Set `fallbacks[]` on agents (or `default_fallbacks` globally) pointing to a viable secondary runner
