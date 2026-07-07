# LinkedIn post — Apiary v0.7.0: Open Source Release

(English, dev / AI-devtools audience. Paste as-is; LinkedIn ignores markdown.)

---

I was using an agent dispatcher to automate my backlog. It worked well, but there was a problem: it only supported APIs.

Every task ran through expensive Claude API endpoints instead of my Claude Pro subscription. API access costs 3-5x more than CLI pricing for the same model. I was burning money on something I already pay for.

I looked for alternatives. Same problem. All of them API-only.

So I built Apiary — a dispatcher that works with CLI tools (Claude, OpenCode, Cursor) instead of forcing you through expensive API tiers.

**What it does:**
- Polls your GitHub/Jira/Plane backlog
- Routes each task to the right agent and model using YAML rules
- Orchestrates multi-step workflows (design → implement → test → review)
- Writes results back automatically
- Tracks exact costs (no estimates, no vendor markup)

**Why it matters:**
You already have a Claude Pro subscription. You already have a machine. You shouldn't have to pay 3-5x more just to automate your backlog.

This is self-hosted. No SaaS, no vendor lock-in. One Go binary. SQLite database. Your data stays yours.

Try it today:

github.com/orlandoburli/apiary

docs: docs.apiary.ai

brew install --cask orlandoburli/tap/apiary

Full article: blog.orlandoburli.com.br/apiary-autonomous-ai-dispatch-for-your-backlog
