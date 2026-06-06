# Apiary Workflow Visualizer

Live visual preview of [apiary](https://orlandoburli.com.br/apiary/) workflow YAML files, rendered as a Mermaid flowchart directly inside VS Code or Cursor.

## Features

- **Auto-opens** a side panel whenever `apiary.yaml` is the active editor
- **Live re-render** on every keystroke (350 ms debounce) — no save required
- **One diagram per workflow**, showing:
  - Source node → trigger edge with label/state filter info
  - Agent steps (blue) with agent name and model
  - Split steps (purple diamond) with labelled branch edges
  - Approval steps (amber)
  - Dashed retry back-edges for `reject_when` / `on_reject` loops
- **Dark and light theme** aware
- Command palette: **Apiary: Show Workflow Preview**

## Usage

Open any `apiary.yaml` file — the preview panel appears automatically on the side.

You can also trigger it manually via the editor title bar icon or:

```
Cmd+Shift+P → Apiary: Show Workflow Preview
```

## About Apiary

[Apiary](https://orlandoburli.com.br/apiary/) is an open-source AI agent orchestration system that dispatches work to Claude, OpenCode, and other LLM runners based on GitHub Issues, Plane tasks, and other sources.

- 🌐 Website: https://orlandoburli.com.br/apiary/
- 📦 Source: https://github.com/orlandoburli/apiary
