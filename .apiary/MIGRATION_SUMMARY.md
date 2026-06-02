# Apiary Migration Summary

## What Was Migrated

### Agents (6 agents created)

✅ **investigator.md** — Analyzes and classifies tasks by complexity  
✅ **po.md** — Product Owner: transforms business demands into specs  
✅ **staff.md** — Staff Engineer: designs complex solutions  
✅ **engineer.md** — Engineer: implements with all project conventions  
✅ **reviewer.md** — Reviewer: performs code review and quality checks  
✅ **qa.md** — QA: validates against acceptance criteria  

All agents are adapted from the Hermes ERP project automation pipeline and customized for Apiary.

### Skills (7 skills copied)

✅ **git-workflow** — Feature branches, worktree, PR, auto-merge  
✅ **e2e-before-pr** — Unit/lint/UI/E2E/smoke tests  
✅ **gitnexus-codebase** — Code intelligence and impact analysis  
✅ **changelog-contexto** — OpenSpec changelog management  
✅ **delegate-to-agent** — Agent delegation via Plane  
✅ **docs-obsidian-mirror** — Documentation sync to Obsidian  
✅ **handler-error-logging** — Backend error handling patterns  

All skills are imported from the project-erp/.claude/skills/ directory.

## Directory Structure

```
apiary/
├── .apiary/
│   ├── souls/
│   │   ├── investigator.md       ← Agent personality
│   │   ├── po.md                 ← Agent personality
│   │   ├── staff.md              ← Agent personality
│   │   ├── engineer.md           ← Agent personality
│   │   ├── reviewer.md           ← Agent personality
│   │   ├── qa.md                 ← Agent personality
│   │   ├── AGENTS_AND_SKILLS.md  ← Documentation
│   │   └── MIGRATION_SUMMARY.md  ← This file
│   └── example-apiary-full.yaml  ← Complete config example
├── .claude/
│   └── skills/
│       ├── git-workflow/         ← Copied skill
│       ├── e2e-before-pr/        ← Copied skill
│       ├── gitnexus-codebase/    ← Copied skill
│       ├── changelog-contexto/   ← Copied skill
│       ├── delegate-to-agent/    ← Copied skill
│       ├── docs-obsidian-mirror/ ← Copied skill
│       ├── handler-error-logging/← Copied skill
│       └── gitnexus/             ← Built-in skill
└── src/
    └── internal/
        ├── config/
        │   ├── config.go          ← AgentConfig struct
        │   └── validate.go        ← Agent validation
        ├── daemon/
        │   └── dispatcher.go       ← Agent instantiation & dispatch
        ├── router/
        │   └── router.go          ← Agent routing
        └── model/
            └── result.go          ← AgentMetadata field
```

## Key Features

### 1. **Role-Based Agents**
- Each agent has a specific role in the automation pipeline
- Agents can specialize their behavior via soul files
- Agents can use different models based on task complexity

### 2. **Skill-Based Guidance**
- Agents reference skills that define best practices and conventions
- Skills include project-specific guidelines (git workflow, testing, logging)
- Skills enable agents to follow project patterns consistently

### 3. **Late-Binding Soul Files**
- Soul files loaded at task dispatch time (not startup)
- Changes to soul files take effect immediately without restart
- Enables iterative improvement of agent instructions

### 4. **Multi-Model Support**
- Agents can specify preferred models in priority order
- PO and Staff agents use Opus for complex reasoning
- Engineer, Reviewer, QA agents use Sonnet with Haiku fallback

## Using the Agents

### Configuration

Copy `.apiary/example-apiary-full.yaml` to `apiary.yaml` to use all agents:

```bash
cp .apiary/example-apiary-full.yaml apiary.yaml
```

### Task Routing

Use Plane labels to route issues:

1. **New issue** → No special label (investigator routes it)
2. **Needs design** → Add `agent:staff` label
3. **Needs specs** → Add `agent:po` label
4. **Ready to implement** → Add `agent:engineer` label
5. **Ready for review** → Add `agent:reviewer` label
6. **Ready for QA** → Add `agent:qa` label

### Running

```bash
# Validate configuration
apiary validate

# Test what would be dispatched
apiary run --dry-run --verbose

# Run once and exit
apiary run --once

# Run as daemon (continuous polling)
apiary run
```

## Next Steps

1. **Customize soul files** — Adjust agent personalities for your specific needs
2. **Add more skills** — Copy additional skills from project-erp as needed
3. **Configure Plane integration** — Set up API keys and project IDs
4. **Test with real tasks** — Create test issues and verify routing and execution
5. **Monitor and iterate** — Review agent behavior and refine instructions

## Notes

- All agent definitions adapted from ERP Hermes project automation pipeline
- Soul files follow the convention of `.apiary/souls/<agent-name>.md`
- Skills provide reusable guidance for consistent agent behavior across tasks
- The system supports both simple single-agent flows and complex multi-agent pipelines
