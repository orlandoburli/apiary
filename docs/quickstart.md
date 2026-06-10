# Quickstart

This guide takes you from a fresh install to your first autonomously handled
GitHub issue in about ten minutes. By the end, Apiary will be polling a
repository, dispatching labeled issues to a Claude agent, and posting the
result back as a comment.

## Prerequisites

- **Apiary installed** — see [Installation](installation.md). Verify with
  `apiary version`.
- **An agent CLI installed and authenticated.** This guide uses the
  [Claude CLI](https://docs.anthropic.com/en/docs/claude-code) (`claude` on
  your `PATH`). Apiary never handles credentials — the CLI tool manages its
  own authentication.
- **A GitHub repository** you can create issues in, and a
  [personal access token](https://github.com/settings/tokens?type=beta) with
  `Issues: Read & Write` (fine-grained) or `repo` scope (classic).

## 1. Create the config

Apiary is project-scoped: it reads `apiary.yaml` from the directory you run it
in (or `.apiary/apiary.yaml`), and keeps its state in a `.apiary/` folder next
to it. Run it from the repository you want the agent to work on.

Create `apiary.yaml`:

```yaml
version: "1"

runners:
  # How agents execute — here, the local `claude` CLI as a subprocess.
  # The preset asks Claude for structured events, so the dashboard can show
  # the live conversation (messages, tool calls, cost).
  - id: claude
    type: cli
    provider: claude

sources:
  # Where work comes from — GitHub issues carrying the `ai-ready` label.
  - id: my-repo
    type: github
    config:
      repo: my-org/my-repo          # ← change this
      api_key: ${GITHUB_TOKEN}
    poll_interval: 120s
    filters:
      states: [open]
      labels: [ai-ready]

agents:
  # A named persona: runner + model (+ optional soul file and skills).
  - id: engineer
    description: "Implements tasks following project conventions"
    runner: claude
    model: claude-sonnet-4-6

workflows:
  # A workflow fires when its trigger matches a task, then runs its steps.
  - id: implement
    trigger:
      priority: 10                  # lower number = evaluated first
      match:
        source: my-repo
    steps:
      - id: run
        agent: engineer
    on_complete:
      set_state: closed             # close the issue when the run succeeds

settings:
  log_level: info
  state_lock: true                  # label the issue `in-progress` while running
  result_comment: true              # post the agent's output back as a comment
```

## 2. Provide the token

Apiary auto-loads a `.env` file from the config directory at startup
(already-exported shell variables take priority). Create `.env` next to
`apiary.yaml`:

```bash
GITHUB_TOKEN=github_pat_...
```

!!! warning
    Add `.env` and `.apiary/` to your `.gitignore` — the token must not be
    committed, and `.apiary/` holds the local database, logs, and socket.

## 3. Validate

```sh
apiary validate                 # schema + reference checks
apiary validate --connectivity  # also test that the GitHub source connects
```

Fix anything it reports before starting the daemon. The
[VS Code extension](vscode-extension.md) gives you the same validation live
while you edit.

## 4. Create a task

Open an issue in your repository describing something small and well-scoped —
a good first test is a README tweak or a tiny refactor — and add the
**`ai-ready`** label. The label is your opt-in switch: Apiary only sees issues
that pass the source `filters`.

## 5. Run

```sh
apiary run
```

In a second terminal, watch it work:

```sh
apiary dashboard
```

Within one poll interval you'll see the cycle complete:

1. The poller picks up the labeled issue and the trigger matches it to the
   `implement` workflow.
2. The issue gets an `in-progress` label (`state_lock`).
3. The `engineer` agent runs in a `claude` subprocess; the dashboard's
   **Tasks** tab shows it live.
4. The agent's output is posted back to the issue as a comment
   (`result_comment`), and the issue is closed (`on_complete.set_state`).

Useful variations:

```sh
apiary run --debug    # record the full agent conversation in the task logs
apiary run --once     # poll once, process everything pending, then exit
apiary run --dry-run  # poll and match, but never invoke a runner
```

To keep Apiary running permanently, install it as a system service
(systemd / launchd / Windows Service):

```sh
apiary service install
apiary service start
```

## Where to go next

- [Concepts](concepts.md) — the vocabulary and lifecycle behind what you just
  ran.
- [Configuration](configuration.md) — every field of `apiary.yaml`: runners,
  sources, agents, settings, environment variables.
- [Workflows](workflows.md) — multi-step pipelines: classification, branching,
  approval gates, CI waits, retry loops.
- [Rate limits & resilience](resilience.md) — fallback chains and the
  safeguards that make unattended dispatch safe to leave running.
- [Dashboard](dashboard.md) — everything the terminal UI shows you.
