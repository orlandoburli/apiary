# Hermes Profile Migration to Apiary

This document explains how Hermes profiles were converted into Apiary agents.

## Overview

The Hermes system (`~/.hermes/profiles/`) uses a profile-based architecture where each profile is an independent Claude environment with its own configuration, soul file (SOUL.md), and toolsets. Apiary consolidates this into an agent-based configuration where agents are defined in the main `apiary.yaml` with referenced soul files and preferred models.

## Hermes Profiles → Apiary Agents

### 1. Router Profile → `hermes-router` Agent

**Hermes Location**: `~/.hermes/profiles/router/`

**Apiary Configuration**:
```yaml
- id: hermes-router
  description: "Hermes Router — reassigns tasks to correct specialist agents"
  soul_file: .apiary/souls/hermes-router.md
  preferred_models: [claude-opus-4-8]
  skills: [gitnexus-codebase, changelog-contexto]
```

**Purpose**: Reads newly created Plane tasks and routes them to the appropriate agent (engineer, qa, or po) based on task type.

**Key Rules** (from SOUL.md):
- Only reassigns tasks — never implements, tests, or analyzes code
- Routes based on task type:
  - `[BUG]` / `[TASK]` with code changes → `hermes-engineer`
  - `[QA]` or testing needed → `hermes-qa`
  - `[PO]` or requirements → `hermes-po`
- Completes immediately after reassigning

**Model**: claude-opus-4-8 (decision-making for routing)

### 2. Engineer Profile → `hermes-engineer` Agent

**Hermes Location**: `~/.hermes/profiles/engineer/`

**Apiary Configuration**:
```yaml
- id: hermes-engineer
  description: "Hermes Engineer — implements ERP features with mandatory git workflow"
  soul_file: .apiary/souls/hermes-engineer.md
  preferred_models: [claude-sonnet-4-6, claude-haiku-4-5]
  skills: [git-workflow, e2e-before-pr, gitnexus-codebase, handler-error-logging, changelog-contexto]
```

**Purpose**: Implements code fixes and features in the ERP system (Go backend, Next.js/React frontend, PostgreSQL, Docker).

**Key Rules** (from SOUL.md):
- **EVERY change MUST use `git worktree`** — NEVER edit main directly
- Commit → push → PR → auto-merge → sync → rebuild → reindex → cleanup
- Bug fixes REQUIRE unit test + E2E test
- Code standards:
  - Monetary values: NUMERIC in Postgres, decimal libraries in code
  - Text search: case-insensitive + accent-insensitive (unaccent + ILIKE)
  - Number formatting: pt-BR locale
- Prefer bulk saves over fragmented API calls

**Models**: claude-sonnet-4-6, claude-haiku-4-5 (fast implementation)

### 3. PO Profile → `hermes-po` Agent

**Hermes Location**: `~/.hermes/profiles/po/`

**Apiary Configuration**:
```yaml
- id: hermes-po
  description: "Hermes PO — creates and validates OpenSpec changes"
  soul_file: .apiary/souls/hermes-po.md
  preferred_models: [claude-opus-4-8]
  skills: [gitnexus-codebase, changelog-contexto, delegate-to-agent]
```

**Purpose**: Creates and validates OpenSpec changes, transforming business requirements into detailed technical specifications.

**Key Rules** (from SOUL.md):
- **EVERY scope change MUST start with OpenSpec** (proposal → design → tasks)
- Tasks must be small (< 60 turns each)
- QA tasks separate from engineer tasks
- Definition of Done: PO accepts → Engineer implements → QA tests → PO accepts
- Communicates OpenSpec for user approval before unblocking implementation
- Follow git workflow: worktree → PR → auto-merge

**Model**: claude-opus-4-8 (reasoning for architectural decisions)

### 4. QA Profile → `hermes-qa` Agent

**Hermes Location**: `~/.hermes/profiles/qa/`

**Apiary Configuration**:
```yaml
- id: hermes-qa
  description: "Hermes QA — validates running implementation against acceptance criteria"
  soul_file: .apiary/souls/hermes-qa.md
  preferred_models: [claude-sonnet-4-6, claude-haiku-4-5]
  skills: [e2e-before-pr, changelog-contexto]
```

**Purpose**: Validates implementations against acceptance criteria by testing the running application. NEVER analyzes source code.

**Key Rules** (from SOUL.md):
- **NEVER analyze source code** — test the running application only
- BEFORE testing: verify `main` branch is clean and current
- Test coverage: happy path, edge cases, flow variations, destructive testing
- Bug reporting: screenshot + reproduction steps, title format `[LEVEL] [MODULE] Description`
- Regression testing: full test suite after implementation
- Test on Chrome (primary) and Firefox (secondary) as per E2E specs

**Models**: claude-sonnet-4-6, claude-haiku-4-5 (browser automation)

## Key Differences: Hermes vs Apiary

| Aspect | Hermes | Apiary |
|--------|--------|--------|
| **Configuration** | Profile folder in `~/.hermes/profiles/` | YAML config in `apiary.yaml` |
| **Soul File** | `SOUL.md` in profile folder | Separate soul files in `.apiary/souls/` |
| **Model** | Configured in `config.yaml` (provider, base_url) | `preferred_models` list in agent config |
| **Skills** | Symlinked in profile's skills folder | Referenced in agent config as skill array |
| **Dispatch** | Kanban worker spawns with profile context | CLI runner spawns with agent ID and soul file |
| **Isolation** | Each profile is separate Claude session | Agents share same coordinator, different runners |
| **Toolsets** | Profile-specific (hermes-cli, kanban) | Shared toolsets from CLI runner config |

## Migration Checklist

When adapting Hermes profiles to Apiary:

- [x] Understand Hermes profile structure and responsibilities
- [x] Extract SOUL.md content from each profile
- [x] Identify preferred model from `config.yaml` (Hermes used deepseek-v4-flash; Apiary maps to Claude)
- [x] Identify required skills from profile setup
- [x] Create Apiary agent definition with:
  - Unique agent ID (prefix with `hermes-` to indicate origin)
  - Clear description
  - Reference to new soul file in `.apiary/souls/`
  - Preferred models list
  - Required skills array
- [x] Create soul file in `.apiary/souls/` based on Hermes SOUL.md
- [x] Add agents to `agents:` section of `apiary.yaml`
- [x] Create routing rules if needed

## Using Hermes Agents in Apiary

To use the Hermes agents in your Apiary setup:

1. **Copy the soul files**:
   ```bash
   cp .apiary/souls/hermes-*.md your-apiary-config-dir/.apiary/souls/
   ```

2. **Update your `apiary.yaml`** to include the agent definitions:
   ```yaml
   agents:
     - id: hermes-router
       description: "Hermes Router — reassigns tasks to correct specialist agents"
       soul_file: .apiary/souls/hermes-router.md
       preferred_models: [claude-opus-4-8]
       skills: [gitnexus-codebase, changelog-contexto]
     # ... other agents ...
   ```

3. **Create routing rules** to dispatch tasks to Hermes agents:
   ```yaml
   routes:
     - id: task-routing
       priority: 1
       match:
         source: your-source-id
         labels: []  # All unassigned tasks
       agent: hermes-router
       on_complete:
         set_state: in review
   ```

## Skills Imported from Hermes

The following skill files were already available in the project-erp project and are referenced by Hermes agents:

- **git-workflow** — Git conventions and worktree workflow
- **e2e-before-pr** — Pre-PR testing requirements
- **gitnexus-codebase** — Code intelligence and impact analysis
- **changelog-contexto** — OpenSpec changelog management
- **delegate-to-agent** — Agent delegation patterns
- **handler-error-logging** — Backend error handling patterns

These skills are already in the `.claude/skills/` directory and should be copied to `.apiary/` if you want Apiary to manage them independently.

## Notes on Model Selection

Hermes profiles use `deepseek-v4-flash` from opencode-go provider. Apiary uses Claude models:

- **claude-opus-4-8** — Reasoning-heavy tasks (routing, PO specs, staff design)
- **claude-sonnet-4-6** — Balanced implementation (engineer, QA, reviewer)
- **claude-haiku-4-5** — Fast, simple tasks (fallback for engineer/QA if needed)

This mapping assumes:
- Hermes "5 max_turns" (router) → Opus (complex routing logic)
- Hermes "200-500 max_turns" (engineer, po, qa) → Sonnet (production work)
- Fallback to Haiku for speed when needed

## Future Enhancements

1. **Model Fallback Logic**: Implement fallback from preferred_models[0] to preferred_models[1] if first model is unavailable
2. **Turn Budgeting**: Add `max_turns` field to agent config to match Hermes behavior
3. **Skill Templating**: Create skill generation from templates for project-specific variations
4. **Profile Merging**: Allow agents to inherit from base profiles (e.g., hermes-engineer-frontend extends hermes-engineer)

## Troubleshooting

**Issue**: Agent not picking up changes to soul file
- **Solution**: Soul files are read at dispatch time (not startup), so changes take effect immediately

**Issue**: Agent using wrong model
- **Solution**: Check that `preferred_models` array is non-empty and models are valid in your system

**Issue**: Agent missing required skill
- **Solution**: Verify skill is listed in agent's `skills` array and exists in `.apiary/skills/` or `.claude/skills/`

**Issue**: Hermes profile still in use but want to switch to Apiary
- **Solution**: Update your Plane routing labels to point to Apiary agents instead (e.g., `agent:hermes-engineer` instead of kanban worker ID)
