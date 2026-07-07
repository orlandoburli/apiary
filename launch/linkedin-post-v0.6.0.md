LinkedIn post — Apiary v0.7.0: First Public Beta

(English, dev / AI-devtools audience. Paste as-is; LinkedIn ignores markdown.)

---

After six months in production, Apiary is now in public beta.

It's an open-source agent dispatcher. You point it at your GitHub/Jira/Plane backlog, 
define workflows in YAML, and let it run unattended. No SaaS, no signup, no complexity — 
just one Go binary and a SQLite file.

What's included:

- Dependency blocking — gate downstream steps on upstream completion
- Agent memory — facts persist across runs
- Cost tracking — see what you spend per agent and per step
- Resilience — rate-limit failover, re-dispatch caps, approval gates, CI waits
- Dashboard — live task view, cost rollup, logs, history

It's running real production work. Config APIs may evolve before v1.0 (hence "beta"), but 
the core is solid.

Try it:
👉 github.com/orlandoburli/apiary

Brew: `brew install --cask orlandoburli/tap/apiary`
Docker: `docker run ghcr.io/orlandoburli/apiary`

#AI #DeveloperTools #OpenSource #LLM #Automation #DevOps
