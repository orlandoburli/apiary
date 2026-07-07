Title: Your AI coding agent still needs a human dispatcher. Apiary removes them.

Subtitle: An open-source harness that routes work from your issue tracker to the right AI agent and model — and is built to run unattended.

---

> **Publishing notes (delete before posting):** drop the screenshots from
> `docs/screenshots/` where indicated (`overview.png`, `tasks.png`, `agents.png`,
> `logs.png`). Cover image suggestion: `docs/logo.svg` on a dark background. Canonical
> link should point back to the GitHub repo. Tags: `AI`, `Developer Tools`,
> `Open Source`, `Automation`, `LLM`.

---

## The loop nobody closes

The last two years gave us astonishing AI coding agents. Claude, OpenCode, Cursor — point
one at a well-scoped task and it'll write the code, run the tests, and open a PR.

And yet, look closely at how teams actually use them, and there's a human stuck in the
middle of every run. Someone reads the backlog. Someone decides *this* issue is a good fit
for an agent. Someone picks the model, pastes the context, kicks off the run, and babysits
it. The agent is autonomous; the *dispatching* is not.

That human-in-the-middle is the bottleneck. It doesn't scale, it doesn't run at 2 a.m.,
and it quietly caps how much of your backlog an agent can actually touch.

**Apiary closes that loop.** Tasks flow in from your issue tracker, Apiary decides which
agent and which model handles each one based on rules you write, and the work gets done —
no human dispatcher required.

Today it goes **public beta** with `v0.2.0`.

## What Apiary is

Apiary is a small, self-hosted Go binary. It polls your task system (GitHub Issues, Plane,
Jira, Linear), matches each task against your routing rules, and dispatches it to an AI
agent — then writes the result back to the source (close the issue, add a label, post a
comment).

```
Task System ──► Apiary ──► Agent ──► LLM Model
  (GitHub)      (router)  (backend-dev)  (claude-sonnet-4-6)
```

Everything is one declarative file, `apiary.yaml`. No SaaS, no database to stand up — state
lives in a local SQLite file next to your config.

> _[screenshot: docs/screenshots/overview.png — the dashboard overview]_

## The mental model

Five concepts, and you've got it:

- **Source** — an integration that polls work items and writes results back (GitHub, Plane,
  Jira, Linear).
- **Runner** — *how* an agent executes: a CLI subprocess (the `claude` / `opencode` /
  `cursor` binaries) or a direct API call.
- **Agent** — a named LLM persona: a runner + a model + an optional "soul" prompt and
  skills, with optional fallbacks.
- **Workflow** — a pipeline of steps, fired by a `trigger` that matches tasks. Each step
  runs an agent. The simplest workflow is one step — that's your basic "route this label to
  this agent" rule.
- **Task** — a unit of work flowing through the system (an issue or item from a source).

## A real `apiary.yaml`

Here's a complete, working config: poll a GitHub repo for issues labeled `ai-ready`, and
send backend bugs to a Claude-backed agent that closes the issue when it's done.

```yaml
version: "1"

runners:
  - id: claude
    type: cli
    provider: claude
    config:
      args: ["--output-format", "stream-json", "--verbose"]

sources:
  - id: main-repo
    type: github
    config:
      repo: my-org/my-repo
      api_key: ${GITHUB_TOKEN}
    poll_interval: 120s
    filters:
      labels: [ai-ready]

agents:
  - id: backend-dev
    description: "Implements backend tasks"
    runner: claude
    model: claude-sonnet-4-6

# A workflow fires when its trigger matches a task, then runs its steps.
workflows:
  - id: backend-bugs
    trigger:
      priority: 10        # lower number = evaluated first
      match:
        labels: [backend, bug]
    steps:
      - id: run
        agent: backend-dev
    on_complete:
      set_state: closed
```

`apiary run` starts the daemon. That's the whole thing.

Because each agent is just "runner + model", routing different work to different *models* is
a one-line change — cheap model for triage, frontier model for the hard design tasks. And
because the config is validated against a published JSON Schema, the VS Code extension
(**Apiary Workflow Visualizer**) draws a live flowchart of your workflows and red-squiggles
mistakes as you type.

## More than one agent per task

A workflow isn't limited to a single step. Steps can fan out, gate on manual approval, or
spawn sub-workflows — so you can encode a real pipeline: an *investigator* classifies an
incoming issue, hands it to a *staff engineer* to design, which decomposes it for an
*engineer*, then a *reviewer* and *QA* close the loop. Each step is its own agent, with its
own model.

> _[screenshot: docs/screenshots/tasks.png — tasks flowing through workflows]_

## Built to run unattended

This is the part that separates "a fun demo" from "something you leave running." When you
hand real work to autonomous agents against metered LLM APIs, the failure modes are
expensive. `v0.2.0` is the release where we hardened all of them:

- **Rate-limit failover.** When a provider rejects a run on a usage limit (e.g. Claude's
  5-hour session limit), Apiary doesn't pretend the empty run succeeded. It pauses that
  runner type until the limit resets, and fails over to the agent's next `fallbacks`
  entry — a different runner or model — so work keeps moving instead of stalling or
  burning a pre-failed call.
- **Re-dispatch cap.** A task whose workflow keeps failing would otherwise be re-dispatched
  on every poll, forever. `settings.max_attempts` stops it after N consecutive failures and
  applies the workflow's `on_fail` hook. Rate-limited runs don't count against it; a single
  success resets it.
- **Non-blocking dispatch.** A fully-busy agent parks its own runs in its own goroutine,
  without stalling polling or dispatch for any other source or agent.
- **Approval rehydration.** Workflows can pause on a manual-approval gate. Those parked runs
  now survive a daemon restart — restart no longer strands them.
- **Scoped environment variables.** Set env vars per agent, per workflow, or per step, with
  a clear precedence order.

These aren't headline features you'll click on. They're the boring reliability work that
lets you actually trust the thing while you sleep.

## Watch it work

Apiary ships a terminal dashboard — tasks, agents, and logs in real time, with live
markdown rendering of agent output.

> _[screenshot: docs/screenshots/agents.png and docs/screenshots/logs.png]_

```sh
apiary dashboard
```

## Install in 30 seconds

```sh
# macOS / Linux — Homebrew
brew install --cask orlandoburli/tap/apiary

# Windows — Scoop
scoop bucket add orlandoburli https://github.com/orlandoburli/scoop-bucket
scoop install apiary

# Docker (multi-arch: amd64 + arm64)
docker run --rm -v apiary-data:/data ghcr.io/orlandoburli/apiary:latest version
```

`.deb`/`.rpm` packages and signed-checksum archives are on the
[releases page](https://github.com/orlandoburli/apiary/releases/latest).

## It's a beta — and that's the invitation

Apiary works end-to-end and is in active use, but it's beta on purpose: the config and
adapter APIs may still shift before `v1.0`. If you run agents against your backlog, I want
your rough edges — the source you wish existed, the routing rule that didn't express what
you meant, the dashboard column that's missing.

- ⭐ Star and try it: **https://github.com/orlandoburli/apiary**
- 🐛 File issues for anything that bites
- 📦 Dual-licensed: AGPLv3 for open-source/internal use, commercial license available for
  proprietary/SaaS deployments

Stop being the dispatcher. Let the work route itself.
