# Design: visual workflow editor backed by YAML

## Document model and round trips

The extension replaces value-only `js-yaml` parsing with the `yaml` document
model. Nodes retain comments, ordering, scalar style, and source ranges. Edits
address a precise workflow/step path and produce a candidate document; saving is
always an atomic VS Code `WorkspaceEdit` over the original text document.

Support analysis is allow-list based at workflow, trigger, step, branch, and
outcome levels. Unknown keys, aliases, custom tags, or unsupported node shapes
make the affected workflow read-only. The raw file remains viewable and CLI
actions remain available. The editor never converts an unsupported subtree to a
plain JavaScript object and writes it back.

## Shared schema and diagnostics

`schema/apiary.json` is bundled into the extension and evaluated by Ajv. Schema
instance paths are resolved back to YAML nodes and line/column ranges. A second
semantic pass validates IDs and references among sources, runners, agents,
steps, branch targets, retry targets, and subworkflows, plus basic expression
shape. Diagnostics carry workflow and step IDs so the webview can decorate the
relevant node as well as show the YAML location.

The extension's checks are immediate authoring feedback. `apiary validate`
remains authoritative and is exposed as an editor action against the saved
candidate.

## Editor interaction

The existing per-workflow Mermaid view remains the graph. A workflow selector,
mode toggle, node palette, node inspector, and connection/outcome fields provide
authoring without persisting layout metadata. Clicking a diagram node selects
its inspector. Adding a node inserts a supported step; deleting and reordering
are explicit operations. Branch and retry connections are edited as referenced
step IDs, which map directly to YAML semantics.

Every edit remains an in-webview candidate until Save. The preview tab shows the
candidate YAML and semantic diff (added, removed, changed paths). Save is
disabled for unsupported workflows or validation errors and requires an explicit
confirmation message from the webview.

## CLI actions

Validate and dry-run messages are handled by the extension host using
`child_process.execFile`, never a shell. The executable path is configurable and
defaults to `apiary`. Output is returned to the panel and surfaced in an output
channel. Dry-run first saves the approved candidate, then invokes the same
`apiary run --dry-run --config <path>` path documented by the CLI.
