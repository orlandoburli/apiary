# Reusable typed subworkflows

## Why

Common workflow sequences are currently either duplicated or declared as another
workflow in the same `apiary.yaml`. The existing child-instance engine provides
execution isolation, but it has no reusable file boundary, declared contract, or
explicit output mapping.

## What changes

- Allow a workflow step to reference a local YAML workflow through `uses`.
- Let reusable workflows declare typed inputs, defaults, required values, and
  explicitly mapped outputs.
- Resolve files relative to the declaring file and reject missing files,
  duplicate workflow IDs, invalid contracts, and recursive reference cycles.
- Execute nested workflows as linked child instances while recording the parent
  call step and returning declared child outputs to downstream callers.
- Propagate context cancellation, timeout, failure, logs, usage history, and cost
  visibility through the existing linked execution tree.

## Scope

The first version supports local `.yaml` and `.yml` files. Remote registries and
versioned workflow packages remain future extensions of the same `uses` syntax.

## Tracking

Closes GitHub issue #210.
