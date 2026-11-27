# Agent questions (`ask_human`)

A running agent can ask you a question and wait for the answer, mid-run —
"which of these three approaches do you want?", "the ticket does not say which
environment; staging or production?".

This is not an approval step. An approval gate sits *between* steps in a
workflow, declared ahead of time. A question is asked by the agent itself,
while it runs, when it discovers it needs something only a person knows.

## How it works

An agent process cannot be paused from outside: its stdin is a one-shot prompt,
and apiary only reads its output markers once it exits. The one thing an agent
can do in the middle of a turn is call a tool, so apiary ships one.

`apiary mcp-serve` is a small MCP server that apiary registers with the agent
CLI automatically. It exposes a single tool:

```
ask_human(question, options?, allow_free_text?)
```

When the agent calls it:

1. The tool posts the question to the daemon.
2. The agent blocks on the tool call.
3. The question appears in the dashboard. You answer it.
4. The tool returns your answer and the agent carries on in the same run.

If nobody answers before the question expires (30 minutes by default, capped at
24 hours), the tool returns "no answer" as a normal result. The agent continues
rather than hanging — a question that is never answered should not strand a run.

## Using it

Nothing to configure in a workflow. Any agent run whose CLI supports MCP gets
the tool, as long as the setting is on:

```yaml
settings:
  questions:
    enabled: true   # default
```

Supported runners are the ones apiary already writes MCP config for: Claude,
Cursor, OpenCode and Codex. A runner with no MCP support simply does not get
the tool; its runs are unaffected.

Turn the whole feature off with `settings.questions.enabled: false` — no MCP
entry is injected and no questions can be created.

## Answering

Questions appear in the dashboard on their own, on whichever tab you are
looking at, unless you are already in the middle of something (a form, a
filter, a picker) — then a `? N question(s)` hint in the footer lets you open
the next one.

The sheet shows the question, the answers it offers (if any), a free-text
answer, and an optional note. `enter` submits, `esc` puts it aside: the question
stays pending — the agent is still blocked on it — and `?` reopens it. It does
not reopen by itself, so postponing it actually gets it out of your way.

## Endpoints

The agent channel is the daemon's unix socket. It is the same trust boundary as
the rest of the dashboard and CLI: the socket directory is `0700`, and anyone
who can reach it can already control the daemon.

| Method | Path | Used by |
|---|---|---|
| `POST` | `/questions` | the MCP server, to ask |
| `GET` | `/questions/{id}?wait=20` | the MCP server, to wait for the answer |
| `POST` | `/questions/{id}/answer` | the dashboard |
| `GET` | `/questions?status=pending` | listing |

Answers are validated against the question's declared options (case-insensitive,
trimmed). A question that only offers options refuses a free-text answer; one
that allows free text accepts anything.

Waiting is a long poll: the endpoint re-reads the question row every 250 ms
until it is answered or the wait expires. There is no push channel, and for a
human-driven flow there is no reason to build one — a few hundred milliseconds
of latency costs less than another lifecycle to keep correct.
