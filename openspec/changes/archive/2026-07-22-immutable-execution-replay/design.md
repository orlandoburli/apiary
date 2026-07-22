# Design

## Immutable descendants

The workflow engine creates a fresh `workflow_instances` row for resume and sets `resumed_from` to the source instance. Passed steps eligible for reuse are copied to new `step_runs` rows with new IDs and `skipped_cached = true`. The original instance and step rows are never updated.

## Resume point

Without `--from`, all compatible passed steps are reused. With `--from`, only passed steps declared before the selected step are reused; the selected step and all later declared steps execute again. Split steps are always re-evaluated because they are side-effect free and activate branches.

## Workflow snapshots

Snapshots live in a separate `workflow_instance_snapshots` table keyed by instance ID. This avoids widening the high-fan-out workflow-instance scan contract. Normal and resumed runs record the structural `WorkflowConfig` used. Workflow and step environment overlays are deliberately omitted because config expansion may place secrets in those maps; original-definition replay resolves those overlays from the current configuration.

## Comparison

The daemon builds comparison data from two instance details. Steps are aligned by step ID. Repeated loop executions use the last row for change detection and aggregate usage already persisted on the logical step row.
