LinkedIn post — Apiary v0.2.0 public beta
(English, dev / AI-devtools audience. Paste as-is; LinkedIn ignores markdown, so the
formatting below is plain-text-friendly. ~190 words.)

---

AI coding agents are incredible — and yet there's still a human stuck in the middle of
every run. Someone reads the backlog, picks the task, chooses the model, pastes the
context, and babysits it. The agent is autonomous. The dispatching isn't.

So I built Apiary to remove that human. It's an open-source harness that polls your issue
tracker (GitHub, Plane, Jira, Linear), routes each task to the right agent and model with
rules you write in one YAML file, and writes the result back — close the issue, add a
label, comment. No SaaS. Just a self-hosted Go binary.

Today it goes public beta (v0.2.0). The thing I'm most proud of isn't a flashy feature —
it's the boring reliability work that lets you actually leave it running: rate-limit
failover to a fallback model, a re-dispatch cap so a broken task can't loop forever,
non-blocking dispatch, and approval gates that survive a restart.

Try it (brew / scoop / docker), and tell me where it breaks:
👉 https://github.com/orlandoburli/apiary

#AI #DeveloperTools #OpenSource #LLM #Automation #AIagents #DevOps
