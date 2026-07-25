import * as vscode from 'vscode';
import { parseApiary, validateConfig, classifyStepEditability, ValidationError } from './parser';
import { serializeApiary } from './serializer';
import { computeDiff } from './diff';

function getNonce(): string {
  let text = '';
  const possible = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
  for (let i = 0; i < 32; i++) {
    text += possible.charAt(Math.floor(Math.random() * possible.length));
  }
  return text;
}

export function isApiaryYaml(document: vscode.TextDocument): boolean {
  if (document.languageId !== 'yaml') { return false; }
  const name = document.fileName.split(/[\\/]/).pop() ?? '';
  return name === 'apiary.yaml' || name.endsWith('.apiary.yaml');
}

// EditorMessage types sent from webview → extension host
type WebviewToExtMsg =
  | { type: 'ready' }
  | { type: 'save'; yaml: string }
  | { type: 'requestYaml' }
  | { type: 'validate' };

// EditorMessage types sent from extension host → webview
type ExtToWebviewMsg =
  | { type: 'init'; payload: EditorPayload }
  | { type: 'validationResult'; errors: ValidationError[] }
  | { type: 'saveResult'; success: boolean; error?: string };

export interface EditorPayload {
  originalYaml: string;
  config: unknown;          // ApiaryConfig (serialized as JSON)
  editability: Record<string, string>;   // stepId → StepEditability
  errors: ValidationError[];
  currentYaml: string;      // serialized from model (may differ from originalYaml due to round-trip)
}

export class ApiaryEditorPanel {
  private static current: ApiaryEditorPanel | undefined;

  private readonly panel: vscode.WebviewPanel;
  private document: vscode.TextDocument;
  private disposables: vscode.Disposable[] = [];
  private debounceTimer: ReturnType<typeof setTimeout> | undefined;

  static show(document: vscode.TextDocument): void {
    if (ApiaryEditorPanel.current) {
      ApiaryEditorPanel.current.panel.reveal(vscode.ViewColumn.Beside, true);
      ApiaryEditorPanel.current.setDocument(document);
      return;
    }
    const panel = vscode.window.createWebviewPanel(
      'apiaryEditor',
      'Apiary Editor',
      { viewColumn: vscode.ViewColumn.Beside, preserveFocus: false },
      {
        enableScripts: true,
        retainContextWhenHidden: true,
        localResourceRoots: [],
      },
    );
    ApiaryEditorPanel.current = new ApiaryEditorPanel(panel, document);
  }

  static get isOpen(): boolean { return ApiaryEditorPanel.current !== undefined; }

  private constructor(panel: vscode.WebviewPanel, document: vscode.TextDocument) {
    this.panel = panel;
    this.document = document;
    this.panel.webview.html = this.buildHtml();

    this.panel.webview.onDidReceiveMessage((msg: WebviewToExtMsg) => {
      switch (msg.type) {
        case 'ready':
          this.sendPayload();
          break;
        case 'save':
          void this.applySave(msg.yaml);
          break;
        case 'requestYaml':
          this.sendPayload();
          break;
        case 'validate':
          this.sendValidation();
          break;
      }
    }, null, this.disposables);

    // Re-parse on document edits (debounced)
    this.disposables.push(
      vscode.workspace.onDidChangeTextDocument(e => {
        if (e.document === this.document) {
          this.scheduleSendPayload();
        }
      }),
    );

    // Track active editor switches
    this.disposables.push(
      vscode.window.onDidChangeActiveTextEditor(editor => {
        if (editor && isApiaryYaml(editor.document)) {
          this.setDocument(editor.document);
        }
      }),
    );

    this.panel.onDidDispose(() => this.dispose(), null, this.disposables);
  }

  private setDocument(document: vscode.TextDocument): void {
    this.document = document;
    this.panel.title = `Apiary Editor — ${document.fileName.split(/[\\/]/).pop()}`;
    this.sendPayload();
  }

  private scheduleSendPayload(): void {
    if (this.debounceTimer !== undefined) { clearTimeout(this.debounceTimer); }
    this.debounceTimer = setTimeout(() => this.sendPayload(), 400);
  }

  private sendPayload(): void {
    const text = this.document.getText();
    try {
      const config = parseApiary(text);
      const errors = validateConfig(config);
      const editability: Record<string, string> = {};
      for (const wf of config.workflows ?? []) {
        for (const step of wf.steps ?? []) {
          editability[`${wf.id}::${step.id}`] = classifyStepEditability(step);
        }
      }
      const currentYaml = serializeApiary(config);
      const payload: EditorPayload = { originalYaml: text, config, editability, errors, currentYaml };
      const msg: ExtToWebviewMsg = { type: 'init', payload };
      void this.panel.webview.postMessage(msg);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      // Send parse error as a global validation error
      const msg: ExtToWebviewMsg = {
        type: 'init',
        payload: {
          originalYaml: text,
          config: null,
          editability: {},
          errors: [{ scope: 'workflow', message: `Parse error: ${message}` }],
          currentYaml: '',
        },
      };
      void this.panel.webview.postMessage(msg);
    }
  }

  private sendValidation(): void {
    const text = this.document.getText();
    try {
      const config = parseApiary(text);
      const errors = validateConfig(config);
      const msg: ExtToWebviewMsg = { type: 'validationResult', errors };
      void this.panel.webview.postMessage(msg);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      const msg: ExtToWebviewMsg = {
        type: 'validationResult',
        errors: [{ scope: 'workflow', message: `Parse error: ${message}` }],
      };
      void this.panel.webview.postMessage(msg);
    }
  }

  private async applySave(newYaml: string): Promise<void> {
    const editor = vscode.window.visibleTextEditors.find(e => e.document === this.document);
    try {
      if (editor) {
        const fullRange = new vscode.Range(
          this.document.positionAt(0),
          this.document.positionAt(this.document.getText().length),
        );
        await editor.edit(eb => eb.replace(fullRange, newYaml));
      } else {
        // Document is not open in an editor — write via workspace edit
        const we = new vscode.WorkspaceEdit();
        const fullRange = new vscode.Range(
          this.document.positionAt(0),
          this.document.positionAt(this.document.getText().length),
        );
        we.replace(this.document.uri, fullRange, newYaml);
        await vscode.workspace.applyEdit(we);
      }
      const msg: ExtToWebviewMsg = { type: 'saveResult', success: true };
      void this.panel.webview.postMessage(msg);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      const msg: ExtToWebviewMsg = { type: 'saveResult', success: false, error: message };
      void this.panel.webview.postMessage(msg);
    }
  }

  private buildHtml(): string {
    const nonce = getNonce();
    // The editor is a self-contained SPA embedded in the webview HTML.
    // It communicates with the extension host via postMessage.
    // No external CDN dependencies — all logic is inlined.
    return /* html */`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta http-equiv="Content-Security-Policy"
        content="default-src 'none';
                 script-src 'nonce-${nonce}';
                 style-src 'unsafe-inline';">
  <meta name="viewport" content="width=device-width,initial-scale=1.0">
  <title>Apiary Editor</title>
  <style>
    *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }

    :root {
      --bg: var(--vscode-editor-background, #1e1e1e);
      --fg: var(--vscode-editor-foreground, #d4d4d4);
      --border: var(--vscode-widget-border, #454545);
      --accent: var(--vscode-focusBorder, #007acc);
      --panel-bg: var(--vscode-sideBar-background, #252526);
      --input-bg: var(--vscode-input-background, #3c3c3c);
      --input-border: var(--vscode-input-border, #3c3c3c);
      --btn-bg: var(--vscode-button-background, #0e639c);
      --btn-fg: var(--vscode-button-foreground, #fff);
      --btn-hover: var(--vscode-button-hoverBackground, #1177bb);
      --error-bg: var(--vscode-inputValidation-errorBackground, #5a1d1d);
      --error-border: var(--vscode-inputValidation-errorBorder, #be1100);
      --error-fg: var(--vscode-errorForeground, #f48771);
      --warn-fg: var(--vscode-editorWarning-foreground, #cca700);
      --tag-agent: #1a3a6b;
      --tag-split: #3a1a6b;
      --tag-approval: #5a4a0a;
      --tag-wait: #1a4a3a;
      --tag-foreach: #3a2a0a;
      --tag-workflow: #2a1a4a;
      --tag-readonly: #3a3a3a;
    }

    body {
      background: var(--bg);
      color: var(--fg);
      font-family: var(--vscode-font-family, 'Segoe UI', sans-serif);
      font-size: var(--vscode-font-size, 13px);
      height: 100vh;
      display: flex;
      flex-direction: column;
      overflow: hidden;
    }

    /* ── Toolbar ── */
    #toolbar {
      display: flex;
      align-items: center;
      gap: 6px;
      padding: 6px 10px;
      border-bottom: 1px solid var(--border);
      background: var(--panel-bg);
      flex-shrink: 0;
    }
    #toolbar .wf-selector {
      flex: 1;
      max-width: 280px;
      background: var(--input-bg);
      color: var(--fg);
      border: 1px solid var(--input-border);
      border-radius: 3px;
      padding: 3px 6px;
      font-size: 12px;
    }
    #toolbar button {
      background: var(--btn-bg);
      color: var(--btn-fg);
      border: none;
      border-radius: 3px;
      padding: 4px 10px;
      font-size: 12px;
      cursor: pointer;
    }
    #toolbar button:hover { background: var(--btn-hover); }
    #toolbar button.secondary {
      background: transparent;
      color: var(--fg);
      border: 1px solid var(--border);
    }
    #toolbar button.secondary:hover { background: var(--panel-bg); }
    #toolbar .spacer { flex: 1; }

    /* ── Tab bar ── */
    #tabs {
      display: flex;
      border-bottom: 1px solid var(--border);
      background: var(--panel-bg);
      flex-shrink: 0;
    }
    .tab {
      padding: 5px 14px;
      font-size: 12px;
      cursor: pointer;
      border-bottom: 2px solid transparent;
      color: var(--vscode-tab-inactiveForeground, #aaa);
      user-select: none;
    }
    .tab.active {
      color: var(--fg);
      border-bottom-color: var(--accent);
    }
    .tab:hover:not(.active) { color: var(--fg); }

    /* ── Main layout ── */
    #main {
      display: flex;
      flex: 1;
      overflow: hidden;
    }

    /* ── Graph canvas ── */
    #canvas-area {
      flex: 1;
      overflow: auto;
      padding: 16px;
      background: var(--bg);
    }
    #canvas-area.hidden, #yaml-area.hidden, #diff-area.hidden { display: none; }

    /* ── YAML / Diff view ── */
    #yaml-area, #diff-area {
      flex: 1;
      overflow: auto;
      padding: 16px;
      background: var(--bg);
    }
    pre.yaml-code {
      font-family: var(--vscode-editor-font-family, monospace);
      font-size: 12px;
      white-space: pre;
      line-height: 1.5;
    }

    /* diff colors */
    .diff-added { background: rgba(0,100,0,0.3); color: #9df9b1; }
    .diff-removed { background: rgba(100,0,0,0.3); color: #f98f8f; text-decoration: line-through; }
    .diff-context { color: var(--fg); opacity: 0.7; }
    .diff-ellipsis { color: var(--vscode-descriptionForeground); font-style: italic; }

    /* ── Properties panel ── */
    #props-panel {
      width: 320px;
      min-width: 260px;
      max-width: 400px;
      border-left: 1px solid var(--border);
      background: var(--panel-bg);
      display: flex;
      flex-direction: column;
      overflow: hidden;
    }
    #props-panel.empty-state {
      align-items: center;
      justify-content: center;
      color: var(--vscode-descriptionForeground);
      font-size: 12px;
      padding: 16px;
      text-align: center;
    }
    #props-header {
      padding: 8px 12px;
      font-size: 11px;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.07em;
      color: var(--vscode-descriptionForeground);
      border-bottom: 1px solid var(--border);
      flex-shrink: 0;
    }
    #props-body {
      flex: 1;
      overflow-y: auto;
      padding: 10px 12px;
    }

    /* ── Form controls ── */
    .form-row {
      margin-bottom: 10px;
    }
    .form-row label {
      display: block;
      font-size: 11px;
      font-weight: 600;
      color: var(--vscode-descriptionForeground);
      margin-bottom: 3px;
    }
    .form-row input[type=text],
    .form-row select,
    .form-row textarea {
      width: 100%;
      background: var(--input-bg);
      color: var(--fg);
      border: 1px solid var(--input-border);
      border-radius: 3px;
      padding: 4px 6px;
      font-size: 12px;
      font-family: var(--vscode-font-family, inherit);
    }
    .form-row textarea {
      resize: vertical;
      min-height: 60px;
      font-family: var(--vscode-editor-font-family, monospace);
      font-size: 11px;
    }
    .form-row input:focus, .form-row select:focus, .form-row textarea:focus {
      outline: 1px solid var(--accent);
    }
    .form-row .hint {
      font-size: 10px;
      color: var(--vscode-descriptionForeground);
      margin-top: 2px;
    }
    .form-section {
      font-size: 11px;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.07em;
      color: var(--vscode-descriptionForeground);
      border-top: 1px solid var(--border);
      margin: 12px 0 8px;
      padding-top: 8px;
    }
    .props-actions {
      display: flex;
      gap: 6px;
      margin-top: 12px;
    }
    .props-actions button {
      background: var(--btn-bg);
      color: var(--btn-fg);
      border: none;
      border-radius: 3px;
      padding: 4px 10px;
      font-size: 12px;
      cursor: pointer;
    }
    .props-actions button:hover { background: var(--btn-hover); }
    .props-actions button.danger {
      background: var(--error-bg);
      border: 1px solid var(--error-border);
      color: var(--error-fg);
    }
    .props-actions button.danger:hover { background: #7a1d1d; }

    /* ── Graph nodes ── */
    .wf-container { margin-bottom: 32px; }
    .wf-title {
      font-size: 13px;
      font-weight: 600;
      margin-bottom: 14px;
      padding-bottom: 6px;
      border-bottom: 1px solid var(--border);
    }
    .wf-trigger {
      background: var(--panel-bg);
      border: 1px solid var(--border);
      border-radius: 6px;
      padding: 6px 10px;
      font-size: 11px;
      margin-bottom: 10px;
      display: inline-block;
      cursor: pointer;
    }
    .wf-trigger:hover { border-color: var(--accent); }
    .edge-line {
      width: 2px;
      height: 18px;
      background: var(--border);
      margin: 0 auto 0 18px;
    }
    .step-row {
      display: flex;
      align-items: flex-start;
      gap: 8px;
      margin-bottom: 6px;
    }
    .step-drag-handle {
      width: 16px;
      height: 44px;
      display: flex;
      flex-direction: column;
      justify-content: center;
      align-items: center;
      cursor: grab;
      color: var(--border);
      font-size: 10px;
      flex-shrink: 0;
      padding-top: 4px;
    }
    .step-drag-handle:active { cursor: grabbing; }
    .step-card {
      flex: 1;
      border-radius: 5px;
      border: 1.5px solid var(--border);
      padding: 7px 10px;
      cursor: pointer;
      transition: border-color 0.1s;
      position: relative;
      min-height: 44px;
    }
    .step-card:hover { border-color: var(--accent); }
    .step-card.selected {
      border-color: var(--accent);
      box-shadow: 0 0 0 1px var(--accent);
    }
    .step-card.has-error { border-color: var(--error-border) !important; }
    .step-card.readonly { opacity: 0.75; }
    .step-card.unsupported { opacity: 0.5; }

    .step-card[data-type="agent"]    { background: #1a2c47; }
    .step-card[data-type="split"]    { background: #2a1a47; border-radius: 0; }
    .step-card[data-type="approval"] { background: #3a2e08; }
    .step-card[data-type="wait_for"] { background: #0d2c22; }
    .step-card[data-type="foreach"]  { background: #2a1e08; }
    .step-card[data-type="workflow"] { background: #1a0d2c; }

    .step-card-top {
      display: flex;
      align-items: baseline;
      gap: 6px;
    }
    .step-type-badge {
      font-size: 9px;
      font-weight: 700;
      text-transform: uppercase;
      letter-spacing: 0.08em;
      padding: 1px 5px;
      border-radius: 3px;
      background: rgba(255,255,255,0.1);
      flex-shrink: 0;
    }
    .step-id {
      font-weight: 600;
      font-size: 12px;
      flex: 1;
    }
    .step-meta {
      font-size: 10px;
      color: var(--vscode-descriptionForeground);
      margin-top: 2px;
      line-height: 1.4;
    }
    .lock-badge {
      position: absolute;
      top: 5px;
      right: 7px;
      font-size: 10px;
      color: var(--warn-fg);
    }
    .error-badge {
      position: absolute;
      bottom: 5px;
      right: 7px;
      font-size: 9px;
      color: var(--error-fg);
    }
    .step-if {
      font-size: 10px;
      color: var(--warn-fg);
      margin-top: 3px;
      font-family: var(--vscode-editor-font-family, monospace);
    }
    /* Split branches */
    .branches-row {
      display: flex;
      gap: 6px;
      margin: 4px 0 4px 24px;
    }
    .branch-chip {
      font-size: 10px;
      background: rgba(155, 91, 213, 0.2);
      border: 1px solid rgba(155, 91, 213, 0.5);
      border-radius: 3px;
      padding: 1px 6px;
      color: #c8a8f8;
    }
    /* Retry loop indicator */
    .retry-badge {
      font-size: 10px;
      color: var(--warn-fg);
      margin-top: 3px;
    }
    /* Add step button */
    .add-step-btn {
      display: flex;
      align-items: center;
      gap: 6px;
      color: var(--vscode-descriptionForeground);
      font-size: 11px;
      cursor: pointer;
      padding: 4px 0 4px 24px;
      margin-top: 2px;
      border: 1.5px dashed var(--border);
      border-radius: 4px;
      background: none;
      width: 100%;
      text-align: left;
    }
    .add-step-btn:hover {
      color: var(--fg);
      border-color: var(--accent);
    }

    /* ── Error panel ── */
    #error-panel {
      background: var(--error-bg);
      border-top: 1px solid var(--error-border);
      color: var(--error-fg);
      font-size: 11px;
      padding: 6px 12px;
      max-height: 80px;
      overflow-y: auto;
      flex-shrink: 0;
      display: none;
    }
    #error-panel.visible { display: block; }
    .error-item { margin-bottom: 2px; }

    /* ── Status bar ── */
    #status-bar {
      background: var(--accent);
      color: #fff;
      font-size: 11px;
      padding: 2px 10px;
      flex-shrink: 0;
      display: none;
    }
    #status-bar.visible { display: block; }
    #status-bar.ok { background: #1e6432; }
    #status-bar.err { background: #6e1414; }

    /* validation message inside props */
    .node-errors {
      background: var(--error-bg);
      border: 1px solid var(--error-border);
      border-radius: 3px;
      padding: 5px 8px;
      font-size: 11px;
      color: var(--error-fg);
      margin-bottom: 10px;
    }
    .node-errors li { margin-left: 12px; margin-top: 2px; }

    .readonly-notice {
      background: rgba(204, 167, 0, 0.1);
      border: 1px solid rgba(204, 167, 0, 0.4);
      border-radius: 3px;
      padding: 5px 8px;
      font-size: 11px;
      color: var(--warn-fg);
      margin-bottom: 10px;
    }

    .diff-save-bar {
      display: flex;
      gap: 8px;
      margin-bottom: 12px;
      align-items: center;
    }
    .diff-save-bar button {
      background: var(--btn-bg);
      color: var(--btn-fg);
      border: none;
      border-radius: 3px;
      padding: 4px 12px;
      font-size: 12px;
      cursor: pointer;
    }
    .diff-save-bar button:hover { background: var(--btn-hover); }
    .diff-save-bar .label { font-size: 12px; color: var(--vscode-descriptionForeground); }
    .no-diff { color: var(--vscode-descriptionForeground); font-size: 12px; font-style: italic; }
  </style>
</head>
<body>
  <!-- Toolbar -->
  <div id="toolbar">
    <span style="font-size:12px;font-weight:600;margin-right:4px;">Apiary Editor</span>
    <select id="wf-selector" class="wf-selector">
      <option value="">— no workflows —</option>
    </select>
    <button id="btn-add-step" class="secondary" title="Add a new step to the current workflow">+ Step</button>
    <div class="spacer"></div>
    <button id="btn-validate" class="secondary" title="Run validation">Validate</button>
    <button id="btn-save" title="Preview diff and save changes to file">Save…</button>
  </div>

  <!-- Tabs -->
  <div id="tabs">
    <div class="tab active" data-tab="graph">Graph</div>
    <div class="tab" data-tab="yaml">YAML</div>
    <div class="tab" data-tab="diff">Diff</div>
  </div>

  <!-- Main -->
  <div id="main">
    <!-- Graph canvas -->
    <div id="canvas-area">
      <div id="graph-root"><div style="color:var(--vscode-descriptionForeground);font-size:12px;padding:24px;">Loading…</div></div>
    </div>

    <!-- YAML view -->
    <div id="yaml-area" class="hidden">
      <pre class="yaml-code" id="yaml-code-display"></pre>
    </div>

    <!-- Diff view -->
    <div id="diff-area" class="hidden">
      <div id="diff-content"></div>
    </div>

    <!-- Properties panel -->
    <div id="props-panel" class="empty-state">
      <div id="props-empty-msg">Select a workflow node to edit its properties.</div>
      <div id="props-inner" style="display:none;width:100%;height:100%;display:flex;flex-direction:column;">
        <div id="props-header"></div>
        <div id="props-body"></div>
      </div>
    </div>
  </div>

  <!-- Error panel -->
  <div id="error-panel"></div>

  <!-- Status bar -->
  <div id="status-bar"></div>

<script nonce="${nonce}">
// =====================================================================
// Apiary bidirectional visual workflow editor — webview script
// Communicates with the extension host via acquireVsCodeApi().postMessage
// =====================================================================

const vscode = acquireVsCodeApi();

// ── State ──────────────────────────────────────────────────────────────
let state = {
  config: null,          // ApiaryConfig
  originalYaml: '',
  currentYaml: '',
  editability: {},       // "wfId::stepId" → 'editable'|'readonly'|'unsupported'
  errors: [],            // ValidationError[]
  selectedWfIdx: 0,
  selectedNode: null,    // { kind: 'trigger'|'step', wfIdx, stepIdx }
  pendingChanges: false, // model has unsaved changes
  activeTab: 'graph',
};

// Deep-clone a value for editing (avoids mutating shared state)
function clone(v) { return JSON.parse(JSON.stringify(v)); }

// ── DOM refs ───────────────────────────────────────────────────────────
const wfSelector = document.getElementById('wf-selector');
const graphRoot = document.getElementById('graph-root');
const yamlCodeDisplay = document.getElementById('yaml-code-display');
const diffContent = document.getElementById('diff-content');
const propsPanel = document.getElementById('props-panel');
const propsEmptyMsg = document.getElementById('props-empty-msg');
const propsInner = document.getElementById('props-inner');
const propsHeader = document.getElementById('props-header');
const propsBody = document.getElementById('props-body');
const errorPanel = document.getElementById('error-panel');
const statusBar = document.getElementById('status-bar');

// ── Tab switching ──────────────────────────────────────────────────────
document.querySelectorAll('.tab').forEach(tab => {
  tab.addEventListener('click', () => {
    document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
    tab.classList.add('active');
    state.activeTab = tab.dataset.tab;
    document.getElementById('canvas-area').classList.toggle('hidden', state.activeTab !== 'graph');
    document.getElementById('yaml-area').classList.toggle('hidden', state.activeTab !== 'yaml');
    document.getElementById('diff-area').classList.toggle('hidden', state.activeTab !== 'diff');
    if (state.activeTab === 'yaml') { renderYamlTab(); }
    if (state.activeTab === 'diff') { renderDiffTab(); }
  });
});

// ── Toolbar buttons ────────────────────────────────────────────────────
document.getElementById('btn-validate').addEventListener('click', () => {
  vscode.postMessage({ type: 'validate' });
});

document.getElementById('btn-save').addEventListener('click', () => {
  if (!state.config) { return; }
  // Switch to diff tab, then confirm save
  document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
  document.querySelector('[data-tab="diff"]').classList.add('active');
  state.activeTab = 'diff';
  document.getElementById('canvas-area').classList.add('hidden');
  document.getElementById('yaml-area').classList.add('hidden');
  document.getElementById('diff-area').classList.remove('hidden');
  renderDiffTab(true);
});

document.getElementById('btn-add-step').addEventListener('click', () => {
  const wf = currentWorkflow();
  if (!wf) { return; }
  const newStep = { id: \`step-\${Date.now()}\`, type: 'agent', agent: '', prompt: '' };
  if (!wf.steps) { wf.steps = []; }
  wf.steps.push(newStep);
  markDirty();
  renderGraph();
  selectStep(state.selectedWfIdx, wf.steps.length - 1);
});

wfSelector.addEventListener('change', () => {
  state.selectedWfIdx = parseInt(wfSelector.value, 10) || 0;
  state.selectedNode = null;
  renderGraph();
  renderProps();
});

// ── Helpers ────────────────────────────────────────────────────────────
function currentWorkflow() {
  if (!state.config || !state.config.workflows) { return null; }
  return state.config.workflows[state.selectedWfIdx] ?? null;
}

function markDirty() {
  state.pendingChanges = true;
  // Regenerate current YAML from model
  state.currentYaml = serializeConfig(state.config);
}

function showStatus(msg, kind) {
  statusBar.textContent = msg;
  statusBar.className = 'visible ' + (kind || '');
  setTimeout(() => { statusBar.className = ''; }, 3000);
}

// ── Message handler ────────────────────────────────────────────────────
window.addEventListener('message', event => {
  const msg = event.data;
  switch (msg.type) {
    case 'init':
      handleInit(msg.payload);
      break;
    case 'validationResult':
      state.errors = msg.errors || [];
      showErrors();
      renderGraph();
      showStatus('Validation complete — ' + state.errors.length + ' issue(s)', state.errors.length ? 'err' : 'ok');
      break;
    case 'saveResult':
      if (msg.success) {
        state.pendingChanges = false;
        showStatus('Saved successfully', 'ok');
      } else {
        showStatus('Save failed: ' + (msg.error || 'unknown error'), 'err');
      }
      break;
  }
});

function handleInit(payload) {
  state.originalYaml = payload.originalYaml || '';
  state.config = payload.config ? clone(payload.config) : null;
  state.editability = payload.editability || {};
  state.errors = payload.errors || [];
  state.currentYaml = payload.currentYaml || '';
  state.pendingChanges = false;

  // Populate workflow selector
  wfSelector.innerHTML = '';
  if (state.config && state.config.workflows && state.config.workflows.length > 0) {
    state.config.workflows.forEach((wf, i) => {
      const opt = document.createElement('option');
      opt.value = String(i);
      opt.textContent = wf.id + (wf.description ? \` — \${wf.description}\` : '');
      wfSelector.appendChild(opt);
    });
    wfSelector.value = String(Math.min(state.selectedWfIdx, state.config.workflows.length - 1));
    state.selectedWfIdx = parseInt(wfSelector.value, 10);
  } else {
    const opt = document.createElement('option');
    opt.value = '0';
    opt.textContent = state.config ? '(no workflows)' : '(parse error)';
    wfSelector.appendChild(opt);
  }

  showErrors();
  if (state.activeTab === 'graph') { renderGraph(); }
  if (state.activeTab === 'yaml') { renderYamlTab(); }
  if (state.activeTab === 'diff') { renderDiffTab(); }
  if (state.selectedNode) { renderProps(); }
}

// ── Error display ──────────────────────────────────────────────────────
function showErrors() {
  const globalErrs = state.errors.filter(e => !e.stepId);
  if (globalErrs.length === 0) {
    errorPanel.className = '';
    return;
  }
  errorPanel.className = 'visible';
  errorPanel.innerHTML = globalErrs
    .map(e => \`<div class="error-item">⚠ \${escHtml(e.message)}</div>\`)
    .join('');
}

function errorsForStep(wfId, stepId) {
  return state.errors.filter(e => e.stepId === stepId || e.stepId === \`\${wfId}::\${stepId}\`);
}

// ── Graph rendering ────────────────────────────────────────────────────
function renderGraph() {
  const wf = currentWorkflow();
  if (!wf) {
    graphRoot.innerHTML = '<div style="color:var(--vscode-descriptionForeground);font-size:12px;padding:24px;">No workflows defined.</div>';
    return;
  }

  const html = [];
  html.push(\`<div class="wf-container">\`);
  html.push(\`<div class="wf-title">\${escHtml(wf.id)}\${wf.description ? ' <span style="font-weight:400;color:var(--vscode-descriptionForeground)">— ' + escHtml(wf.description) + '</span>' : ''}</div>\`);

  // Trigger node
  if (wf.trigger) {
    const m = wf.trigger.match || {};
    const parts = [];
    if (m.labels && m.labels.length) { parts.push('labels: ' + m.labels.join(', ')); }
    if (m.types && m.types.length) { parts.push('types: ' + m.types.join(', ')); }
    if (m.states && m.states.length) { parts.push('states: ' + m.states.join(', ')); }
    if (m.source) { parts.push('source: ' + m.source); }
    const isSelected = state.selectedNode && state.selectedNode.kind === 'trigger' && state.selectedNode.wfIdx === state.selectedWfIdx;
    html.push(\`<div class="wf-trigger \${isSelected ? 'selected' : ''}" data-kind="trigger" data-wf="\${state.selectedWfIdx}" title="Click to edit trigger">
      <b>⚡ trigger</b> · priority \${wf.trigger.priority ?? 0}
      \${parts.length ? '<br><span style="font-size:10px;">' + escHtml(parts.join(' · ')) + '</span>' : ''}
    </div>\`);
    html.push('<div class="edge-line"></div>');
  }

  // Steps
  const steps = wf.steps || [];
  for (let i = 0; i < steps.length; i++) {
    const step = steps[i];
    const key = \`\${wf.id}::\${step.id}\`;
    const editability = state.editability[key] || 'editable';
    const stepErrs = errorsForStep(wf.id, step.id);
    const type = step.type || 'agent';
    const isSelected = state.selectedNode && state.selectedNode.kind === 'step' &&
                       state.selectedNode.wfIdx === state.selectedWfIdx &&
                       state.selectedNode.stepIdx === i;

    const classes = [
      'step-card',
      isSelected ? 'selected' : '',
      editability === 'readonly' ? 'readonly' : '',
      editability === 'unsupported' ? 'unsupported' : '',
      stepErrs.length ? 'has-error' : '',
    ].filter(Boolean).join(' ');

    html.push(\`<div class="step-row">\`);
    html.push(\`<div class="step-drag-handle" draggable="true" data-drag-step="\${i}" title="Drag to reorder">⠿</div>\`);
    html.push(\`<div class="\${classes}" data-kind="step" data-wf="\${state.selectedWfIdx}" data-step="\${i}" data-type="\${escHtml(type)}" title="Click to edit">\`);

    // Top row: type badge + id
    html.push(\`<div class="step-card-top">\`);
    html.push(\`<span class="step-type-badge">\${escHtml(type)}</span>\`);
    html.push(\`<span class="step-id">\${escHtml(step.id)}</span>\`);
    html.push('</div>');

    // Meta line
    const metaParts = [];
    if (step.agent) { metaParts.push('agent: ' + step.agent); }
    if (step.workflow || step.uses) { metaParts.push('workflow: ' + (step.workflow || step.uses)); }
    if (step.items) { metaParts.push('items: ' + step.items); }
    if (step.wait_for) { metaParts.push('kind: ' + (step.wait_for.kind || 'ci')); }
    if (metaParts.length) {
      html.push(\`<div class="step-meta">\${escHtml(metaParts.join(' · '))}</div>\`);
    }

    // Condition guard
    if (step.if || step.condition) {
      html.push(\`<div class="step-if">if: \${escHtml(step.if || step.condition)}</div>\`);
    }

    // Retry badge
    if (step.reject_when || step.on_reject) {
      const rf = step.on_reject && step.on_reject.restart_from ? ' → ' + step.on_reject.restart_from : '';
      const mx = step.on_reject && step.on_reject.max ? ' max ' + step.on_reject.max + 'x' : '';
      html.push(\`<div class="retry-badge">↩ retry\${escHtml(rf + mx)}</div>\`);
    }
    if (step.on_fail && step.on_fail.goto) {
      html.push(\`<div class="retry-badge">↩ on_fail → \${escHtml(step.on_fail.goto)}</div>\`);
    }

    // Readonly / unsupported badge
    if (editability === 'readonly') {
      html.push('<span class="lock-badge" title="This step uses v2 sub-group constructs; shown read-only">🔒</span>');
    } else if (editability === 'unsupported') {
      html.push('<span class="lock-badge" title="Unknown step type; cannot edit">⛔</span>');
    }

    // Error badge
    if (stepErrs.length) {
      html.push(\`<span class="error-badge" title="\${escHtml(stepErrs.map(e=>e.message).join('\\n'))}">⚠ \${stepErrs.length}</span>\`);
    }

    html.push('</div>'); // step-card
    html.push('</div>'); // step-row

    // Branches for split steps
    if (type === 'split' && step.branches && step.branches.length) {
      html.push('<div class="branches-row">');
      for (const br of step.branches) {
        const label = br.else ? 'else' : (br.if || '');
        html.push(\`<span class="branch-chip">→ \${escHtml(br.goto)} <em>\${escHtml(label)}</em></span>\`);
      }
      html.push('</div>');
    }

    // Edge connector (except after last step)
    if (i < steps.length - 1) {
      html.push('<div class="edge-line"></div>');
    }
  }

  // Add step button
  html.push(\`<div style="margin-top:8px;">
    <button class="add-step-btn" id="btn-add-step-inline">+ Add Step</button>
  </div>\`);

  html.push('</div>'); // wf-container
  graphRoot.innerHTML = html.join('');

  // Event delegation for node clicks
  graphRoot.addEventListener('click', handleGraphClick);
  graphRoot.addEventListener('dragstart', handleDragStart);
  graphRoot.addEventListener('dragover', handleDragOver);
  graphRoot.addEventListener('drop', handleDrop);

  const addInline = document.getElementById('btn-add-step-inline');
  if (addInline) {
    addInline.addEventListener('click', () => {
      document.getElementById('btn-add-step').click();
    });
  }
}

// ── Graph event handling ───────────────────────────────────────────────
function handleGraphClick(e) {
  const trigger = e.target.closest('[data-kind="trigger"]');
  if (trigger) {
    state.selectedNode = { kind: 'trigger', wfIdx: parseInt(trigger.dataset.wf, 10) };
    renderGraph();
    renderProps();
    return;
  }
  const card = e.target.closest('[data-kind="step"]');
  if (card) {
    const wfIdx = parseInt(card.dataset.wf, 10);
    const stepIdx = parseInt(card.dataset.step, 10);
    state.selectedNode = { kind: 'step', wfIdx, stepIdx };
    renderGraph();
    renderProps();
  }
}

// ── Drag-and-drop reorder ──────────────────────────────────────────────
let dragIdx = null;
function handleDragStart(e) {
  const handle = e.target.closest('[data-drag-step]');
  if (!handle) { return; }
  dragIdx = parseInt(handle.dataset.dragStep, 10);
  e.dataTransfer.effectAllowed = 'move';
}
function handleDragOver(e) {
  if (dragIdx === null) { return; }
  e.preventDefault();
  e.dataTransfer.dropEffect = 'move';
}
function handleDrop(e) {
  if (dragIdx === null) { return; }
  e.preventDefault();
  const target = e.target.closest('[data-drag-step]');
  if (!target) { dragIdx = null; return; }
  const toIdx = parseInt(target.dataset.dragStep, 10);
  if (toIdx === dragIdx) { dragIdx = null; return; }
  const wf = currentWorkflow();
  if (!wf || !wf.steps) { dragIdx = null; return; }
  const [moved] = wf.steps.splice(dragIdx, 1);
  wf.steps.splice(toIdx, 0, moved);
  // Adjust selected step index
  if (state.selectedNode && state.selectedNode.kind === 'step') {
    const sel = state.selectedNode.stepIdx;
    if (sel === dragIdx) { state.selectedNode.stepIdx = toIdx; }
    else if (dragIdx < toIdx && sel > dragIdx && sel <= toIdx) { state.selectedNode.stepIdx--; }
    else if (dragIdx > toIdx && sel >= toIdx && sel < dragIdx) { state.selectedNode.stepIdx++; }
  }
  dragIdx = null;
  markDirty();
  renderGraph();
  renderProps();
}

// ── Properties panel ───────────────────────────────────────────────────
function renderProps() {
  if (!state.selectedNode) {
    propsPanel.className = 'empty-state';
    propsEmptyMsg.style.display = '';
    propsInner.style.display = 'none';
    return;
  }

  propsPanel.className = '';
  propsEmptyMsg.style.display = 'none';
  propsInner.style.display = 'flex';
  propsInner.style.flexDirection = 'column';
  propsInner.style.width = '100%';
  propsInner.style.height = '100%';

  const { kind, wfIdx, stepIdx } = state.selectedNode;
  const wf = state.config && state.config.workflows && state.config.workflows[wfIdx];
  if (!wf) { return; }

  if (kind === 'trigger') {
    renderTriggerProps(wf, wfIdx);
  } else if (kind === 'step') {
    const step = wf.steps && wf.steps[stepIdx];
    if (!step) { return; }
    renderStepProps(wf, wfIdx, step, stepIdx);
  }
}

function renderTriggerProps(wf, wfIdx) {
  propsHeader.textContent = \`Trigger — \${wf.id}\`;
  const trigger = wf.trigger || {};
  const match = trigger.match || {};

  const html = [];
  html.push(makeFormRow('Priority', 'trigger-priority', 'number', String(trigger.priority ?? 0), 'Dispatch priority (lower = higher priority)'));
  html.push(makeFormRow('Labels (comma-separated)', 'trigger-labels', 'text', (match.labels || []).join(', '), 'Only match tasks that have ALL these labels'));
  html.push(makeFormRow('Types (comma-separated)', 'trigger-types', 'text', (match.types || []).join(', '), 'e.g. feature, bug'));
  html.push(makeFormRow('States (comma-separated)', 'trigger-states', 'text', (match.states || []).join(', '), 'e.g. open'));
  html.push(makeFormRow('Source ID', 'trigger-source', 'text', match.source || '', 'Source this trigger reads from'));
  html.push(makeFormRow('Exclude label prefix', 'trigger-excl', 'text', match.exclude_label_prefix || '', 'Exclude tasks with labels starting with this prefix'));
  html.push(makeCheckboxRow('Exclusive', 'trigger-exclusive', trigger.exclusive || false, 'Stop trigger evaluation after this matches'));
  html.push(makeCheckboxRow('Once', 'trigger-once', trigger.once || false, 'Run at most once per task even if it stays matching'));

  html.push('<div class="props-actions">');
  html.push('<button id="btn-apply-trigger">Apply</button>');
  html.push('</div>');

  propsBody.innerHTML = html.join('');

  document.getElementById('btn-apply-trigger').addEventListener('click', () => {
    if (!wf.trigger) { wf.trigger = {}; }
    if (!wf.trigger.match) { wf.trigger.match = {}; }
    wf.trigger.priority = parseInt(v('trigger-priority'), 10) || 0;
    wf.trigger.exclusive = document.getElementById('trigger-exclusive').checked;
    wf.trigger.once = document.getElementById('trigger-once').checked;
    const m = wf.trigger.match;
    m.labels = splitCsv('trigger-labels');
    m.types = splitCsv('trigger-types');
    m.states = splitCsv('trigger-states');
    m.source = v('trigger-source') || undefined;
    m.exclude_label_prefix = v('trigger-excl') || undefined;
    markDirty();
    renderGraph();
    showStatus('Trigger updated — click Save… to write to file', '');
  });
}

function renderStepProps(wf, wfIdx, step, stepIdx) {
  const key = \`\${wf.id}::\${step.id}\`;
  const editability = state.editability[key] || 'editable';
  const stepErrs = errorsForStep(wf.id, step.id);
  const type = step.type || 'agent';

  propsHeader.textContent = \`Step: \${step.id}\`;

  const html = [];

  // Error list
  if (stepErrs.length) {
    html.push('<div class="node-errors"><b>Validation errors:</b><ul>');
    for (const e of stepErrs) { html.push(\`<li>\${escHtml(e.message)}</li>\`); }
    html.push('</ul></div>');
  }

  // Readonly notice
  if (editability === 'readonly') {
    html.push('<div class="readonly-notice">🔒 This step uses v2 sub-group constructs (<code>steps:</code>, <code>parallel:</code>, <code>for_each:</code>). Properties are shown read-only; edit the YAML directly to change sub-group layout.</div>');
  } else if (editability === 'unsupported') {
    html.push('<div class="readonly-notice">⛔ Unknown step type <code>' + escHtml(type) + '</code>. Edit the YAML directly.</div>');
  }

  const ro = editability !== 'editable';

  // Common fields
  html.push(makeFormRow('ID', 'step-id', 'text', step.id, 'Unique step identifier', ro));
  html.push(makeSelectRow('Type', 'step-type', ['agent','split','approval','foreach','workflow','wait_for'], type, 'Step type', ro));
  html.push(makeFormRow('Condition (if:)', 'step-if', 'text', step.if || step.condition || '', 'Guard expression — step skipped when false', ro));

  // Type-specific fields
  if (type === 'agent') {
    html.push('<div class="form-section">Agent Step</div>');
    html.push(makeFormRow('Agent ID', 'step-agent', 'text', step.agent || '', 'Must match an agent defined in this config', ro));
    html.push(makeFormRow('Model override', 'step-model', 'text', step.model || '', 'Overrides the agent\'s model for this step', ro));
    html.push(makeTextareaRow('Prompt', 'step-prompt', step.prompt || '', 'Injected prompt for this step', ro));
    html.push(makeTextareaRow('Summary prompt', 'step-summary-prompt', step.summary_prompt || '', 'Used for per_step result comments', ro));
    html.push(makeCheckboxRow('Idempotent', 'step-idempotent', step.idempotent || false, 'Skip if a successful run already exists', ro));
    html.push('<div class="form-section">Retry / Rejection</div>');
    html.push(makeFormRow('reject_when', 'step-reject-when', 'text', step.reject_when || step.fail_when || '', 'Expression: reject and loop when true', ro));
    html.push(makeFormRow('on_reject.restart_from', 'step-restart-from', 'text',
      (step.on_reject && step.on_reject.restart_from) || '', 'Step ID to restart from on rejection', ro));
    html.push(makeFormRow('on_reject.max', 'step-reject-max', 'number',
      String((step.on_reject && step.on_reject.max) || ''), 'Max retry cycles', ro));
    html.push('<div class="form-section">Memory</div>');
    html.push(makeFormRow('memory.write (comma-separated field names)', 'step-mem-write', 'text',
      ((step.memory && step.memory.write) || []).join(', '), 'Output schema fields to persist to workflow memory', ro));
  } else if (type === 'split') {
    html.push('<div class="form-section">Branches</div>');
    const branches = step.branches || [];
    for (let bi = 0; bi < branches.length; bi++) {
      const br = branches[bi];
      html.push(\`<div style="background:rgba(155,91,213,0.1);border:1px solid rgba(155,91,213,0.3);border-radius:4px;padding:6px 8px;margin-bottom:6px;">\`);
      html.push(makeFormRow(\`Branch \${bi+1} — if:\`, \`branch-if-\${bi}\`, 'text', br.if || '', 'Condition expression; empty = else branch', ro));
      html.push(makeFormRow(\`Branch \${bi+1} — goto:\`, \`branch-goto-\${bi}\`, 'text', br.goto || '', 'Target step ID', ro));
      if (!ro) {
        html.push(\`<button class="secondary" style="font-size:10px;padding:2px 6px;background:var(--error-bg);border:1px solid var(--error-border);color:var(--error-fg);border-radius:3px;cursor:pointer;" data-rm-branch="\${bi}">Remove</button>\`);
      }
      html.push('</div>');
    }
    if (!ro) {
      html.push('<button id="btn-add-branch" class="secondary" style="font-size:11px;margin-top:4px;">+ Add Branch</button>');
    }
  } else if (type === 'approval') {
    html.push('<div class="form-section">Approval Step</div>');
    html.push(makeTextareaRow('Message', 'step-message', step.message || '', 'Message shown to approvers', ro));
    html.push(makeFormRow('Timeout', 'step-timeout', 'text', step.timeout || '', 'e.g. 48h', ro));
    const resumeOn = (step.resume_on || {});
    const abortOn  = (step.abort_on  || {});
    html.push(makeFormRow('resume_on.comment_contains', 'step-resume-comment', 'text',
      resumeOn.comment_contains || '', 'Resume when a comment contains this text', ro));
    html.push(makeFormRow('abort_on.comment_contains', 'step-abort-comment', 'text',
      abortOn.comment_contains || '', 'Abort when a comment contains this text', ro));
  } else if (type === 'foreach') {
    html.push('<div class="form-section">ForEach Step</div>');
    html.push(makeFormRow('Items expression', 'step-items', 'text', step.items || step.for_each || '', 'e.g. memory.tasks', ro));
    html.push(makeFormRow('As (loop variable)', 'step-as', 'text', step.as || '', 'e.g. task', ro));
    html.push(makeFormRow('Concurrency', 'step-concurrency', 'number', String(step.concurrency || ''), '', ro));
    html.push(makeFormRow('Max items', 'step-max-items', 'number', String(step.max_items || step.max || ''), '', ro));
    html.push(makeCheckboxRow('Fail fast', 'step-fail-fast', step.fail_fast || false, 'Stop on first child failure', ro));
    if (step.step) {
      html.push(makeFormRow('Child step agent', 'foreach-child-agent', 'text', step.step.agent || '', '', ro));
      html.push(makeTextareaRow('Child step prompt', 'foreach-child-prompt', step.step.prompt || '', '', ro));
    }
  } else if (type === 'workflow') {
    html.push('<div class="form-section">Sub-Workflow Step</div>');
    html.push(makeFormRow('Workflow ID', 'step-workflow', 'text', step.workflow || '', 'Must match a workflow defined in this config', ro));
    html.push(makeFormRow('Uses (file path)', 'step-uses', 'text', step.uses || '', 'Relative path to a reusable workflow YAML file', ro));
  } else if (type === 'wait_for') {
    html.push('<div class="form-section">Wait-For Step</div>');
    const wfc = step.wait_for || {};
    html.push(makeSelectRow('Kind', 'waitfor-kind', ['ci','dependency'], wfc.kind || 'ci', 'What to wait for', ro));
    html.push(makeFormRow('Max duration', 'waitfor-max-dur', 'text', wfc.max_duration || '', 'e.g. 2h', ro));
    html.push(makeFormRow('Check interval', 'waitfor-interval', 'text', wfc.check_interval || '', 'e.g. 30s', ro));
    html.push(makeFormRow('on_timeout', 'waitfor-timeout', 'text', wfc.on_timeout || '', 'fail or hold', ro));
    html.push(makeCheckboxRow('Fail if not passed', 'waitfor-fail', wfc.fail_if_not_passed !== false, 'Reject step if CI is not green', ro));
  }

  if (!ro) {
    html.push('<div class="props-actions">');
    html.push('<button id="btn-apply-step">Apply</button>');
    html.push(\`<button id="btn-delete-step" class="danger">Delete</button>\`);
    html.push('</div>');
  }

  propsBody.innerHTML = html.join('');

  if (!ro) {
    document.getElementById('btn-apply-step').addEventListener('click', () => {
      applyStepChanges(wf, step, stepIdx, type);
    });
    document.getElementById('btn-delete-step').addEventListener('click', () => {
      if (!wf.steps) { return; }
      wf.steps.splice(stepIdx, 1);
      state.selectedNode = null;
      markDirty();
      renderGraph();
      renderProps();
    });

    if (type === 'split') {
      propsBody.querySelectorAll('[data-rm-branch]').forEach(btn => {
        btn.addEventListener('click', () => {
          const bi = parseInt(btn.dataset.rmBranch, 10);
          step.branches.splice(bi, 1);
          markDirty();
          renderProps();
        });
      });
      const addBranchBtn = document.getElementById('btn-add-branch');
      if (addBranchBtn) {
        addBranchBtn.addEventListener('click', () => {
          if (!step.branches) { step.branches = []; }
          step.branches.push({ if: '', goto: '' });
          markDirty();
          renderProps();
        });
      }
    }
  }
}

function applyStepChanges(wf, step, stepIdx, type) {
  const newId = v('step-id');
  const oldId = step.id;

  step.id = newId || step.id;
  const newType = v('step-type') || step.type;
  step.type = newType === 'agent' ? undefined : newType;

  const ifVal = v('step-if');
  if (ifVal) { step.if = ifVal; delete step.condition; }
  else { delete step.if; delete step.condition; }

  if (type === 'agent' || newType === 'agent') {
    step.agent = v('step-agent') || undefined;
    step.model = v('step-model') || undefined;
    const pr = v('step-prompt');
    step.prompt = pr || undefined;
    const spr = v('step-summary-prompt');
    step.summary_prompt = spr || undefined;
    step.idempotent = document.getElementById('step-idempotent').checked || undefined;
    const rw = v('step-reject-when');
    if (rw) { step.reject_when = rw; delete step.fail_when; } else { delete step.reject_when; delete step.fail_when; }
    const rf = v('step-restart-from');
    const rm = parseInt(v('step-reject-max'), 10);
    if (rf) { step.on_reject = { restart_from: rf, max: rm || undefined }; } else { delete step.on_reject; }
    const mw = splitCsv('step-mem-write');
    if (mw.length) { step.memory = Object.assign(step.memory || {}, { write: mw }); }
    else if (step.memory) { delete step.memory.write; }
  } else if (type === 'split') {
    const branches = step.branches || [];
    for (let bi = 0; bi < branches.length; bi++) {
      branches[bi].if = v(\`branch-if-\${bi}\`) || undefined;
      branches[bi].goto = v(\`branch-goto-\${bi}\`) || branches[bi].goto;
      if (!branches[bi].if) { branches[bi].else = true; delete branches[bi].if; }
      else { delete branches[bi].else; }
    }
    step.branches = branches;
  } else if (type === 'approval') {
    step.message = v('step-message') || undefined;
    step.timeout = v('step-timeout') || undefined;
    const rc = v('step-resume-comment');
    const ac = v('step-abort-comment');
    if (rc) { step.resume_on = { comment_contains: rc }; } else { delete step.resume_on; }
    if (ac) { step.abort_on = { comment_contains: ac }; } else { delete step.abort_on; }
  } else if (type === 'foreach') {
    const items = v('step-items');
    if (items) { step.items = items; delete step.for_each; } else { delete step.items; }
    step.as = v('step-as') || undefined;
    step.concurrency = parseInt(v('step-concurrency'), 10) || undefined;
    step.max_items = parseInt(v('step-max-items'), 10) || undefined;
    step.fail_fast = document.getElementById('step-fail-fast').checked || undefined;
    if (step.step) {
      step.step.agent = v('foreach-child-agent') || undefined;
      step.step.prompt = v('foreach-child-prompt') || undefined;
    }
  } else if (type === 'workflow') {
    step.workflow = v('step-workflow') || undefined;
    step.uses = v('step-uses') || undefined;
  } else if (type === 'wait_for') {
    if (!step.wait_for) { step.wait_for = {}; }
    step.wait_for.kind = v('waitfor-kind') || 'ci';
    step.wait_for.max_duration = v('waitfor-max-dur') || undefined;
    step.wait_for.check_interval = v('waitfor-interval') || undefined;
    step.wait_for.on_timeout = v('waitfor-timeout') || undefined;
    step.wait_for.fail_if_not_passed = document.getElementById('waitfor-fail').checked;
  }

  // Rename step id references if id changed
  if (newId && newId !== oldId && wf.steps) {
    for (const s of wf.steps) {
      if (s.on_fail && s.on_fail.goto === oldId) { s.on_fail.goto = newId; }
      if (s.on_pass && s.on_pass.next === oldId) { s.on_pass.next = newId; }
      if (s.on_reject && s.on_reject.restart_from === oldId) { s.on_reject.restart_from = newId; }
      if (s.branches) {
        for (const br of s.branches) { if (br.goto === oldId) { br.goto = newId; } }
      }
    }
  }

  // Update editability
  const key = \`\${wf.id}::\${step.id}\`;
  state.editability[key] = classifyStep(step);

  markDirty();
  renderGraph();
  renderProps();
  showStatus('Changes applied — click Save… to write to file', '');
}

// ── YAML tab ───────────────────────────────────────────────────────────
function renderYamlTab() {
  if (!state.config) {
    yamlCodeDisplay.textContent = state.originalYaml;
    return;
  }
  const yaml = state.pendingChanges ? state.currentYaml : state.originalYaml;
  yamlCodeDisplay.textContent = yaml;
}

// ── Diff tab ───────────────────────────────────────────────────────────
function renderDiffTab(showSaveAction) {
  if (!state.config) {
    diffContent.innerHTML = '<div class="no-diff">Cannot compute diff: parse error.</div>';
    return;
  }
  const before = state.originalYaml;
  const after  = state.pendingChanges ? state.currentYaml : serializeConfig(state.config);

  if (before === after) {
    diffContent.innerHTML = '<div class="no-diff">No changes.</div>';
    return;
  }

  const diffLines = computeDiff(before, after);

  let html = '';
  if (showSaveAction) {
    html += '<div class="diff-save-bar"><span class="label">Review changes below, then save to file:</span><button id="btn-confirm-save">Save to file</button></div>';
  }
  html += '<table style="border-collapse:collapse;width:100%;font-family:var(--vscode-editor-font-family,monospace);font-size:12px;">';
  for (const line of diffLines) {
    if (line.text === '...') {
      html += \`<tr><td colspan="2" class="diff-ellipsis" style="padding:2px 6px;">   …</td></tr>\`;
      continue;
    }
    const prefix = line.type === 'added' ? '+' : line.type === 'removed' ? '-' : ' ';
    const cls = line.type === 'added' ? 'diff-added' : line.type === 'removed' ? 'diff-removed' : 'diff-context';
    html += \`<tr class="\${cls}"><td style="padding:2px 6px;user-select:none;opacity:0.5;width:2ch;">\${escHtml(prefix)}</td><td style="padding:2px 6px;white-space:pre;">\${escHtml(line.text)}</td></tr>\`;
  }
  html += '</table>';

  diffContent.innerHTML = html;

  if (showSaveAction) {
    document.getElementById('btn-confirm-save').addEventListener('click', () => {
      vscode.postMessage({ type: 'save', yaml: after });
    });
  }
}

// ── Serializer (inline, mirrors serializer.ts logic) ──────────────────
// Note: this is a simplified version for use in the webview (no require/import).
// The extension host uses the TypeScript module; we replicate the key behavior here.
function serializeConfig(config) {
  // Convert to YAML using a simple stringifier.
  // The webview can't import js-yaml directly; we encode to JSON and let the
  // extension host do the final YAML dump when saving.
  // For the diff preview, we use the currentYaml computed by the extension host.
  return state.currentYaml || JSON.stringify(config, null, 2);
}

// ── Diff implementation (inline, mirrors diff.ts) ─────────────────────
function computeDiff(before, after, ctx) {
  ctx = ctx === undefined ? 3 : ctx;
  const aLines = before.split('\\n');
  const bLines = after.split('\\n');
  const m = aLines.length, n = bLines.length;
  // LCS dp table
  const dp = [];
  for (let i = 0; i <= m; i++) { dp.push(new Array(n + 1).fill(0)); }
  for (let i = 1; i <= m; i++) {
    for (let j = 1; j <= n; j++) {
      dp[i][j] = aLines[i-1] === bLines[j-1] ? dp[i-1][j-1]+1 : Math.max(dp[i-1][j], dp[i][j-1]);
    }
  }
  // Build edits
  const edits = [];
  let i = m, j = n;
  while (i > 0 || j > 0) {
    if (i > 0 && j > 0 && aLines[i-1] === bLines[j-1]) {
      edits.push({ type: 'equal', aIdx: i-1, bIdx: j-1 }); i--; j--;
    } else if (j > 0 && (i === 0 || dp[i][j-1] >= dp[i-1][j])) {
      edits.push({ type: 'insert', aIdx: i, bIdx: j-1 }); j--;
    } else {
      edits.push({ type: 'delete', aIdx: i-1, bIdx: j }); i--;
    }
  }
  edits.reverse();
  // Find changed indices
  const changed = new Set();
  edits.forEach((e, k) => { if (e.type !== 'equal') changed.add(k); });
  // Expand with context
  const included = new Set();
  for (const ci of changed) {
    for (let d = -ctx; d <= ctx; d++) {
      const idx = ci + d;
      if (idx >= 0 && idx < edits.length) included.add(idx);
    }
  }
  const result = [];
  let prev = false;
  for (let k = 0; k < edits.length; k++) {
    const e = edits[k];
    if (!included.has(k)) {
      if (prev) { result.push({ type: 'context', lineNo: 0, text: '...' }); }
      prev = false;
      continue;
    }
    prev = true;
    if (e.type === 'equal') { result.push({ type: 'context', lineNo: e.bIdx+1, text: bLines[e.bIdx] }); }
    else if (e.type === 'insert') { result.push({ type: 'added', lineNo: e.bIdx+1, text: bLines[e.bIdx] }); }
    else { result.push({ type: 'removed', lineNo: e.aIdx+1, text: aLines[e.aIdx] }); }
  }
  return result;
}

// ── Step editability (mirror of parser.ts logic) ──────────────────────
function classifyStep(step) {
  const supported = new Set(['agent','split','approval','foreach','workflow','wait_for','']);
  if (!supported.has(step.type || '')) { return 'unsupported'; }
  if (step.steps !== undefined || step.parallel !== undefined || step.for_each !== undefined) { return 'readonly'; }
  return 'editable';
}

// ── Form builder helpers ───────────────────────────────────────────────
function makeFormRow(label, id, inputType, value, hint, readonly) {
  const ro = readonly ? 'readonly disabled' : '';
  return \`<div class="form-row">
    <label for="\${escHtml(id)}">\${escHtml(label)}</label>
    <input type="\${escHtml(inputType)}" id="\${escHtml(id)}" value="\${escHtml(value)}" \${ro}>
    \${hint ? '<div class="hint">' + escHtml(hint) + '</div>' : ''}
  </div>\`;
}

function makeTextareaRow(label, id, value, hint, readonly) {
  const ro = readonly ? 'readonly disabled' : '';
  return \`<div class="form-row">
    <label for="\${escHtml(id)}">\${escHtml(label)}</label>
    <textarea id="\${escHtml(id)}" \${ro}>\${escHtml(value)}</textarea>
    \${hint ? '<div class="hint">' + escHtml(hint) + '</div>' : ''}
  </div>\`;
}

function makeSelectRow(label, id, options, selected, hint, readonly) {
  const ro = readonly ? 'disabled' : '';
  const opts = options.map(o => \`<option value="\${escHtml(o)}" \${o === selected ? 'selected' : ''}>\${escHtml(o)}</option>\`).join('');
  return \`<div class="form-row">
    <label for="\${escHtml(id)}">\${escHtml(label)}</label>
    <select id="\${escHtml(id)}" \${ro}>\${opts}</select>
    \${hint ? '<div class="hint">' + escHtml(hint) + '</div>' : ''}
  </div>\`;
}

function makeCheckboxRow(label, id, checked, hint, readonly) {
  const ro = readonly ? 'disabled' : '';
  return \`<div class="form-row" style="display:flex;align-items:center;gap:6px;">
    <input type="checkbox" id="\${escHtml(id)}" \${checked ? 'checked' : ''} \${ro}>
    <label for="\${escHtml(id)}" style="margin-bottom:0;">\${escHtml(label)}</label>
    \${hint ? '<div class="hint">' + escHtml(hint) + '</div>' : ''}
  </div>\`;
}

// ── Utilities ──────────────────────────────────────────────────────────
function escHtml(s) {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

function v(id) {
  const el = document.getElementById(id);
  return el ? el.value.trim() : '';
}

function splitCsv(id) {
  return v(id).split(',').map(s => s.trim()).filter(Boolean);
}

// ── Boot ───────────────────────────────────────────────────────────────
vscode.postMessage({ type: 'ready' });
</script>
</body>
</html>`;
  }

  dispose(): void {
    ApiaryEditorPanel.current = undefined;
    this.panel.dispose();
    for (const d of this.disposables) { d.dispose(); }
    this.disposables = [];
    if (this.debounceTimer !== undefined) { clearTimeout(this.debounceTimer); }
  }
}
