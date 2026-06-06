import * as vscode from 'vscode';
import { parseApiary } from './parser';
import { renderApiary } from './renderer';

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

export class ApiaryPreviewPanel {
  private static current: ApiaryPreviewPanel | undefined;

  private readonly panel: vscode.WebviewPanel;
  private disposables: vscode.Disposable[] = [];
  private debounceTimer: ReturnType<typeof setTimeout> | undefined;

  static show(document: vscode.TextDocument): void {
    if (ApiaryPreviewPanel.current) {
      ApiaryPreviewPanel.current.panel.reveal(vscode.ViewColumn.Beside, true);
      ApiaryPreviewPanel.current.update(document.getText());
      return;
    }
    const panel = vscode.window.createWebviewPanel(
      'apiaryPreview',
      'Apiary Preview',
      { viewColumn: vscode.ViewColumn.Beside, preserveFocus: true },
      { enableScripts: true, retainContextWhenHidden: true }
    );
    ApiaryPreviewPanel.current = new ApiaryPreviewPanel(panel, document);
  }

  static get isOpen(): boolean {
    return ApiaryPreviewPanel.current !== undefined;
  }

  private constructor(panel: vscode.WebviewPanel, document: vscode.TextDocument) {
    this.panel = panel;
    this.panel.webview.html = this.buildHtml();

    // Send initial content once the webview signals it is ready
    this.panel.webview.onDidReceiveMessage(msg => {
      if (msg.type === 'ready') {
        this.update(document.getText());
      }
    }, null, this.disposables);

    // Re-render on every keystroke (debounced)
    this.disposables.push(
      vscode.workspace.onDidChangeTextDocument(e => {
        if (isApiaryYaml(e.document)) {
          this.scheduleUpdate(e.document.getText());
        }
      })
    );

    // Track active editor switches between apiary.yaml files
    this.disposables.push(
      vscode.window.onDidChangeActiveTextEditor(editor => {
        if (editor && isApiaryYaml(editor.document)) {
          this.update(editor.document.getText());
          this.panel.title = `Apiary — ${editor.document.fileName.split(/[\\/]/).pop()}`;
        }
      })
    );

    this.panel.onDidDispose(() => this.dispose(), null, this.disposables);
  }

  private scheduleUpdate(text: string): void {
    if (this.debounceTimer !== undefined) { clearTimeout(this.debounceTimer); }
    this.debounceTimer = setTimeout(() => this.update(text), 350);
  }

  private update(text: string): void {
    try {
      const config = parseApiary(text);
      const diagrams = renderApiary(config);
      void this.panel.webview.postMessage({ type: 'update', diagrams });
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      void this.panel.webview.postMessage({ type: 'update', error: message });
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
  <title>Apiary Preview</title>
  <style>
    *, *::before, *::after { box-sizing: border-box; }
    body {
      margin: 0;
      padding: 14px 18px;
      background: var(--vscode-editor-background);
      color: var(--vscode-editor-foreground);
      font-family: var(--vscode-font-family, sans-serif);
      font-size: var(--vscode-font-size, 13px);
    }
    h2 {
      margin: 22px 0 10px;
      padding-bottom: 5px;
      font-size: 11px;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.07em;
      color: var(--vscode-descriptionForeground);
      border-bottom: 1px solid var(--vscode-widget-border, #444);
    }
    h2:first-of-type { margin-top: 0; }
    .diagram-wrap { overflow-x: auto; }
    .diagram-wrap svg { display: block; max-width: 100%; }
    .error {
      margin: 8px 0;
      padding: 8px 12px;
      border-radius: 3px;
      font-size: 12px;
      background: var(--vscode-inputValidation-errorBackground, #5a1d1d);
      border: 1px solid var(--vscode-inputValidation-errorBorder, #be1100);
      color: var(--vscode-errorForeground, #f48771);
      white-space: pre-wrap;
      word-break: break-word;
    }
    .empty {
      padding: 32px 0;
      text-align: center;
      font-size: 12px;
      color: var(--vscode-descriptionForeground);
    }
  </style>
</head>
<body>
  <div id="root"><div class="empty">Loading…</div></div>

  <script nonce="${nonce}" src="https://cdn.jsdelivr.net/npm/mermaid@10/dist/mermaid.min.js"></script>
  <script nonce="${nonce}">
    const vscode = acquireVsCodeApi();
    const root = document.getElementById('root');

    const isDark = document.body.classList.contains('vscode-dark') ||
                   document.body.classList.contains('vscode-high-contrast');

    mermaid.initialize({
      startOnLoad: false,
      theme: isDark ? 'dark' : 'default',
      flowchart: { curve: 'basis', useMaxWidth: true, htmlLabels: false },
      securityLevel: 'loose',
      logLevel: 'error',
    });

    async function render(diagrams) {
      root.innerHTML = '';
      for (let i = 0; i < diagrams.length; i++) {
        const d = diagrams[i];

        const h2 = document.createElement('h2');
        h2.textContent = d.title;
        root.appendChild(h2);

        const wrap = document.createElement('div');
        wrap.className = 'diagram-wrap';

        const div = document.createElement('div');
        div.className = 'mermaid';
        div.id = 'md-' + i;
        div.textContent = d.diagram;

        wrap.appendChild(div);
        root.appendChild(wrap);
      }
      try {
        await mermaid.run({ querySelector: '.mermaid' });
      } catch (e) {
        console.error('mermaid render error', e);
      }
    }

    window.addEventListener('message', event => {
      const msg = event.data;
      if (msg.type !== 'update') { return; }

      root.innerHTML = '';

      if (msg.error) {
        const div = document.createElement('div');
        div.className = 'error';
        div.textContent = msg.error;
        root.appendChild(div);
        return;
      }

      if (!msg.diagrams || msg.diagrams.length === 0) {
        const div = document.createElement('div');
        div.className = 'empty';
        div.textContent = 'No workflows defined in this file.';
        root.appendChild(div);
        return;
      }

      render(msg.diagrams);
    });

    vscode.postMessage({ type: 'ready' });
  </script>
</body>
</html>`;
  }

  dispose(): void {
    ApiaryPreviewPanel.current = undefined;
    this.panel.dispose();
    for (const d of this.disposables) { d.dispose(); }
    this.disposables = [];
    if (this.debounceTimer !== undefined) { clearTimeout(this.debounceTimer); }
  }
}
