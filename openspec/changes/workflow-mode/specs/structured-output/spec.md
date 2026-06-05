# Specification: Structured Output

Enable agent steps to emit typed JSON that the workflow engine can read and expose in conditions, replacing fragile string matching.

## Problem

The current expression language can only match against the raw stdout string of an agent step (`steps.<id>.output contains "COMPLEX"`). This is brittle — the agent must emit a specific sentinel word, and any formatting change breaks the branch.

## Approach: Opt-In Last-Line JSON Contract

Apiary does not parse the body of agent output (preserving ADR-006). Instead, a runner that supports structured output emits a single JSON object as the **last line** of stdout, preceded by a sentinel prefix:

```
APIARY_OUTPUT: {"complexity":"high","action":"implement","confidence":0.9}
```

Everything before this line is treated as human-readable log output and stored as-is. If no `APIARY_OUTPUT:` line is present, the step is treated as unstructured (existing behavior).

Runners opt in — Apiary never requires it. Steps that declare `output_schema` and receive unstructured output fail validation at runtime (configurable: `on_missing_output: fail | warn | ignore`).

## Schema

```yaml
steps:
  - id: analyze
    agent: architect
    output_schema:
      type: object
      properties:
        complexity:
          type: string
          enum: [low, medium, high]
        action:
          type: string
          enum: [implement, escalate, reject]
        confidence:
          type: number
      required: [complexity, action]
    on_missing_output: warn   # default: warn

  - id: route
    type: split
    depends_on: analyze
    branches:
      - if: "steps.analyze.output.complexity == 'high'"
        goto: senior-dev
      - if: "steps.analyze.output.action == 'reject'"
        goto: close-task
      - else: true
        goto: standard-dev
```

## Expression Access

When a step has `output_schema` and the output was successfully parsed, its fields are accessible in conditions as:

```
steps.<id>.output.<field>        # top-level field
steps.<id>.output.<field>.<sub>  # nested field
```

Dot-path access on arrays is not supported in v1 (use `contains` on the raw string for array membership checks).

Unstructured steps continue to expose `steps.<id>.output` as the full stdout string.

## Validation

1. `output_schema` must be a valid JSON Schema (subset: `object`, `string`, `number`, `boolean`, `enum`, `required`). Arrays and `$ref` are not supported in v1.
2. At runtime, if the last line matches `APIARY_OUTPUT:` but the JSON fails schema validation, the step fails.
3. Conditions referencing `steps.<id>.output.<field>` on a step without `output_schema` fail config validation.
4. Conditions referencing `steps.<id>.output` (no field path) on a step with `output_schema` return the raw string (backward compatible).

## Runner Responsibility

The runner adapter is responsible for instructing the underlying agent to emit the `APIARY_OUTPUT:` line. For the OpenCode runner, this becomes an append to `system_prompt_append`:

```
When you are done, output the following as the last line of your response:
APIARY_OUTPUT: <JSON matching this schema: {schema}>
```

This prompt injection is automatic when the step declares `output_schema`. The runner adapter generates it; the agent author does not need to know about the sentinel format.

## Compatibility

Steps without `output_schema` are unaffected. The `APIARY_OUTPUT:` prefix is chosen to be unlikely to appear in normal agent output. If it does appear in the middle of output (not the last line), it is ignored.
