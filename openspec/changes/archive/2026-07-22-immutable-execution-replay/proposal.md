# Immutable execution replay

## Why

`apiary resume` currently mutates the failed or interrupted workflow instance and marks its previously passed step rows as cached. That loses the immutable record of the original attempt and prevents reliable before/after comparison.

## What changes

- Every resume creates a new workflow instance linked through `resumed_from`.
- Passed step outputs selected for reuse are copied into the descendant attempt and marked cached; source rows remain unchanged.
- Workflow definitions are snapshotted per instance so operators may replay the original definition or the current configuration.
- `--from <step>` selects the first step to execute again; `--definition`
  selects `current` or `original` without conflicting with the global config-file flag.
- `apiary instances compare <a> <b>` compares step state, input/output changes, usage, cost, runner, model, and timing.
- Existing routing dry-run remains the supported no-run route/condition preview.

## Compatibility

The default configuration mode remains `current` for compatibility. Instances created before workflow snapshots exist can still resume with current configuration; requesting `original` returns an actionable error.
