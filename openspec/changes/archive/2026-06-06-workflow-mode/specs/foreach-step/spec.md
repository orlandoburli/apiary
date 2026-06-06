# Specification: Foreach Step

Enable dynamic fan-out over a list produced by a prior step, spawning one sub-run per item.

## Problem

Workflow steps are declared statically. If an analysis agent returns a list of files to fix, or a triage agent returns a list of sub-tasks, there is no way to spawn one agent per item without pre-declaring every branch. The number of items is only known at runtime.

## Schema

```yaml
steps:
  - id: find-issues
    agent: analyzer
    output_schema:
      type: object
      properties:
        issues:
          type: array
          items:
            type: object
            properties:
              file: {type: string}
              description: {type: string}
            required: [file, description]
      required: [issues]

  - id: fix-each
    type: foreach
    depends_on: find-issues
    items: "steps.find-issues.output.issues"   # dot-path to the array
    as: issue                                   # variable name inside the step
    concurrency: 4                              # max parallel sub-runs (default: 2)
    max_items: 20                               # safety cap; fail if exceeded (default: 50)
    step:
      agent: backend-dev
      prompt: |
        Fix the issue in file {{ issue.file }}:
        {{ issue.description }}
      output_schema:
        type: object
        properties:
          fixed: {type: boolean}
          summary: {type: string}
        required: [fixed]

  - id: summarize
    agent: docs-writer
    depends_on: fix-each
    prompt: |
      All issues have been processed. Write a summary of what was fixed.
```

## How It Works

1. When `fix-each` is ready to run, the engine reads `steps.find-issues.output.issues`.
2. It creates one **foreach sub-run** per item. Each sub-run is a scoped agent invocation with:
   - The item value bound to the `as` variable (`issue` in the example)
   - The step's `prompt` rendered with `{{ issue.<field> }}` template substitution
   - Its own entry in `step_runs` keyed as `fix-each[0]`, `fix-each[1]`, etc.
3. Sub-runs execute concurrently up to `concurrency`.
4. `fix-each` is marked `passed` when all sub-runs pass; `failed` if any sub-run fails (configurable with `fail_fast`).
5. Downstream steps (`summarize`) see `fix-each` as a single step dependency.

## Output of a Foreach Step

The foreach step exposes its sub-run outputs as an array:

```
steps.fix-each.outputs          # array of each sub-run's output object
steps.fix-each.outputs[0].fixed # first sub-run's structured output field
steps.fix-each.passed_count     # number of sub-runs that passed
steps.fix-each.failed_count     # number that failed
```

In expressions (conditions/prompts), array indexing (`[0]`) is supported only for foreach outputs in v1.

## Configuration Fields

| Field | Type | Default | Description |
|---|---|---|---|
| `items` | string | required | Dot-path to an array in a prior step's structured output |
| `as` | string | `item` | Variable name for the current item in prompt templates |
| `concurrency` | int | `2` | Max parallel sub-runs |
| `max_items` | int | `50` | Abort if the array exceeds this length |
| `fail_fast` | bool | `false` | Fail the foreach step as soon as one sub-run fails |
| `step` | StepConfig | required | The agent step definition to replicate per item |

## Prompt Templating

Inside a `foreach` step's `prompt`, the `as` variable is available as a Go template:

```
Fix {{ issue.file }}: {{ issue.description }}
```

String fields are supported. Nested objects are rendered as JSON. The full cell and prior step context is also available as usual.

## Constraints

1. `items` must reference a step that declares `output_schema` with the target field as an array. Referencing unstructured output is not supported.
2. Nested `foreach` steps (a `foreach` inside a `foreach`) are not supported in v1.
3. `max_items` is a hard cap — if the array length exceeds it, the foreach step fails immediately. This prevents runaway fan-out.
4. The `step` inside a `foreach` cannot be of type `split`, `approval`, or `foreach`.

## Validation

- `items` dot-path must resolve to an `array` type in the referenced step's `output_schema`.
- `concurrency` must be between 1 and 16.
- `max_items` must be between 1 and 200.
