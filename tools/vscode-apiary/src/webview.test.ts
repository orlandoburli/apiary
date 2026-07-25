import assert from 'node:assert/strict';
import test from 'node:test';
import { buildEditorHtml } from './webview';

test('editor webview exposes authoring, review, and CLI controls', () => {
  const html = buildEditorHtml('test-nonce');
  for (const expected of ['Apiary Editor', 'Review & Save', 'Validate', 'Dry Run', 'Semantic diff', 'Generated YAML', 'data-add', 'data-move']) {
    assert.match(html, new RegExp(expected));
  }
  assert.match(html, /script-src https:\/\/cdn\.jsdelivr\.net 'nonce-test-nonce'/);
  assert.match(html, /Unsupported YAML detected/);
});
