# Proposal: Apiary Agent Configuration Redesign

## Why

Apiary currently conflates **agent definitions** (who, capabilities, models) with **routing logic** (which tasks they work on). This creates rigid coupling: updating an agent's skills or preferred models requires modifying routes, and agents cannot be reused across multiple task categories. This change decouples agents from routing, enabling flexible task assignment, reusable agent definitions, and centralized agent expertise guidelines via "soul files."

## What Changes

- **New `agents:` top-level config** — Agents are now explicitly defined with ID, description, preferred models, skills, and soul file reference
- **Routes reference agents, not workers** — Routes specify `agent: <agent-id>` instead of `worker: <worker-id>`, enabling one agent to handle multiple task types
- **Soul files** — Each agent can reference a markdown file (e.g., `agents/souls/frontend.md`) containing personality, constraints, and expertise guidelines, injected into the agent's system prompt at dispatch
- **Preferred models list** — Agents specify models in preference order (e.g., `[sonnet, haiku]`); the first model is used for execution
- **Skills metadata** — Agents declare named skills (e.g., `web-componentes-ui`, `backend-api`) for visibility and future routing enhancements

## Capabilities

### New Capabilities

- `agent-definition`: Define agents with preferred models, skills, and soul files; decouple agent lifecycle from routing
- `agent-routing`: Route tasks to agents by ID; enable flexible assignment and multi-route agent reuse
- `agent-soul-files`: Load and inject agent personality/expertise guidelines from external markdown files into system prompts

### Modified Capabilities

- `route-matching`: Route configuration now references `agent` instead of `worker`; route structure remains stable
- `configuration-validation`: Config validation now handles agent definitions, soul file paths, and agent ID references in routes

## Impact

**Code:**
- `src/internal/config/config.go` — Add `AgentConfig` struct; update `Config` to include `Agents` array; update `RouteConfig`
- `src/internal/config/validate.go` — Add agent validation (ID uniqueness, soul file existence, agent reference checks)
- `src/internal/daemon/dispatcher.go` — Instantiate runners from agents; read and inject soul files; pass agent metadata to runner
- `src/internal/router/router.go` — Update route matching to accept agent references

**User experience:**
- New `agents:` section in `apiary.yaml` for explicit agent definitions
- New `.apiary/` directory for extra configs: soul files, skills definitions, etc.
  - Soul files stored at `.apiary/souls/frontend.md`, `.apiary/souls/backend.md`, etc.
  - Future: other configs (skills, templates) also under `.apiary/`
