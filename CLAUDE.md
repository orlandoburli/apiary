<!-- gitnexus:start -->
# GitNexus — Code Intelligence

This project is indexed by GitNexus as **apiary** (6306 symbols, 22913 relationships, 300 execution flows). Use the GitNexus MCP tools to understand code, assess impact, and navigate safely.

> Index stale? Run `node .gitnexus/run.cjs analyze` from the project root — it auto-selects an available runner. No `.gitnexus/run.cjs` yet? `npx gitnexus analyze` (npm 11 crash → `npm i -g gitnexus`; #1939).

## Always Do

- **MUST run impact analysis before editing any symbol.** Before modifying a function, class, or method, run `impact({target: "symbolName", direction: "upstream"})` and report the blast radius (direct callers, affected processes, risk level) to the user.
- **MUST run `detect_changes()` before committing** to verify your changes only affect expected symbols and execution flows. For regression review, compare against the default branch: `detect_changes({scope: "compare", base_ref: "main"})`.
- **MUST warn the user** if impact analysis returns HIGH or CRITICAL risk before proceeding with edits.
- When exploring unfamiliar code, use `query({search_query: "concept"})` to find execution flows instead of grepping. It returns process-grouped results ranked by relevance.
- When you need full context on a specific symbol — callers, callees, which execution flows it participates in — use `context({name: "symbolName"})`.
- For security review, `explain({target: "fileOrSymbol"})` lists taint findings (source→sink flows; needs `analyze --pdg`).

## Never Do

- NEVER edit a function, class, or method without first running `impact` on it.
- NEVER ignore HIGH or CRITICAL risk warnings from impact analysis.
- NEVER rename symbols with find-and-replace — use `rename` which understands the call graph.
- NEVER commit changes without running `detect_changes()` to check affected scope.

## Resources

| Resource | Use for |
|----------|---------|
| `gitnexus://repo/apiary/context` | Codebase overview, check index freshness |
| `gitnexus://repo/apiary/clusters` | All functional areas |
| `gitnexus://repo/apiary/processes` | All execution flows |
| `gitnexus://repo/apiary/process/{name}` | Step-by-step execution trace |

## CLI

| Task | Read this skill file |
|------|---------------------|
| Understand architecture / "How does X work?" | `.claude/skills/gitnexus/gitnexus-exploring/SKILL.md` |
| Blast radius / "What breaks if I change X?" | `.claude/skills/gitnexus/gitnexus-impact-analysis/SKILL.md` |
| Trace bugs / "Why is X failing?" | `.claude/skills/gitnexus/gitnexus-debugging/SKILL.md` |
| Rename / extract / split / refactor | `.claude/skills/gitnexus/gitnexus-refactoring/SKILL.md` |
| Tools, resources, schema reference | `.claude/skills/gitnexus/gitnexus-guide/SKILL.md` |
| Index, status, clean, wiki CLI commands | `.claude/skills/gitnexus/gitnexus-cli/SKILL.md` |

<!-- gitnexus:end -->

## After running `gitnexus analyze`

A **full** index build — a fresh clone, a **new git worktree**, or `--force` —
rewrites all six `.claude/skills/gitnexus/*/SKILL.md` files from the tool's
bundled copies, discarding what this repo commits there (measured on 1.6.3: 76
insertions, 165 deletions). `--skip-agents-md` does **not** prevent it: it covers
only the generated block above. An incremental analyze over an existing
`.gitnexus/` leaves them alone, which is why this mostly bites in worktrees —
every new one is a first analyze.

Always restore them afterwards, and check the tree is clean:

```bash
gitnexus analyze --skip-agents-md && git checkout -- .claude/skills/gitnexus/
git status --short
```

What the regenerated copies drop matters: the `node .gitnexus/run.cjs` runner
instructions and the npm 11 `npx` crash workaround (#1939) — the very failure the
line above tells you to route around.

Tracked in #457; upstream at abhigyanpatwari/GitNexus#3080.
