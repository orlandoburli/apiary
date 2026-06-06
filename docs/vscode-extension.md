# VS Code Extension

**Apiary Workflow Visualizer** is a VS Code / Cursor extension that renders a
live diagram of your `apiary.yaml` while you edit it. As you write workflows,
a panel on the side draws each one as a flowchart — sources, triggers, steps,
splits, and retry loops — and re-renders on every keystroke.

It is a read-only visual aid: it never modifies your config, it only reads the
file in the active editor and draws it.

## Install

From the VS Code / Cursor Marketplace:

```
ext install orlandoburli.vscode-apiary
```

Or search **"Apiary Workflow Visualizer"** in the Extensions panel.

!!! note "Cursor"
    Cursor uses the same extension format. Install the VSIX or search the
    Marketplace exactly as you would in VS Code.

## Usage

Open any `apiary.yaml` (or a `*.apiary.yaml`) file. The preview panel opens
automatically on the side and tracks the active editor.

You can also open it manually:

- Click the **diagram icon** in the editor title bar, or
- Run **Apiary: Show Workflow Preview** from the command palette
  (`Cmd+Shift+P` / `Ctrl+Shift+P`).

The diagram updates as you type — there is a short (~350 ms) debounce, so you
do not need to save. If the YAML is temporarily invalid while you edit, the
panel shows the parse error instead of a stale diagram and recovers as soon as
the file is valid again.

## What the diagram shows

The extension draws **one flowchart per workflow** defined in the file. Each
node and edge maps directly to your config:

| Element | Meaning |
|---|---|
| 📦 **Source node** (green) | The `trigger.match.source` the workflow listens to |
| **Trigger edge** | Labelled with the match filters (labels, states, type, excluded prefixes) |
| **Agent step** (blue) | A normal step — shows the step id, its `agent`, and the agent's model |
| **Split step** (purple diamond) | A `type: split` step; each branch edge is labelled with its condition (or `else`) |
| **Approval step** (amber) | A `type: approval` gate |
| **Dashed back-edge** | A retry loop from `reject_when` / `on_reject`, labelled with the `max` count |

The panel follows your editor theme — both light and dark themes are
supported.

### Example

Given a `triage` workflow that classifies an issue and routes it to one of
several agents:

```yaml
workflows:
  - id: triage
    trigger:
      match:
        source: project-erp
        exclude_label_prefix: "agent:"
    steps:
      - id: classify
        agent: investigator
      - id: route
        type: split
        branches:
          - if: 'memory.agent == "po"'
            goto: spec
          - else: true
            goto: implement
      - id: spec
        agent: po
      - id: implement
        agent: engineer
```

…the extension renders the source feeding into `classify`, a diamond for
`route` with one labelled edge per branch, and the agent/model on each step —
so you can see the routing at a glance instead of tracing `goto` targets by
hand.

## Limitations

- The diagram is **static** — there is no click-to-navigate or drag. It is a
  reading aid for authoring, not an interactive editor.
- Mermaid lays the graph out automatically; workflows with many branches can
  produce a dense layout. Scroll the panel horizontally for wide graphs.
- Only `workflows:` are drawn. The `runners:`, `sources:`, and `agents:`
  blocks are read to label nodes but are not diagrammed on their own.

## Source & issues

The extension lives in the Apiary repository under
[`tools/vscode-apiary`](https://github.com/orlandoburli/apiary/tree/main/tools/vscode-apiary).
Report bugs or request features on the
[issue tracker](https://github.com/orlandoburli/apiary/issues).
