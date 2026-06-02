# Apiary Agents & Skills

Complete automation pipeline with 7 agents and 8 skills for managing tasks through Plane.

## 🎯 Quick Start

1. **Copy the full configuration**:
   ```bash
   cp .apiary/example-apiary-full.yaml ../apiary.yaml
   ```

2. **Validate it works**:
   ```bash
   ../apiary validate
   ../apiary run --dry-run --verbose
   ```

3. **Start routing tasks** — Label issues in Plane with agent labels to route them

## 🤖 Available Agents

| Agent | Role | Model | Skills | Soul File |
|-------|------|-------|--------|-----------|
| **investigator** | Classify & route tasks | Opus | gitnexus, changelog | `investigator.md` |
| **po** | Business specs & design | Opus | gitnexus, changelog, delegate | `po.md` |
| **staff** | Architecture & decomposition | Opus | gitnexus, git, changelog | `staff.md` |
| **engineer** | Implementation | Sonnet/Haiku | All (full stack) | `engineer.md` |
| **reviewer** | Code review & quality | Sonnet/Haiku | git, gitnexus, e2e | `reviewer.md` |
| **qa** | Testing & validation | Sonnet/Haiku | e2e, changelog | `qa.md` |

## 🏷️ Plane Labels for Routing

Use these labels to route issues to specific agents:

- `agent:investigator` — Analyze and classify
- `agent:po` — Create business specifications
- `agent:staff` — Design complex solutions
- `agent:engineer` — Implement the task
- `agent:reviewer` — Review code
- `agent:qa` — Validate implementation

## 📚 Included Skills

1. **git-workflow** — Feature branches, git worktree, PR creation, auto-merge
2. **e2e-before-pr** — Unit/lint/UI/E2E/docker tests before PR
3. **gitnexus-codebase** — Code intelligence, impact analysis, symbol context
4. **changelog-contexto** — OpenSpec change management and tracking
5. **delegate-to-agent** — Creating issues and delegating to other agents
6. **docs-obsidian-mirror** — Markdown synchronization to Obsidian vault
7. **handler-error-logging** — Backend error handling and logging patterns
8. **gitnexus** — Built-in code analysis (read-only, included)

## 📂 File Structure

```
.apiary/
├── souls/
│   ├── investigator.md     Agent 1: Task classification
│   ├── po.md              Agent 2: Product ownership
│   ├── staff.md           Agent 3: Architectural design
│   ├── engineer.md        Agent 4: Implementation
│   ├── reviewer.md        Agent 5: Code review
│   ├── qa.md              Agent 6: Quality assurance
│   ├── frontend.md        (Example agent from migration)
│   ├── backend.md         (Example agent from migration)
│   ├── README.md          This file
│   ├── AGENTS_AND_SKILLS.md    Detailed documentation
│   ├── MIGRATION_SUMMARY.md    What was migrated
│   └── example-apiary-full.yaml Complete config example

.claude/skills/
├── git-workflow/          Git conventions & worktree workflow
├── e2e-before-pr/         Pre-PR testing requirements
├── gitnexus-codebase/     Code intelligence & impact analysis
├── changelog-contexto/    OpenSpec changelog management
├── delegate-to-agent/     Agent delegation patterns
├── docs-obsidian-mirror/  Documentation sync
├── handler-error-logging/ Error handling patterns
└── gitnexus/              Built-in code analysis tool
```

## 🚀 Common Workflows

### Full Automation Pipeline
```
New Issue
  ↓
[Investigator] — Classify complexity
  ↓
[Staff] (if complex) → Design solution
  ↓
[PO] → Write specifications
  ↓
[Engineer] → Implement
  ↓
[Reviewer] → Code review
  ↓
[QA] → Test & validate
  ↓
Done
```

### Simple Task
```
Issue with label "agent:engineer"
  ↓
[Engineer] → Implement
  ↓
[Reviewer] → Review
  ↓
[QA] → Validate
  ↓
Done
```

### Specification First
```
Issue with label "agent:po"
  ↓
[PO] → Write OpenSpec specs
  ↓
[Engineer] → Implement to spec
  ↓
[Reviewer] → Review
  ↓
[QA] → Validate against specs
  ↓
Done
```

## ⚙️ Configuration Options

### Minimal (Single Agent)
```yaml
agents:
  - id: engineer
    soul_file: .apiary/souls/engineer.md
    preferred_models: [claude-sonnet-4-6]
    skills: [git-workflow, e2e-before-pr]

routes:
  - id: engineer-task
    match: { labels: [agent:engineer] }
    agent: engineer
```

### Full Pipeline (All 6 Agents)
See `example-apiary-full.yaml` for complete configuration with all agents and routing rules.

### Custom
Mix and match agents, skills, and routing rules based on your needs.

## 🔧 Customization

### Add a New Agent
1. Create a new soul file in `.apiary/souls/myagent.md`
2. Add to `apiary.yaml` agents section
3. Create route(s) that reference your agent

### Modify Agent Behavior
1. Edit the `.apiary/souls/<agent>.md` file
2. Changes take effect on next task dispatch (no restart needed)

### Add More Skills
Copy skills from project-erp:
```bash
cp -r /path/to/project-erp/.claude/skills/skill-name .claude/skills/
```

## 📖 Documentation

- **AGENTS_AND_SKILLS.md** — Detailed description of each agent and skill
- **MIGRATION_SUMMARY.md** — What was migrated from the ERP project
- **example-apiary-full.yaml** — Complete configuration example
- This file — Quick reference and getting started

## 🎓 Learning Resources

- Apiary main README: `/apiary/README.md`
- Apiary architecture: Run `apiary run --help` for CLI options
- GitNexus: Use `gitnexus query`, `gitnexus impact`, `gitnexus context`
- OpenSpec: Use `/opsx-propose`, `/opsx-apply`, `/opsx-archive`

## 📝 Notes

- All agents use Claude models (Opus for complex reasoning, Sonnet for implementation)
- Soul files are loaded at dispatch time (changes immediate)
- Skills provide guidance for consistent agent behavior
- Multiple agents can work in parallel (concurrency configurable in settings)
- Plane API integration required for task management

## 🔗 Source

- Agents adapted from: ERP Hermes automation pipeline
- Skills copied from: project-erp/.claude/skills/
- Apiary core: Agent-based task dispatcher with Plane integration

---

**Ready to use!** Start labeling tasks in Plane and watch the agents work.
