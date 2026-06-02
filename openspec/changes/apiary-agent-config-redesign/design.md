# Design: Agent Configuration Redesign

## Context

Apiary currently defines agents implicitly through `workers:` array, each with a single runner and model. Routing then assigns tasks to workers. This design conflates two concerns:

1. **Agent definition**: Who is this worker, what capabilities (models, skills) do they have, what are their expertise guidelines?
2. **Task routing**: Which tasks should be assigned to which workers?

Current constraints:
- No ability to share expertise guidelines ("SOUL" files) across agents
- Model fallback or preference ordering not supported
- Routes are tightly coupled to specific workers
- Agents cannot be referenced by multiple routes

Target: Decouple agent definitions from routing, support preferred model lists and soul files, enable flexible routing and agent reuse.

## Goals / Non-Goals

**Goals:**

- Separate agent definitions from task routing via explicit `agents:` config section
- Support preferred model lists (e.g., `[sonnet, haiku]`); use first available at runtime
- Load and inject agent soul files (markdown with expertise/personality) into system prompts at dispatch
- Enable one agent to be used by multiple routes
- Keep soul files as external markdown files, not embedded in config

**Non-Goals:**

- Dynamic agent discovery or registration (agents are defined in config)
- Model fallback at runtime (first model must be available; enhancement for future)
- Skill-based routing (skills are metadata only; routing is still label/state/regex-based)
- Multi-tenant or per-workspace agent definitions (single global agent pool)
- Agent authentication or fine-grained permissions (out of scope)

**Directory Structure:**
- `apiary.yaml` — Main config file in root directory
- `.apiary/` — Extra configs directory (sibling to `apiary.yaml`)
  - `.apiary/souls/` — Agent soul files (personality/expertise guidelines)
  - `.apiary/skills/` — Future: skill definitions and templates
  - `.apiary/` — Other agent-related configs as needed

## Decisions

### 1. Config Structure: New `agents:` Array at Top Level

**Decision**: Add `agents: []AgentConfig` to the root `Config` struct, alongside `sources:`, `workers:`, and `routes:`.

**Rationale**: 
- Explicit, declarative, easy to reference from routes
- Consistent with existing config structure (sources, workers are also top-level)
- Clear separation of concerns: agents are distinct from routing rules

**Alternatives considered**:
- Separate `agents.yaml` file — adds file management complexity; inline is cleaner for small use cases
- Embed agents in `workers:` — defeats the purpose of decoupling; would require renaming workers to agents

### 2. Agent Struct Fields

**Decision**: `AgentConfig` has: `id`, `description`, `soul_file`, `preferred_models`, `skills`.

**Rationale**:
- `id` — Unique identifier for references (required)
- `description` — Human-readable label for logs/status
- `soul_file` — Path to markdown file with personality/expertise (optional but encouraged)
- `preferred_models` — List of models in preference order; first is active (required, non-empty)
- `skills` — Named skill references for visibility and future enhancements (optional)

**Alternatives**:
- Single `model` field instead of `preferred_models` — less flexible; fallback patterns are real use cases
- Embed soul content in config instead of file path — less modular; files are easier to version/share

### 3. Soul Files as External Markdown

**Decision**: `soul_file` is a path to a `.md` file (e.g., `agents/souls/frontend.md`). Content is read at dispatch time and appended to the runner's system prompt.

**Rationale**:
- Separates agent personality from config structure
- Markdown is readable, versionable, and easy to edit
- Late binding (read at dispatch) allows soul file changes without restarting daemon
- Modular: one soul file can be shared across multiple agent definitions (future enhancement)

**Alternatives**:
- Embed soul as multiline string in config — less modular, harder to edit and review
- Store in database — adds persistence layer; config file is simpler for this project

### 4. Routes Reference Agents by ID

**Decision**: Change `RouteConfig.Worker` → `RouteConfig.Agent` (both strings). At dispatch, route's agent ID is looked up and runner is retrieved.

**Rationale**:
- Decouple routing from worker instances
- One agent can be referenced by multiple routes
- Clear semantics: "route this task to this agent"

**Implementation detail**: Internally, dispatcher creates synthetic "pseudo-workers" for each agent (one runner per agent using its first preferred model). This keeps the runner instantiation logic minimal.

**Alternatives**:
- Keep `worker` field, map workers to agents — confusing; workers are implementation detail
- Direct route → runner mapping — loses the agent abstraction

### 5. Soul File Injection Timing

**Decision**: Read soul file at dispatch time (in `Dispatcher.dispatch()`) and append content to `RunRequest.SystemAppend` before passing to runner.

**Rationale**:
- Allows soul file changes without restarting Apiary daemon
- Late binding enables A/B testing different soul files
- Simple: file read happens once per dispatch, minimal overhead

**Alternatives**:
- Read at daemon startup — fast at dispatch time, but requires restart for soul file changes
- Pass file path to runner and let runner read it — less coupling, but runner must handle file I/O

## Risks / Trade-offs

**Risk: Soul files can grow large and blow token budgets**
→ *Mitigation*: Document soul file best practices; recommend keeping them concise and focused. Provide example templates.

**Risk: Typos in `soul_file` paths go undetected until dispatch**
→ *Mitigation*: Add validation at config load time: check that `soul_file` exists and is readable (using `os.Stat` or similar).

**Risk: Model list ordering is not enforced; user might list `[haiku, sonnet]` by mistake**
→ *Mitigation*: Document preferred models as preference order; recommend reverse-alphabetical in examples to make ordering intentional.

**Trade-off: Skills are metadata only; they don't affect routing**
→ *Rationale*: Keeps routing logic simple (still label/state/regex-based). Skills are for visibility and future enhancements like skill-based routing.

**Trade-off: First preferred model must be available at runtime; no automatic fallback**
→ *Rationale*: Simpler implementation; fallback logic can be added in a future release if needed. For now, users should ensure first model is always available.
