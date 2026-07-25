import * as vscode from 'vscode';
import { ApiaryEditorPanel, isApiaryYaml } from './editorPanel';

export function activate(context: vscode.ExtensionContext): void {
  context.subscriptions.push(
    vscode.commands.registerCommand('apiary.showPreview', () => {
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

  // Auto-open when an apiary.yaml becomes the active editor and no panel is open
  context.subscriptions.push(
    vscode.window.onDidChangeActiveTextEditor(editor => {
      if (editor && isApiaryYaml(editor.document) && !ApiaryEditorPanel.isOpen) {
        ApiaryEditorPanel.show(editor.document);
      }
    }),
  );

  // Handle the file already open at activation time
  const active = vscode.window.activeTextEditor;
  if (active && isApiaryYaml(active.document)) {
    ApiaryEditorPanel.show(active.document);
  }
}

export function deactivate(): void {
  // panels self-dispose; nothing to clean up here
}
