# Design: reusable typed subworkflows

## Authoring contract

A reusable file contains one workflow definition at its root. Inputs and outputs
use a small JSON-compatible type set: `string`, `number`, `integer`, `boolean`,
`array`, and `object`.

```yaml
id: prepare-repository
inputs:
  repository:
    type: string
    required: true
outputs:
  workspace:
    type: string
    value: ${{ steps.checkout.workspace }}
steps:
  - id: checkout
    agent: engineer
    prompt: Clone ${{ inputs.repository }}.
    output:
      type: object
      properties:
        workspace: {type: string}
      required: [workspace]
```

Callers use a local path and bind values explicitly:

```yaml
- id: prepare
  uses: ./workflows/prepare-repository.yaml
  with:
    repository: ${{ task.repository }}
```

## Loading and validation

`config.Load` retains its public signature. After decoding the primary config it
recursively loads referenced local files, assigns the resolved child workflow ID
to the existing engine IR field, and appends each canonical file once. A DFS over
canonical paths reports file cycles during loading; a second DFS over workflow
IDs protects configs assembled directly in Go.

Strict YAML decoding is applied to reusable files. Relative references resolve
from the file that declares them. Extensionless references try `.yaml` and
`.yml`. Duplicate IDs from different files are rejected.

## Runtime data flow

The caller's `with` values are resolved from literals, task/cell fields, workflow
memory, or structured outputs of earlier steps. Resolved inputs are type-checked,
defaults are applied, and the child receives them as an isolated seed contribution.
Child prompts render `${{ inputs.<name> }}`.

After the child completes, each declared output expression is resolved from the
child's step contributions. The resulting map becomes the structured output of
the parent call step, making it available to later `with` bindings without
leaking the child's entire memory.

## History and lifecycle

The parent call is persisted as a normal step run. The child remains a linked
`workflow_instance` through `parent_instance_id`, and its internal step runs keep
their individual logs and usage/cost records. A child failure fails the call
step. The parent's context is passed into the child; cancellation and an optional
call timeout cancel the child and mark both the child instance and call step as
failed. A depth guard protects runtime execution even after validation.
