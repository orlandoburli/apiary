import * as vscode from 'vscode';
import { ApiaryPreviewPanel, isApiaryYaml } from './previewPanel';
import { ApiaryEditorPanel } from './editorPanel';

export function activate(context: vscode.ExtensionContext): void {
  // ── Read-only preview ──────────────────────────────────────────────────────
  context.subscriptions.push(
    vscode.commands.registerCommand('apiary.showPreview', () => {
      const editor = vscode.window.activeTextEditor;
      if (!editor || !isApiaryYaml(editor.document)) {
        void vscode.window.showInformationMessage(
          'Open an apiary.yaml file first, then run Apiary: Show Workflow Preview.',
        );
        return;
      }
      ApiaryPreviewPanel.show(editor.document);
    }),
  );

  // ── Bidirectional editor ───────────────────────────────────────────────────
  context.subscriptions.push(
    vscode.commands.registerCommand('apiary.openEditor', () => {
      const editor = vscode.window.activeTextEditor;
      if (!editor || !isApiaryYaml(editor.document)) {
        void vscode.window.showInformationMessage(
          'Open an apiary.yaml file first, then run Apiary: Open Workflow Editor.',
        );
        return;
      }
      ApiaryEditorPanel.show(editor.document);
    }),
  );

  // Auto-open preview when an apiary.yaml becomes active (no editor open yet)
  context.subscriptions.push(
    vscode.window.onDidChangeActiveTextEditor(editor => {
      if (editor && isApiaryYaml(editor.document) && !ApiaryPreviewPanel.isOpen) {
        ApiaryPreviewPanel.show(editor.document);
      }
    }),
  );

  // Handle the file already open at activation time
  const activeEditor = vscode.window.activeTextEditor;
  if (activeEditor && isApiaryYaml(activeEditor.document)) {
    ApiaryPreviewPanel.show(activeEditor.document);
  }
}

export function deactivate(): void {
  // panel objects handle their own disposal via onDidDispose
}
