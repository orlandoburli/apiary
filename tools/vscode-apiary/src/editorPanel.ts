import * as vscode from 'vscode';
import { parseApiary, ApiaryConfig, Step, classifyStep } from './parser';
import { serializeApiary } from './serializer';
import { validateConfig } from './validator';
import { computeDiff } from './diff';
import { renderApiary, buildErrorSets } from './renderer';
import { isApiaryYaml } from './previewPanel';

function getNonce(): string {
  let text = '';
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
  for (let i = 0; i < 32; i++) {
    text += chars.charAt(Math.floor(Math.random() * chars.length));
  }
  return text;
}

// Message types from WebView → extension host
type WebViewMessage =
  | { type: 'ready' }
  | { type: 'nodeClick'; nodeId: string }
  | { type: 'nodeUpdate'; workflowId: string; stepId: string; stepData: Partial<Step> }
  | { type: 'addStep'; workflowId: string; stepType: string; insertAfterId?: string }
  | { type: 'deleteStep'; workflowId: string; stepId: string }
  | { type: 'requestDiff' }
  | { type: 'save' };

export class ApiaryEditorPanel {
  private static current: ApiaryEditorPanel | undefined;

  private readonly panel: vscode.WebviewPanel;
  private document: vscode.TextDocument;
  private config: ApiaryConfig;
  private originalText: string;
  private disposables: vscode.Disposable[] = [];
  private debounceTimer: ReturnType<typeof setTimeout> | undefined;

  static show(document: vscode.TextDocument): void {
    if (ApiaryEditorPanel.current) {
      ApiaryEditorPanel.current.panel.reveal(vscode.ViewColumn.Beside, true);
      ApiaryEditorPanel.current.loadDocument(document);
      return;
    }
    const panel = vscode.window.createWebviewPanel(
      'apiaryEditor',
      'Apiary Editor',
      { viewColumn: vscode.ViewColumn.Beside, preserveFocus: true },
      { enableScripts: true, retainContextWhenHidden: true },
    );
    ApiaryEditorPanel.current = new ApiaryEditorPanel(panel, document);
  }

  static get isOpen(): boolean {
    return ApiaryEditorPanel.current !== undefined;
  }

  private constructor(panel: vscode.WebviewPanel, document: vscode.TextDocument) {
    this.panel = panel;
    this.document = document;
    this.originalText = document.getText();
    this.config = this.parseSafe(this.originalText) ?? {};

    this.panel.webview.html = this.buildHtml();

    this.panel.webview.onDidReceiveMessage(
      (msg: WebViewMessage) => this.handleMessage(msg),
      null,
      this.disposables,
    );

    // Re-render when the YAML file changes externally
    this.disposables.push(
      vscode.workspace.onDidChangeTextDocument(e => {
        if (e.document.uri.toString() === this.document.uri.toString()) {
          this.scheduleExternalUpdate(e.document.getText());
        }
      }),
    );

    // Switch document when user navigates to another apiary.yaml
    this.disposables.push(
      vscode.window.onDidChangeActiveTextEditor(editor => {
        if (editor && isApiaryYaml(editor.document)) {
          this.loadDocument(editor.document);
          this.panel.title = `Apiary Editor — ${editor.document.fileName.split(/[\\/]/).pop()}`;
        }
      }),
    );

    this.panel.onDidDispose(() => this.dispose(), null, this.disposables);
  }

  private parseSafe(text: string): ApiaryConfig | undefined {
    try {
      return parseApiary(text);
    } catch {
      return undefined;
    }
  }

  private loadDocument(document: vscode.TextDocument): void {
    this.document = document;
    this.originalText = document.getText();
    this.config = this.parseSafe(this.originalText) ?? {};
    this.sendUpdate();
  }

  private scheduleExternalUpdate(text: string): void {
    if (this.debounceTimer !== undefined) { clearTimeout(this.debounceTimer); }
    this.debounceTimer = setTimeout(() => {
      this.originalText = text;
      this.config = this.parseSafe(text) ?? {};
      this.sendUpdate();
    }, 350);
  }

  private sendUpdate(): void {
    try {
      const errors = validateConfig(this.config);
      const { errorNodeIds, readOnlyNodeIds } = buildErrorSets(errors);
      const diagrams = renderApiary(this.config, {
        interactive: true,
        errorNodeIds,
        readOnlyNodeIds,
      });
      const agentIds = (this.config.agents ?? []).map(a => a.id);
      const workflowIds = (this.config.workflows ?? []).map(w => w.id);
      void this.panel.webview.postMessage({
        type: 'update',
        diagrams,
        errors,
        agentIds,
        workflowIds,
        isDirty: serializeApiary(this.config) !== this.originalText,
      });
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      void this.panel.webview.postMessage({ type: 'parseError', message });
    }
  }

  private sendNodeData(nodeId: string): void {
    // Parse workflowId and stepId from the node ID (format: N_<workflowId>_<stepId>)
    const raw = nodeId.replace(/^N_/, '');

    for (const wf of this.config.workflows ?? []) {
      const wfPrefix = wf.id.replace(/[^a-zA-Z0-9]/g, '_') + '_';
      if (raw.startsWith(wfPrefix)) {
        const stepRaw = raw.slice(wfPrefix.length);
        const step = (wf.steps ?? []).find(
          s => s.id.replace(/[^a-zA-Z0-9]/g, '_') === stepRaw,
        );
        if (step) {
          const editability = classifyStep(step);
          void this.panel.webview.postMessage({
            type: 'nodeData',
            workflowId: wf.id,
            stepId: step.id,
            step,
            editability,
            agentIds: (this.config.agents ?? []).map(a => a.id),
          });
          return;
        }
      }
    }
  }

  private applyNodeUpdate(workflowId: string, stepId: string, stepData: Partial<Step>): void {
    const wf = (this.config.workflows ?? []).find(w => w.id === workflowId);
    if (!wf) { return; }
    const stepIdx = (wf.steps ?? []).findIndex(s => s.id === stepId);
    if (stepIdx === -1) { return; }
    const existing = wf.steps![stepIdx];
    wf.steps![stepIdx] = { ...existing, ...stepData };
    this.sendUpdate();
  }

  private applyAddStep(workflowId: string, stepType: string, insertAfterId?: string): void {
    const wf = (this.config.workflows ?? []).find(w => w.id === workflowId);
    if (!wf) { return; }
    if (!wf.steps) { wf.steps = []; }

    const newStep: Step = {
      id: `step_${Date.now()}`,
      type: stepType === 'agent' ? undefined : stepType,
    };
    if (!stepType || stepType === 'agent') {
      newStep.agent = '';
      newStep.prompt = '';
    }

    if (insertAfterId) {
      const idx = wf.steps.findIndex(s => s.id === insertAfterId);
      if (idx !== -1) {
        wf.steps.splice(idx + 1, 0, newStep);
      } else {
        wf.steps.push(newStep);
      }
    } else {
      wf.steps.push(newStep);
    }
    this.sendUpdate();
  }

  private applyDeleteStep(workflowId: string, stepId: string): void {
    const wf = (this.config.workflows ?? []).find(w => w.id === workflowId);
    if (!wf?.steps) { return; }
    wf.steps = wf.steps.filter(s => s.id !== stepId);
    this.sendUpdate();
  }

  private async saveToFile(): Promise<void> {
    const newYaml = serializeApiary(this.config);
    const edit = new vscode.WorkspaceEdit();
    const fullRange = new vscode.Range(
      this.document.positionAt(0),
      this.document.positionAt(this.document.getText().length),
    );
    edit.replace(this.document.uri, fullRange, newYaml);
    const ok = await vscode.workspace.applyEdit(edit);
    if (ok) {
      this.originalText = newYaml;
      void this.panel.webview.postMessage({ type: 'saveOk' });
    } else {
      void this.panel.webview.postMessage({
        type: 'saveError',
        message: 'Failed to apply edit to document.',
      });
    }
  }

  private handleMessage(msg: WebViewMessage): void {
    switch (msg.type) {
      case 'ready':
        this.sendUpdate();
        break;

      case 'nodeClick':
        this.sendNodeData(msg.nodeId);
        break;

      case 'nodeUpdate':
        this.applyNodeUpdate(msg.workflowId, msg.stepId, msg.stepData);
        break;

      case 'addStep':
        this.applyAddStep(msg.workflowId, msg.stepType, msg.insertAfterId);
        break;

      case 'deleteStep':
        this.applyDeleteStep(msg.workflowId, msg.stepId);
        break;

      case 'requestDiff': {
        const newYaml = serializeApiary(this.config);
        const diff = computeDiff(this.originalText, newYaml);
        void this.panel.webview.postMessage({ type: 'diffResult', diff });
        break;
      }

      case 'save':
        void this.saveToFile();
        break;
    }
  }

  private buildHtml(): string {
    const nonce = getNonce();
    return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta http-equiv="Content-Security-Policy"
        content="default-src 'none';
                 script-src https://cdn.jsdelivr.net 'nonce-${nonce}';
                 style-src 'unsafe-inline';
                 img-src data: blob:;">
  <meta name="viewport" content="width=device-width,initial-scale=1.0">
  <title>Apiary Editor</title>
  <style>
    *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }

    body {
      background: var(--vscode-editor-background);
      color: var(--vscode-editor-foreground);
      font-family: var(--vscode-font-family, sans-serif);
      font-size: var(--vscode-font-size, 13px);
      display: flex;
      flex-direction: column;
      height: 100vh;
      overflow: hidden;
    }

    /* ── Toolbar ── */
    #toolbar {
      display: flex;
      align-items: center;
      gap: 8px;
      padding: 6px 12px;
      border-bottom: 1px solid var(--vscode-widget-border, #444);
      flex-shrink: 0;
      background: var(--vscode-editorGroupHeader-tabsBackground, #1e1e1e);
    }
    #toolbar .title {
      font-weight: 600;
      font-size: 12px;
      text-transform: uppercase;
      letter-spacing: 0.06em;
      color: var(--vscode-descriptionForeground);
      flex: 1;
    }
    #dirty-badge {
      display: none;
      font-size: 11px;
      color: var(--vscode-notificationsWarningIcon-foreground, #cca700);
    }
    #dirty-badge.visible { display: inline; }

    button {
      padding: 3px 10px;
      border-radius: 3px;
      font-size: 12px;
      cursor: pointer;
      border: 1px solid var(--vscode-button-border, transparent);
      background: var(--vscode-button-secondaryBackground, #3a3a3a);
      color: var(--vscode-button-secondaryForeground, #ccc);
    }
    button:hover { opacity: 0.85; }
    button.primary {
      background: var(--vscode-button-background, #0e639c);
      color: var(--vscode-button-foreground, #fff);
    }
    button:disabled { opacity: 0.4; cursor: default; }

    /* ── Main split ── */
    #main {
      display: flex;
      flex: 1;
      overflow: hidden;
    }

    /* ── Diagram panel ── */
    #diagram-panel {
      flex: 1;
      overflow: auto;
      padding: 12px 16px;
      border-right: 1px solid var(--vscode-widget-border, #444);
    }

    .wf-title {
      margin: 18px 0 8px;
      padding-bottom: 4px;
      font-size: 11px;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.07em;
      color: var(--vscode-descriptionForeground);
      border-bottom: 1px solid var(--vscode-widget-border, #444);
    }
    .wf-title:first-child { margin-top: 0; }

    .diagram-wrap { overflow-x: auto; }
    .diagram-wrap svg { display: block; max-width: 100%; }

    /* ── Edit sidebar ── */
    #edit-panel {
      width: 320px;
      flex-shrink: 0;
      overflow-y: auto;
      padding: 12px;
      display: flex;
      flex-direction: column;
      gap: 10px;
    }

    .panel-heading {
      font-size: 11px;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.07em;
      color: var(--vscode-descriptionForeground);
      padding-bottom: 6px;
      border-bottom: 1px solid var(--vscode-widget-border, #444);
    }

    .empty-hint {
      font-size: 12px;
      color: var(--vscode-descriptionForeground);
      padding: 16px 0;
      text-align: center;
    }

    /* ── Form elements ── */
    .field { display: flex; flex-direction: column; gap: 4px; }
    label {
      font-size: 11px;
      font-weight: 600;
      color: var(--vscode-descriptionForeground);
      text-transform: uppercase;
      letter-spacing: 0.05em;
    }
    input, select, textarea {
      background: var(--vscode-input-background, #3c3c3c);
      color: var(--vscode-input-foreground, #ccc);
      border: 1px solid var(--vscode-input-border, #555);
      border-radius: 2px;
      padding: 4px 6px;
      font-size: 12px;
      font-family: inherit;
      width: 100%;
    }
    input:focus, select:focus, textarea:focus {
      outline: 1px solid var(--vscode-focusBorder, #007acc);
    }
    textarea { resize: vertical; min-height: 80px; font-family: var(--vscode-editor-font-family, monospace); }
    select option { background: var(--vscode-dropdown-background, #3c3c3c); }

    .form-actions {
      display: flex;
      gap: 6px;
      flex-wrap: wrap;
    }

    /* ── Read-only badge ── */
    .readonly-notice {
      padding: 6px 10px;
      border-radius: 3px;
      font-size: 11px;
      background: var(--vscode-inputValidation-warningBackground, #352a05);
      border: 1px solid var(--vscode-inputValidation-warningBorder, #b89500);
      color: var(--vscode-inputValidation-warningForeground, #cca700);
    }

    /* ── Passthrough notice ── */
    .passthrough-notice {
      padding: 6px 10px;
      border-radius: 3px;
      font-size: 11px;
      background: var(--vscode-inputValidation-infoBackground, #063b49);
      border: 1px solid var(--vscode-inputValidation-infoBorder, #007acc);
      color: var(--vscode-inputValidation-infoForeground, #75beff);
    }

    /* ── Error list ── */
    #error-list {
      border-top: 1px solid var(--vscode-widget-border, #444);
      padding-top: 10px;
    }
    .error-item {
      font-size: 11px;
      padding: 3px 6px;
      border-left: 3px solid;
      margin-bottom: 4px;
    }
    .error-item.error { border-color: #be1100; color: var(--vscode-errorForeground, #f48771); }
    .error-item.warning { border-color: #cca700; color: var(--vscode-inputValidation-warningForeground, #cca700); }

    /* ── Workflow selector ── */
    #wf-selector { font-size: 12px; }

    /* ── Status bar ── */
    #status-bar {
      font-size: 11px;
      padding: 3px 12px;
      background: var(--vscode-statusBar-background, #007acc);
      color: var(--vscode-statusBar-foreground, #fff);
      flex-shrink: 0;
      display: flex;
      gap: 12px;
    }

    /* ── Parse error ── */
    .parse-error {
      padding: 12px;
      font-size: 12px;
      white-space: pre-wrap;
      word-break: break-word;
      background: var(--vscode-inputValidation-errorBackground, #5a1d1d);
      border: 1px solid var(--vscode-inputValidation-errorBorder, #be1100);
      color: var(--vscode-errorForeground, #f48771);
      border-radius: 3px;
      margin: 12px;
    }

    /* ── Diff modal ── */
    #diff-modal {
      display: none;
      position: fixed; inset: 0;
      background: rgba(0,0,0,0.6);
      z-index: 100;
      align-items: center;
      justify-content: center;
    }
    #diff-modal.visible { display: flex; }
    #diff-box {
      background: var(--vscode-editor-background);
      border: 1px solid var(--vscode-widget-border, #555);
      border-radius: 4px;
      width: 80vw;
      max-height: 80vh;
      display: flex;
      flex-direction: column;
    }
    #diff-box header {
      padding: 8px 12px;
      font-weight: 600;
      font-size: 12px;
      border-bottom: 1px solid var(--vscode-widget-border, #555);
      display: flex;
      align-items: center;
      gap: 8px;
    }
    #diff-box header span { flex: 1; }
    #diff-content {
      overflow: auto;
      flex: 1;
      padding: 8px;
      font-family: var(--vscode-editor-font-family, monospace);
      font-size: 12px;
      white-space: pre;
    }
    .diff-add { color: #4ec9b0; }
    .diff-del { color: #f48771; }
    .diff-hdr { color: var(--vscode-descriptionForeground); }
    #diff-box footer {
      padding: 6px 12px;
      border-top: 1px solid var(--vscode-widget-border, #555);
      display: flex; gap: 8px; justify-content: flex-end;
    }
  </style>
</head>
<body>
  <!-- ── Toolbar ── -->
  <div id="toolbar">
    <span class="title">Apiary Editor</span>
    <span id="dirty-badge">● unsaved</span>
    <button id="btn-add-step" disabled>+ Add Step</button>
    <button id="btn-diff" disabled>Preview Diff</button>
    <button id="btn-save" class="primary" disabled>Save</button>
  </div>

  <!-- ── Main area ── -->
  <div id="main">
    <!-- Diagram -->
    <div id="diagram-panel">
      <div class="empty-hint">Loading…</div>
    </div>

    <!-- Edit sidebar -->
    <div id="edit-panel">
      <div class="panel-heading">Inspector</div>
      <div class="empty-hint" id="inspector-hint">Click a node in the diagram to inspect or edit it.</div>
      <div id="inspector-body" style="display:none;"></div>
      <div id="error-list" style="display:none;"></div>
    </div>
  </div>

  <!-- ── Status bar ── -->
  <div id="status-bar">
    <span id="status-errors">0 errors</span>
    <span id="status-warnings">0 warnings</span>
    <span style="flex:1"></span>
    <span id="status-note">⚠ Comments are not preserved in round-trip edits</span>
  </div>

  <!-- ── Diff modal ── -->
  <div id="diff-modal">
    <div id="diff-box">
      <header><span>Semantic diff</span><button id="btn-close-diff">✕</button></header>
      <div id="diff-content"></div>
      <footer>
        <button id="btn-diff-save" class="primary">Save now</button>
        <button id="btn-close-diff2">Cancel</button>
      </footer>
    </div>
  </div>

  <script nonce="${nonce}" src="https://cdn.jsdelivr.net/npm/mermaid@10/dist/mermaid.min.js"></script>
  <script nonce="${nonce}">
  (function() {
    const vscode = acquireVsCodeApi();

    // ─── State ───────────────────────────────────────────────────────────────
    let currentErrors = [];
    let currentAgentIds = [];
    let currentWorkflowIds = [];
    let currentSelectedNode = null; // { workflowId, stepId, step, editability }
    let currentDiagramIds = [];     // workflow IDs in diagram order
    let isDirty = false;

    const isDark = document.body.classList.contains('vscode-dark') ||
                   document.body.classList.contains('vscode-high-contrast');

    mermaid.initialize({
      startOnLoad: false,
      theme: isDark ? 'dark' : 'default',
      flowchart: { curve: 'basis', useMaxWidth: true, htmlLabels: false },
      securityLevel: 'loose',
      logLevel: 'error',
    });

    // ─── Mermaid click callback (global, called by Mermaid click directive) ──
    window.handleNodeClick = function(nodeId) {
      vscode.postMessage({ type: 'nodeClick', nodeId });
    };

    // ─── DOM refs ────────────────────────────────────────────────────────────
    const diagramPanel  = document.getElementById('diagram-panel');
    const inspectorHint = document.getElementById('inspector-hint');
    const inspectorBody = document.getElementById('inspector-body');
    const errorListEl   = document.getElementById('error-list');
    const dirtyBadge    = document.getElementById('dirty-badge');
    const btnAddStep    = document.getElementById('btn-add-step');
    const btnDiff       = document.getElementById('btn-diff');
    const btnSave       = document.getElementById('btn-save');
    const statusErrors  = document.getElementById('status-errors');
    const statusWarnings= document.getElementById('status-warnings');
    const diffModal     = document.getElementById('diff-modal');
    const diffContent   = document.getElementById('diff-content');

    document.getElementById('btn-close-diff').onclick  = () => diffModal.classList.remove('visible');
    document.getElementById('btn-close-diff2').onclick = () => diffModal.classList.remove('visible');
    document.getElementById('btn-diff-save').onclick   = () => {
      diffModal.classList.remove('visible');
      vscode.postMessage({ type: 'save' });
    };

    btnDiff.onclick = () => vscode.postMessage({ type: 'requestDiff' });
    btnSave.onclick = () => vscode.postMessage({ type: 'save' });

    btnAddStep.onclick = () => {
      const wfId = currentDiagramIds[0];
      if (!wfId) { return; }
      const afterId = currentSelectedNode?.stepId;
      const stepType = prompt('Step type (agent / split / approval / foreach / workflow / wait_for / parallel):', 'agent');
      if (!stepType) { return; }
      vscode.postMessage({ type: 'addStep', workflowId: wfId, stepType, insertAfterId: afterId });
    };

    // ─── Render diagrams ─────────────────────────────────────────────────────
    async function renderDiagrams(diagrams) {
      diagramPanel.innerHTML = '';
      currentDiagramIds = diagrams.map(d => d.title.split(' — ')[0].trim());

      for (let i = 0; i < diagrams.length; i++) {
        const d = diagrams[i];
        const h2 = document.createElement('div');
        h2.className = 'wf-title';
        h2.textContent = d.title;
        diagramPanel.appendChild(h2);

        const wrap = document.createElement('div');
        wrap.className = 'diagram-wrap';

        const div = document.createElement('div');
        div.className = 'mermaid';
        div.id = 'md-' + i;
        div.textContent = d.diagram;

        wrap.appendChild(div);
        diagramPanel.appendChild(wrap);
      }

      try {
        await mermaid.run({ querySelector: '.mermaid' });
      } catch (e) {
        console.error('mermaid render error', e);
      }
    }

    // ─── Error list ──────────────────────────────────────────────────────────
    function renderErrors(errors) {
      const errCount = errors.filter(e => e.severity === 'error').length;
      const warnCount = errors.filter(e => e.severity === 'warning').length;
      statusErrors.textContent   = errCount + ' error' + (errCount !== 1 ? 's' : '');
      statusWarnings.textContent = warnCount + ' warning' + (warnCount !== 1 ? 's' : '');

      if (errors.length === 0) {
        errorListEl.style.display = 'none';
        return;
      }
      errorListEl.style.display = 'block';
      errorListEl.innerHTML = '<div class="panel-heading" style="margin-bottom:6px">Validation</div>';
      for (const e of errors) {
        const div = document.createElement('div');
        div.className = 'error-item ' + e.severity;
        div.textContent = e.message;
        errorListEl.appendChild(div);
      }
    }

    // ─── Inspector / step form ───────────────────────────────────────────────
    function showInspector(data) {
      currentSelectedNode = data;
      inspectorHint.style.display = 'none';
      inspectorBody.style.display = 'block';
      inspectorBody.innerHTML = '';

      const { workflowId, stepId, step, editability } = data;
      const type = step.type || 'agent';

      // Heading
      const heading = document.createElement('div');
      heading.className = 'panel-heading';
      heading.textContent = stepId + ' · ' + type;
      inspectorBody.appendChild(heading);

      if (editability.readOnly) {
        const notice = document.createElement('div');
        notice.className = 'readonly-notice';
        notice.textContent = 'This step uses unsupported features and is displayed read-only. Edit the YAML directly.';
        inspectorBody.appendChild(notice);
        // Show raw YAML of the step
        const pre = document.createElement('textarea');
        pre.readOnly = true;
        pre.style.height = '200px';
        pre.value = JSON.stringify(step, null, 2);
        inspectorBody.appendChild(pre);
        return;
      }

      if (editability.passthroughFields.length > 0) {
        const notice = document.createElement('div');
        notice.className = 'passthrough-notice';
        notice.textContent = 'Fields preserved but not shown in form: ' + editability.passthroughFields.join(', ');
        inspectorBody.appendChild(notice);
      }

      const form = buildStepForm(workflowId, stepId, step, type, editability);
      inspectorBody.appendChild(form);

      // Delete button
      const delBtn = document.createElement('button');
      delBtn.textContent = 'Delete step';
      delBtn.style.marginTop = '8px';
      delBtn.onclick = () => {
        if (confirm('Delete step "' + stepId + '"?')) {
          vscode.postMessage({ type: 'deleteStep', workflowId, stepId });
          hideInspector();
        }
      };
      inspectorBody.appendChild(delBtn);
    }

    function hideInspector() {
      currentSelectedNode = null;
      inspectorHint.style.display = '';
      inspectorBody.style.display = 'none';
      inspectorBody.innerHTML = '';
    }

    function field(labelText, inputEl) {
      const wrap = document.createElement('div');
      wrap.className = 'field';
      const lbl = document.createElement('label');
      lbl.textContent = labelText;
      wrap.appendChild(lbl);
      wrap.appendChild(inputEl);
      return wrap;
    }

    function textInput(value, placeholder) {
      const el = document.createElement('input');
      el.type = 'text';
      el.value = value || '';
      el.placeholder = placeholder || '';
      return el;
    }

    function textArea(value, placeholder) {
      const el = document.createElement('textarea');
      el.value = value || '';
      el.placeholder = placeholder || '';
      return el;
    }

    function agentSelect(value) {
      const el = document.createElement('select');
      const empty = document.createElement('option');
      empty.value = ''; empty.textContent = '— select agent —';
      el.appendChild(empty);
      for (const id of currentAgentIds) {
        const opt = document.createElement('option');
        opt.value = id; opt.textContent = id;
        if (id === value) { opt.selected = true; }
        el.appendChild(opt);
      }
      return el;
    }

    function buildStepForm(workflowId, stepId, step, type, editability) {
      const form = document.createElement('div');
      form.style.display = 'flex';
      form.style.flexDirection = 'column';
      form.style.gap = '8px';

      // Common fields
      const idInput = textInput(step.id, 'step-id');
      form.appendChild(field('ID', idInput));

      const ifInput = textInput(step.if, 'condition expression');
      form.appendChild(field('Condition (if)', ifInput));

      // Type-specific fields
      if (!type || type === 'agent') {
        const agentEl = agentSelect(step.agent);
        form.appendChild(field('Agent', agentEl));

        const promptEl = textArea(step.prompt, 'Agent prompt…');
        form.appendChild(field('Prompt', promptEl));

        const summaryEl = textArea(step.summary_prompt, 'Summary prompt…');
        form.appendChild(field('Summary Prompt', summaryEl));

        const rejectEl = textInput(step.reject_when, 'rejection condition');
        form.appendChild(field('Reject When', rejectEl));

        if (step.on_reject) {
          const restartEl = textInput(step.on_reject.restart_from, 'step-id');
          form.appendChild(field('Restart From', restartEl));
          const maxEl = textInput(step.on_reject.max != null ? String(step.on_reject.max) : '', 'max retries');
          form.appendChild(field('Max Retries', maxEl));
        }
      }

      if (type === 'approval') {
        const msgEl = textArea(step.message, 'Approval message…');
        form.appendChild(field('Message', msgEl));

        const timeoutEl = textInput(step.timeout, 'e.g. 24h');
        form.appendChild(field('Timeout', timeoutEl));
      }

      if (type === 'foreach') {
        const itemsEl = textInput(step.items, 'expression resolving to array');
        form.appendChild(field('Items', itemsEl));

        const asEl = textInput(step.as, 'variable name');
        form.appendChild(field('As', asEl));

        const concurrencyEl = textInput(step.concurrency != null ? String(step.concurrency) : '', 'max concurrent');
        form.appendChild(field('Concurrency', concurrencyEl));
      }

      if (type === 'workflow') {
        const refEl = textInput(step.workflow ?? step.uses, 'workflow-id or ./path.yaml');
        form.appendChild(field('Workflow / Uses', refEl));
      }

      if (type === 'wait_for') {
        const kindEl = textInput(step.wait_for?.kind, 'ci | dependency');
        form.appendChild(field('Kind', kindEl));

        const timeoutEl = textInput(step.wait_for?.timeout, 'e.g. 30m');
        form.appendChild(field('Timeout', timeoutEl));
      }

      if (type === 'parallel') {
        const joinEl = textInput(step.join, 'all | any | expression');
        form.appendChild(field('Join Policy', joinEl));
      }

      // Save button
      const actions = document.createElement('div');
      actions.className = 'form-actions';

      const saveBtn = document.createElement('button');
      saveBtn.className = 'primary';
      saveBtn.textContent = 'Apply';
      saveBtn.onclick = () => {
        const updated = collectFormData(form, step, type);
        vscode.postMessage({ type: 'nodeUpdate', workflowId, stepId, stepData: updated });
      };
      actions.appendChild(saveBtn);

      const cancelBtn = document.createElement('button');
      cancelBtn.textContent = 'Deselect';
      cancelBtn.onclick = hideInspector;
      actions.appendChild(cancelBtn);

      form.appendChild(actions);
      return form;
    }

    function collectFormData(form, originalStep, type) {
      const inputs = form.querySelectorAll('input, textarea, select');
      const data = {};
      const labels = form.querySelectorAll('label');
      const labelMap = {};
      labels.forEach((lbl, i) => {
        const inp = inputs[i];
        if (inp) { labelMap[lbl.textContent] = inp; }
      });

      function get(labelText) {
        const el = labelMap[labelText];
        return el ? el.value.trim() : undefined;
      }
      function getOrUndef(labelText) {
        const v = get(labelText);
        return v === '' ? undefined : v;
      }

      const id = get('ID') || originalStep.id;
      const ifCond = getOrUndef('Condition (if)');
      const result = { ...originalStep, id, if: ifCond };

      if (!type || type === 'agent') {
        result.agent = get('Agent') || undefined;
        result.prompt = get('Prompt') || undefined;
        result.summary_prompt = getOrUndef('Summary Prompt');
        result.reject_when = getOrUndef('Reject When');
        const restartFrom = getOrUndef('Restart From');
        const maxRetries = getOrUndef('Max Retries');
        if (restartFrom || maxRetries) {
          result.on_reject = { restart_from: restartFrom, max: maxRetries ? parseInt(maxRetries, 10) : undefined };
        } else {
          result.on_reject = undefined;
        }
      }

      if (type === 'approval') {
        result.message = getOrUndef('Message');
        result.timeout = getOrUndef('Timeout');
      }

      if (type === 'foreach') {
        result.items = getOrUndef('Items');
        result.as = getOrUndef('As');
        const concurr = getOrUndef('Concurrency');
        result.concurrency = concurr ? parseInt(concurr, 10) : undefined;
      }

      if (type === 'workflow') {
        const ref = getOrUndef('Workflow / Uses');
        result.workflow = ref;
        result.uses = undefined;
      }

      if (type === 'wait_for') {
        result.wait_for = {
          ...(originalStep.wait_for || {}),
          kind: getOrUndef('Kind'),
          timeout: getOrUndef('Timeout'),
        };
      }

      if (type === 'parallel') {
        result.join = getOrUndef('Join Policy');
      }

      return result;
    }

    // ─── Diff modal ──────────────────────────────────────────────────────────
    function showDiff(diff) {
      if (!diff.hasChanges) {
        diffContent.innerHTML = '<span style="color:var(--vscode-descriptionForeground)">No semantic changes.</span>';
      } else {
        diffContent.innerHTML = '';
        const lines = diff.unifiedDiff.split('\\n');
        for (const line of lines) {
          const span = document.createElement('span');
          if (line.startsWith('+') && !line.startsWith('+++')) {
            span.className = 'diff-add';
          } else if (line.startsWith('-') && !line.startsWith('---')) {
            span.className = 'diff-del';
          } else if (line.startsWith('@@')) {
            span.className = 'diff-hdr';
          }
          span.textContent = line + '\\n';
          diffContent.appendChild(span);
        }
      }
      diffModal.classList.add('visible');
    }

    // ─── Dirty state ─────────────────────────────────────────────────────────
    function setDirty(dirty) {
      isDirty = dirty;
      dirtyBadge.classList.toggle('visible', dirty);
      btnSave.disabled = !dirty;
      btnDiff.disabled = !dirty;
    }

    // ─── Message handler ─────────────────────────────────────────────────────
    window.addEventListener('message', async event => {
      const msg = event.data;

      if (msg.type === 'update') {
        currentErrors = msg.errors || [];
        currentAgentIds = msg.agentIds || [];
        currentWorkflowIds = msg.workflowIds || [];
        setDirty(msg.isDirty || false);
        btnAddStep.disabled = (msg.diagrams || []).length === 0;
        await renderDiagrams(msg.diagrams || []);
        renderErrors(currentErrors);
      }

      if (msg.type === 'parseError') {
        diagramPanel.innerHTML = '<div class="parse-error">Parse error: ' + escHtml(msg.message) + '</div>';
        setDirty(false);
        btnAddStep.disabled = true;
        statusErrors.textContent = '1 parse error';
        statusWarnings.textContent = '';
      }

      if (msg.type === 'nodeData') {
        showInspector(msg);
      }

      if (msg.type === 'diffResult') {
        showDiff(msg.diff);
      }

      if (msg.type === 'saveOk') {
        setDirty(false);
        vscode.postMessage({ type: 'ready' }); // refresh after save
      }

      if (msg.type === 'saveError') {
        alert('Save failed: ' + msg.message);
      }
    });

    function escHtml(str) {
      return str.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
    }

    vscode.postMessage({ type: 'ready' });
  })();
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
