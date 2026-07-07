Title: Apiary is now in public beta — your AI agents can finally run unattended.

Subtitle: After six months in production, we're releasing Apiary v0.7.0 as the first open-source harness for declarative, reliable agent dispatch.

---

> **Publishing notes (delete before posting):** drop screenshots from
> `docs/screenshots/` where indicated. Cover image: `docs/logo.svg` on dark background. 
> Canonical link: GitHub repo. Tags: `AI`, `Developer Tools`, `Open Source`, `Automation`, `LLM`.

---

## The dispatcher problem

AI agents are incredible at writing code. Claude, OpenCode, Cursor — give them a task and
they'll deliver. But there's a human bottleneck nobody talks about: the dispatcher.

Someone reads your backlog. Someone decides this issue is a good fit for an agent. Someone
picks the model, pastes the context, kicks it off, and watches it run. The agent is
autonomous. The dispatching isn't.

And when you scale to multi-stage workflows (design → implement → test → review), it gets
worse. You need to wait for design to finish before implement runs. You need to know if
design failed so implement doesn't waste time. You need to track costs. You need to
remember state across runs. You need this to survive restarts.

That's what Apiary solves.

## What Apiary does

Apiary is a small, self-hosted Go binary. You write one `apiary.yaml` file. It says:

- **Where work comes from:** "Poll GitHub Issues, Plane, Jira, or Linear for tasks."
- **How to route:** "If the label is `bug`, use the debugging-expert agent. If it's
  `feature`, use the architect."
- **What to do with results:** "Close the issue, post a comment, update a status."

Then you run `apiary daemon` and walk away. Tasks flow in, get dispatched to the right
agent and model, and results flow back out.

It's not SaaS. It's not a UI. It's one binary. It stores state in a local SQLite file. You
own your data. You own your costs.

```yaml
workflows:
  - id: code-review
    trigger:
      kind: github
      on: issues.opened

    steps:
      - id: design
        agent: architect

      - id: implement
        agent: engineer
        kind: dependency
        wait_for_task: design

      - id: test
        agent: qa
        kind: dependency
        wait_for_task: implement
```

When design completes, implement wakes up. When implement finishes, test starts. If design
fails, everything downstream stays parked. And if the daemon crashes, the state survives.

```yaml
workflows:
  - id: code-review
    trigger:
      kind: github
      on: issues.opened

    steps:
      - id: design
        agent: architect
        agent_model: claude-opus-4-8

      - id: implement
        agent: engineer
        kind: dependency
        wait_for_task: design

      - id: test
        agent: qa
        kind: dependency
        wait_for_task: implement
```

When `design` completes, `implement` wakes up and runs. When `implement` finishes, `test`
starts. If `design` fails, `implement` never runs.

> _[screenshot: docs/screenshots/tasks.png — showing task lineage with blocking]_

## Under the hood

This wasn't trivial. Apiary had to:

- **Link tasks** across systems. GitHub has `blocked_by`; Jira has `Blocks`; Linear has its
  own model. The source adapters now surface these relationships, and the engine matches
  them.
- **Park and resume** at task granularity. A step inside a workflow waits; the whole task
  parks. When the blocker completes, the task wakes up mid-workflow and resumes. State
  survives a restart.
- **Keep the logs clean.** Every task log now includes the source reference at the top — 
  issue number, PR, Jira key — so you know exactly which work the run came from without 
  searching.

## Why this matters

Workflows with dependencies unlock new patterns:

- **Staged reviews.** A junior engineer's work gets reviewed by a senior before any
  deployment step runs. The whole thing runs unattended, with blocking built in.
- **Multi-stage refinement.** Generate → filter → rank. Each stage waits for the previous
  one.
- **Parallel fanning and merging.** Split a task across multiple agents (refactor the auth
  layer AND the API layer in parallel), then merge back into a test step.

And because Apiary runs unattended, these patterns actually work. No human sitting around
waiting for a design to finish before saying "okay, start implementing."

> _[screenshot: docs/screenshots/dashboard.png or logs view]_

## What's next

Apiary is in active use. The next features on the roadmap:

- **Expression validation at startup** — catch `${{ expr }}` typos before a task runs
- **Cost budgets per workflow** — fail gracefully if a run overshoots its token budget
- **Built-in webhooks** — notify Slack/Discord when a task completes or a step fails

The expression language is fully documented now — check the docs for the syntax.

## Try it

Install via Homebrew (macOS/Linux), Scoop (Windows), or Docker:

```sh
brew install --cask orlandoburli/tap/apiary
```

Then write a workflow, point it at your GitHub issues or Jira, and let it run. It's
open-source (AGPLv3 + commercial license available), runs on your machine, and leaves a
full audit log of what it did and what it cost.

[GitHub repo](https://github.com/orlandoburli/apiary)
