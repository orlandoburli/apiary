import * as vscode from 'vscode';
import { ApiaryPreviewPanel, isApiaryYaml } from './previewPanel';
import { ApiaryEditorPanel } from './editorPanel';

export function activate(context: vscode.ExtensionContext): void {
  // Read-only diagram preview (existing)
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

  // Bidirectional visual editor (new)
  context.subscriptions.push(
    vscode.commands.registerCommand('apiary.openEditor', () => {
      const editor = vscode.window.activeTextEditor;
      if (!editor || !isApiaryYaml(editor.document)) {
        void vscode.window.showInformationMessage(
          'Open an apiary.yaml file first, then run Apiary: Open Visual Editor.'
        );
        return;
      }
      ApiaryEditorPanel.show(editor.document);
    })
  );

  // Auto-open preview (read-only) when an apiary.yaml becomes the active editor
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
  // nothing to clean up — panels handle their own disposal
}
