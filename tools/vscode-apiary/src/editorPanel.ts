import * as cp from 'child_process';
import * as vscode from 'vscode';
import { analyzeApiary, applyWorkflowEdit, semanticDiff, WorkflowEdit } from './documentModel';
import { renderApiary } from './renderer';
import { buildEditorHtml } from './webview';

function getNonce(): string {
  let text = '';
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
  for (let i = 0; i < 32; i++) { text += chars.charAt(Math.floor(Math.random() * chars.length)); }
  return text;
}

export function isApiaryYaml(document: vscode.TextDocument): boolean {
  if (document.languageId !== 'yaml') { return false; }
  const name = document.fileName.split(/[\\/]/).pop() ?? '';
  return name === 'apiary.yaml' || name.endsWith('.apiary.yaml');
}

export class ApiaryEditorPanel {
  private static current: ApiaryEditorPanel | undefined;

  private readonly panel: vscode.WebviewPanel;
  private readonly disposables: vscode.Disposable[] = [];
  private document: vscode.TextDocument;
  private originalText: string;
  private currentText: string;

  static show(document: vscode.TextDocument): void {
    if (ApiaryEditorPanel.current) {
      ApiaryEditorPanel.current.panel.reveal(vscode.ViewColumn.Beside, true);
      ApiaryEditorPanel.current.loadDocument(document);
      return;
    }
    const panel = vscode.window.createWebviewPanel(
      'apiaryEditor',
      `Apiary Editor — ${document.fileName.split(/[\\/]/).pop()}`,
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
    this.currentText = this.originalText;
    this.panel.webview.html = buildEditorHtml(getNonce());

    this.panel.webview.onDidReceiveMessage(msg => {
      switch (msg.type) {
        case 'ready': this.sendState(); break;
        case 'edit': this.handleEdit(msg.edit as WorkflowEdit); break;
        case 'save': void this.handleSave(); break;
        case 'reset': this.handleReset(); break;
        case 'validate': this.handleCli(false); break;
        case 'dryRun': this.handleCli(true); break;
      }
    }, null, this.disposables);

    // Reload when the file is saved externally (resets pending edits)
    this.disposables.push(
      vscode.workspace.onDidChangeTextDocument(e => {
        if (e.document === this.document && e.contentChanges.length > 0) {
          this.originalText = this.document.getText();
          this.currentText = this.originalText;
          this.sendState();
        }
      }),
    );

    // Follow active editor switches to other apiary.yaml files
    this.disposables.push(
      vscode.window.onDidChangeActiveTextEditor(editor => {
        if (editor && isApiaryYaml(editor.document)) {
          this.loadDocument(editor.document);
        }
      }),
    );

    this.panel.onDidDispose(() => this.dispose(), null, this.disposables);
  }

  private loadDocument(doc: vscode.TextDocument): void {
    this.document = doc;
    this.originalText = doc.getText();
    this.currentText = this.originalText;
    this.panel.title = `Apiary Editor — ${doc.fileName.split(/[\\/]/).pop()}`;
    this.sendState();
  }

  private sendState(): void {
    try {
      const original = analyzeApiary(this.originalText);
      const current = analyzeApiary(this.currentText);
      const diagrams = renderApiary(current.config);
      const changes = semanticDiff(original.config, current.config);
      void this.panel.webview.postMessage({
        type: 'update',
        analysis: current,
        diagrams,
        yaml: this.currentText,
        changes,
      });
    } catch (err) {
      void this.panel.webview.postMessage({
        type: 'update',
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }

  private handleEdit(edit: WorkflowEdit): void {
    try {
      const { analysis } = applyWorkflowEdit(this.currentText, edit);
      this.currentText = analysis.text;
      this.sendState();
    } catch (err) {
      void this.panel.webview.postMessage({
        type: 'actionError',
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }

  private async handleSave(): Promise<void> {
    const doc = this.document;
    const fullRange = new vscode.Range(doc.positionAt(0), doc.positionAt(doc.getText().length));
    const edit = new vscode.WorkspaceEdit();
    edit.replace(doc.uri, fullRange, this.currentText);
    await vscode.workspace.applyEdit(edit);
    await doc.save();
    this.originalText = this.currentText;
    this.sendState();
  }

  private handleReset(): void {
    this.currentText = this.originalText;
    this.sendState();
  }

  private handleCli(dryRun: boolean): void {
    const config = vscode.workspace.getConfiguration('apiary');
    const exe = config.get<string>('executablePath', 'apiary');
    const args = dryRun
      ? ['run', '--dry-run', '--config', this.document.uri.fsPath]
      : ['validate', '--config', this.document.uri.fsPath];
    const cwd = vscode.workspace.getWorkspaceFolder(this.document.uri)?.uri.fsPath;
    cp.execFile(exe, args, { cwd }, (error, stdout, stderr) => {
      void this.panel.webview.postMessage({
        type: 'cliResult',
        output: (stdout + (stderr ? '\n' + stderr : '')).trim(),
        error: error && !stdout && !stderr ? error.message : undefined,
      });
    });
  }

  dispose(): void {
    ApiaryEditorPanel.current = undefined;
    this.panel.dispose();
    for (const d of this.disposables) { d.dispose(); }
    this.disposables.length = 0;
  }
}
