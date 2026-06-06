# Specification: Runner Interface Changes

Update `RunRequest` and `RunResult` to support workflow memory injection, summary extraction, and structured output parsing.

## Current Interface

```go
type RunRequest struct {
    Cell         Cell
    WorkerID     string
    Model        string
    MaxTurns     int
    SystemAppend string
    WorkingDir   string
    Env          map[string]string
    Timeout      time.Duration
}

type RunResult struct {
    WorkerID string
    Success  bool
    Output   string
    Logs     []LogEntry
    Duration time.Duration
    Error    error
}
```

## Updated Interface

```go
type RunRequest struct {
    Cell              Cell
    WorkerID          string          // kept for backward compat; set to agent ID in workflow mode
    Model             string
    MaxTurns          int
    SystemPrepend     string          // NEW: injected before soul file (workflow memory block)
    SystemAppend      string          // unchanged: soul file content + step prompt override
    SummaryPrompt     string          // NEW: instruction appended at end of prompt; agent produces summary
    WorkingDir        string
    Env               map[string]string
    Timeout           time.Duration
    StepID            string          // NEW: step ID within the workflow (empty for plain routes)
    WorkflowInstanceID string         // NEW: instance ID for logging/tracing (empty for plain routes)
}

type RunResult struct {
    WorkerID         string
    Success          bool
    Output           string          // unchanged: full raw stdout (stored in SQLite, not forwarded)
    StructuredOutput map[string]any  // NEW: parsed from APIARY_OUTPUT: last line (nil if absent)
    Summary          string          // NEW: extracted summary block (empty if no SummaryPrompt)
    Logs             []LogEntry
    Duration         time.Duration
    Error            error
}
```

## Field Semantics

### `RunRequest.SystemPrepend`

Prepended to the agent's full system prompt before everything else — before the soul file, before `SystemAppend`, before the step `prompt` override. Contains the formatted workflow memory document:

```
=== Workflow Memory ===
...
======================
```

Empty string for plain routes (no memory to inject).

### `RunRequest.SummaryPrompt`

When non-empty, the runner appends this instruction at the very end of the system prompt, after `SystemAppend`. The runner is responsible for instructing the agent to emit its summary in a recognizable format.

The runner appends the following to the prompt:

```
---
When you are done, write a brief summary of your work using this exact format:

APIARY_SUMMARY_START
[your summary here — 3-5 bullet points]
APIARY_SUMMARY_END
```

The engine extracts the content between the markers and stores it as `RunResult.Summary`.

### `RunResult.StructuredOutput`

Parsed from the `APIARY_OUTPUT: {...}` sentinel on the last line of stdout. The runner strips this line from `Output` before storing it — it should not appear in the human-readable output. If no sentinel is found, `StructuredOutput` is `nil`.

Parsing is the runner's responsibility — it knows the stdout format of its underlying tool. Apiary validates the parsed object against `output_schema` after receiving the `RunResult`.

### `RunResult.Summary`

Extracted from the `APIARY_SUMMARY_START` / `APIARY_SUMMARY_END` markers in stdout. The runner strips these markers from `Output`. If no markers are found (agent did not follow the instruction), `Summary` is empty. The engine stores an empty summary without error — `SummaryPrompt` is best-effort.

## Runner Adapter Responsibilities

Each runner must:

1. Inject `SystemPrepend` at the start of the system prompt it passes to the underlying tool.
2. Append the `SummaryPrompt` instruction block at the end of the system prompt when `SummaryPrompt` is non-empty.
3. Scan stdout for `APIARY_OUTPUT:` on the last non-empty line; parse the JSON and set `StructuredOutput`; remove the line from `Output`.
4. Scan stdout for `APIARY_SUMMARY_START` / `APIARY_SUMMARY_END` markers; extract the content and set `Summary`; remove the markers from `Output`.

Steps 3 and 4 are post-processing on the raw stdout — no changes to how the runner invokes the underlying CLI.

## Backward Compatibility

`SystemPrepend` and `SummaryPrompt` are empty strings for plain routes (no workflow). Runners that do not yet implement steps 1–4 continue to work — they just never produce `StructuredOutput` or `Summary`. The engine treats both as absent (nil / empty) and logs a debug message if `output_schema` was declared but `StructuredOutput` is nil.

`WorkerID` is preserved for all existing log messages and SQLite records. In workflow mode it is set to the agent ID. No callers need to change.

## `script` Runner: Environment Variables

The script runner exposes the new fields as environment variables:

| Variable | Value |
|---|---|
| `APIARY_SYSTEM_PREPEND` | `RunRequest.SystemPrepend` |
| `APIARY_SUMMARY_PROMPT` | `RunRequest.SummaryPrompt` |
| `APIARY_STEP_ID` | `RunRequest.StepID` |
| `APIARY_WORKFLOW_INSTANCE_ID` | `RunRequest.WorkflowInstanceID` |

The script is responsible for parsing `APIARY_OUTPUT:` and `APIARY_SUMMARY_START/END` from its own output if it wants to use structured output or summaries.
