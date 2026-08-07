# Apiary Agents and Skills

> **Historical.** This describes the eight-agent roster used when `.apiary/`
> held the operator config for **project-erp** (investigator, po, staff,
> engineer, backend, frontend, reviewer, qa). That config was replaced by the
> self-hosting setup — apiary working on apiary — whose roster is investigator,
> staff, engineer, docs, reviewer, qa. The `po`, `backend` and `frontend` soul
> files no longer exist.
>
> For the current setup see [`apiary.yaml`](apiary.yaml) and the souls in
> [`souls/`](souls/). The skills sections below are still broadly accurate.

This document describes the agents and skills available for the Apiary automation pipeline.

## Agents (`.apiary/souls/`)

### 1. **Investigator Agent**
- **Role**: Analyzes and classifies newly created tasks by complexity
- **Responsibilities**: 
  - Understand context using GitNexus
  - Classify complexity (simple/medium/complex/blocker)
  - Route tasks to appropriate agents
  - Flag dependencies and risks
- **Soul File**: `.apiary/souls/investigator.md`

### 2. **Product Owner (PO) Agent**
- **Role**: Transforms business demands into specifications
- **Responsibilities**:
  - Discovery and analysis using GitNexus
  - Create OpenSpec proposals and specifications
  - Define acceptance criteria in business language
  - Prioritize and decompose into sub-tasks
  - Validate post-implementation against business requirements
- **Soul File**: `.apiary/souls/po.md`
- **Model**: `claude-opus-4-8` (maximum reasoning capability)

### 3. **Staff Engineer Agent**
- **Role**: Designs complex solutions and decomposes them
- **Responsibilities**:
  - Deep code and architecture analysis
  - Propose design approach with trade-offs
  - Identify risks and mitigations
  - Decompose into logical sub-tasks
  - Document architectural decisions
- **Soul File**: `.apiary/souls/staff.md`
- **Model**: `claude-opus-4-8` (maximum reasoning capability)

### 4. **Engineer Agent**
- **Role**: Implements tasks following project conventions
- **Responsibilities**:
  - Impact analysis before editing
  - Implementation in isolated git worktree
  - Running lint and tests
  - Opening PR with auto-merge
  - Creating review issues in Plane
- **Soul File**: `.apiary/souls/engineer.md`
- **Models**: Sonnet (primary), Haiku (fallback)

### 5. **Reviewer Agent**
- **Role**: Performs code review before QA
- **Responsibilities**:
  - Code correctness and quality review
  - Convention adherence (CLAUDE.md)
  - Impact analysis on modified symbols
  - Detailed PR feedback
  - Approve or request revisions
- **Soul File**: `.apiary/souls/reviewer.md`
- **Models**: Sonnet, Haiku

### 6. **QA Agent**
- **Role**: Validates implementation against acceptance criteria
- **Responsibilities**:
  - Functional testing
  - Edge case and error scenario testing
  - Acceptance criteria verification
  - Regression testing
  - Report findings and approve/reject
- **Soul File**: `.apiary/souls/qa.md`
- **Models**: Sonnet, Haiku

---

## Hermes-Based Agents

These agents are adapted from the Hermes kanban system and bring proven workflow patterns to Apiary.

See [HERMES_MIGRATION.md](./HERMES_MIGRATION.md) for detailed information about the migration.

### 7. **Hermes Router Agent**
- **Role**: Routes tasks to the correct specialist agent based on task type
- **Responsibilities**:
  - Read task title and body
  - Classify task type (bug, feature, test, spec)
  - Reassign to correct agent (engineer, qa, or po)
  - Complete immediately after routing
- **Soul File**: `.apiary/souls/hermes-router.md`
- **Constraint**: NEVER implement, test, or analyze code — only route
- **Model**: `claude-opus-4-8` (decision-making)
- **Origin**: Hermes `router` profile

### 8. **Hermes Engineer Agent**
- **Role**: Implements ERP features with mandatory git workflow
- **Responsibilities**:
  - Code implementation (Go, Next.js/React, PostgreSQL, Docker)
  - Bug fixes with tests (unit + E2E required)
  - Strict git workflow (worktree → PR → auto-merge → sync → rebuild → reindex)
  - Code standards:
    - Monetary values as NUMERIC in Postgres + decimal libraries in code
    - Text search: case-insensitive + accent-insensitive
    - Number formatting: pt-BR locale
    - Bulk API saves over fragmented calls
- **Soul File**: `.apiary/souls/hermes-engineer.md`
- **Models**: Sonnet, Haiku (balanced implementation speed)
- **Origin**: Hermes `engineer` profile (`~/.hermes/profiles/engineer/`)

### 9. **Hermes PO Agent**
- **Role**: Creates and validates OpenSpec changes (business → specifications)
- **Responsibilities**:
  - Create OpenSpec changes (proposal → design → tasks)
  - Validate consistency across artifacts
  - Keep task scope small (< 60 turns each)
  - Separate QA tasks from engineer tasks
  - Communicate specifications to user for approval
  - Enforce Definition of Done: PO accepts → Engineer implements → QA tests → PO accepts
- **Soul File**: `.apiary/souls/hermes-po.md`
- **Model**: `claude-opus-4-8` (architectural reasoning)
- **Origin**: Hermes `po` profile (`~/.hermes/profiles/po/`)

### 10. **Hermes QA Agent**
- **Role**: Validates running implementations (browser testing, never code analysis)
- **Responsibilities**:
  - Test running application at `localhost:8088` ONLY
  - NEVER analyze source code
  - Verify main branch is clean before testing (BLOCK if not)
  - Test coverage: happy path, edge cases, flow variations, destructive tests
  - Report bugs with screenshots and reproduction steps
  - Regression testing after implementation
  - Run tests on Chrome (primary) and Firefox (secondary)
- **Soul File**: `.apiary/souls/hermes-qa.md`
- **Models**: Sonnet, Haiku (browser automation)
- **Origin**: Hermes `qa` profile (`~/.hermes/profiles/qa/`)

## Skills (`.claude/skills/`)

### Copied from Project-ERP

1. **git-workflow** — Git workflow conventions for the project
   - Feature branches with git worktree
   - PR creation and auto-merge
   - Commit message standards
   - Branch cleanup

2. **e2e-before-pr** — Pre-PR testing requirements
   - Unit tests
   - Lint checks
   - UI tests
   - E2E tests (Playwright)
   - Docker smoke tests

3. **gitnexus-codebase** — Code intelligence and impact analysis
   - GitNexus query for code exploration
   - Impact analysis (upstream/downstream)
   - Change detection
   - Symbol context lookup

4. **changelog-contexto** — OpenSpec changelog management
   - Active changes tracking
   - Change archival
   - Historical context review
   - Spec file synchronization

5. **delegate-to-agent** — Agent delegation via Plane
   - Creating issues with labels
   - Routing to specific agents
   - Context passing
   - Agent label standards

6. **docs-obsidian-mirror** — Documentation synchronization
   - Markdown mirroring to Obsidian vault
   - iCloud sync support
   - File organization

7. **handler-error-logging** — Backend error handling standards
   - Go error logging patterns
   - Error context preservation
   - HTTP response error handling

## Agent Labels (Plane)

Use these labels to route issues to the appropriate agent:

### Claude-Based Agents
- `agent:investigator` — New task that needs classification
- `agent:po` — Business requirements needing specification
- `agent:staff` — Complex task needing design analysis
- `agent:engineer` — Ready to implement
- `agent:reviewer` — Ready for code review
- `agent:qa` — Ready for testing

### Hermes-Based Agents
- `agent:hermes-router` — Route to correct specialist (auto-routing)
- `agent:hermes-po` — OpenSpec creation and validation
- `agent:hermes-engineer` — ERP implementation with mandatory workflow
- `agent:hermes-qa` — Browser-based testing and validation

**Recommendation**: Use `agent:hermes-router` to automatically route incoming tasks to appropriate specialists, OR use specific agent labels to directly assign to a known agent type.

## Configuration Example

See `.apiary/example-apiary-full.yaml` for a complete Apiary configuration with all agents and routing rules.

### Quick Start

```yaml
version: "1"

agents:
  - id: engineer
    description: "Engineer — implements tasks"
    soul_file: .apiary/souls/engineer.md
    preferred_models: [claude-sonnet-4-6, claude-haiku-4-5]
    skills: [git-workflow, e2e-before-pr, gitnexus-codebase]

routes:
  - id: engineer-implement
    priority: 20
    match:
      source: project-erp
      labels: [agent:engineer]
    agent: engineer
    on_complete:
      set_state: in review

settings:
  concurrency: 2
  log_level: info
  state_lock: true
  result_comment: true
```

## Running Apiary

```bash
# Validate configuration
apiary validate

# Dry run (see what would be dispatched)
apiary run --dry-run --verbose

# Run once (poll, dispatch, and exit)
apiary run --once

# Run daemon (continuous polling)
apiary run
```

## Notes

- All agent soul files are in `.apiary/souls/` directory
- All skills are in `.claude/skills/` directory
- Agents use preferred models in priority order (fallback if first unavailable)
- Soul files are loaded at dispatch time (late binding) — changes take effect immediately
- Skills define the agent's capabilities and guide behavior
