import * as vscode from 'vscode';
import { ApiaryPreviewPanel, isApiaryYaml } from './previewPanel';

export function activate(context: vscode.ExtensionContext): void {
  context.subscriptions.push(
    vscode.commands.registerCommand('apiary.showPreview', () => {
      const editor = vscode.window.activeTextEditor;
      if (!editor || !isApiaryYaml(editor.document)) {
        void vscode.window.showInformationMessage(
          'Open an apiary.yaml file first, then run Apiary: Show Workflow Preview.'
        );
        return;
      }
      ApiaryPreviewPanel.show(editor.document);
    })
  );

  // Auto-open preview when an apiary.yaml becomes the active editor
  context.subscriptions.push(
    vscode.window.onDidChangeActiveTextEditor(editor => {
      if (editor && isApiaryYaml(editor.document) && !ApiaryPreviewPanel.isOpen) {
        ApiaryPreviewPanel.show(editor.document);
      }
    })
  );

  // Handle the file that is already open when the extension activates
  const activeEditor = vscode.window.activeTextEditor;
  if (activeEditor && isApiaryYaml(activeEditor.document)) {
    ApiaryPreviewPanel.show(activeEditor.document);
  }
}

export function deactivate(): void {
  // nothing to clean up — the panel handles its own disposal
}
