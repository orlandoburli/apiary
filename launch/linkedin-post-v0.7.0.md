LinkedIn post — Apiary v0.7.0: First public beta

(English, dev / AI-devtools audience. Paste as-is; LinkedIn ignores markdown.)

---

We shipped Apiary v0.7.0 to open-source today. It's been running unattended in production for six months. Now it's ready for yours.

Apiary is the AI agent dispatcher you didn't know you needed. It polls your GitHub/Jira/Plane backlog, routes each task to the right agent and model based on rules you write in YAML, orchestrates multi-step workflows (design → implement → test → review), and writes results back automatically.

No SaaS. No complicated auth. One Go binary. SQLite database. Your data stays yours.

What makes it different:

• Multi-system routing — ingest from GitHub Issues, Plane, Jira. Route to Claude, OpenCode, Cursor. One config.

• Dependency gating — downstream steps wait for upstream tasks to complete. No cascading failures.

• Persistent agent memory — agents remember what they learned. Facts survive retries and rewrites.

• Exact cost tracking — see the USD cost per agent, per task, per run. No estimates.

• Parked state that survives restarts — approval gates and CI waits resume where they left off after daemon crashes.

• Resilience by default — rate-limit failover, re-dispatch caps, timeouts, non-blocking dispatch.

All the complexity usually needed for unattended dispatch is already baked in. Your job: write the workflow, point it at your backlog, walk away.

Try it:

github.com/orlandoburli/apiary
docs: docs.apiary.ai

brew install --cask orlandoburli/tap/apiary

#AI #DeveloperTools #OpenSource #LLM #Automation #DevOps #Agents
