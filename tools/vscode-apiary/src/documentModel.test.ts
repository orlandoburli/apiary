import assert from 'node:assert/strict';
import test from 'node:test';
import { analyzeApiary, applyWorkflowEdit, semanticDiff } from './documentModel';

const supported = `# project comment
version: "1"
runners:
  - id: codex
    type: codex-cli
sources:
  - id: github
    type: github
    config: { repo: owner/repo }
agents:
  - id: engineer
    model: gpt-5
    runner: codex
workflows:
  - id: build # workflow comment
    trigger:
      match: { source: github }
    steps:
      - id: implement # keep this
        agent: engineer
      - id: review
        type: approval
        message: Review it
        resume_on: { label_added: approved }
`;

test('round-trip edit preserves comments and changes only the addressed field', () => {
  const result = applyWorkflowEdit(supported, { kind: 'set', workflowId: 'build', stepId: 'implement', path: ['prompt'], value: 'Implement safely' });
  assert.match(result.analysis.text, /# project comment/);
  assert.match(result.analysis.text, /# workflow comment/);
  assert.match(result.analysis.text, /# keep this/);
  assert.equal(result.analysis.config.workflows?.[0].steps?.[0].prompt, 'Implement safely');
  assert.deepEqual(result.changes.map(change => change.path), ['$.workflows[0].steps[0].prompt']);
});

test('unsupported constructs make a workflow explicitly read-only without changing text', () => {
  const text = supported.replace('    steps:', '    custom_graph: true\n    steps:');
  const analysis = analyzeApiary(text);
  assert.equal(analysis.support[0].editable, false);
  assert.match(analysis.support[0].reasons.join(' '), /unsupported key custom_graph/);
  assert.throws(() => applyWorkflowEdit(text, { kind: 'addStep', workflowId: 'build', stepType: 'agent', id: 'next' }), /read-only/);
  assert.equal(analysis.text, text);
});

test('reference diagnostics carry node identity and YAML location', () => {
  const text = supported.replace('agent: engineer', 'agent: missing');
  const diagnostic = analyzeApiary(text).diagnostics.find(item => item.message.includes('Unknown agent'));
  assert.ok(diagnostic);
  assert.equal(diagnostic.workflowId, 'build');
  assert.equal(diagnostic.stepId, 'implement');
  assert.ok(diagnostic.line > 0);
  assert.ok(diagnostic.column > 0);
});

test('add, move, and delete steps retain semantic ordering', () => {
  let candidate = applyWorkflowEdit(supported, { kind: 'addStep', workflowId: 'build', stepType: 'wait_for', id: 'ci' }).analysis.text;
  candidate = applyWorkflowEdit(candidate, { kind: 'moveStep', workflowId: 'build', stepId: 'ci', offset: -1 }).analysis.text;
  assert.deepEqual(analyzeApiary(candidate).config.workflows?.[0].steps?.map(step => step.id), ['implement', 'ci', 'review']);
  candidate = applyWorkflowEdit(candidate, { kind: 'deleteStep', workflowId: 'build', stepId: 'ci' }).analysis.text;
  assert.deepEqual(analyzeApiary(candidate).config.workflows?.[0].steps?.map(step => step.id), ['implement', 'review']);
});

test('semantic diff classifies additions, removals, and changes', () => {
  assert.deepEqual(semanticDiff({ a: 1, b: 2 }, { a: 3, c: 4 }).map(item => [item.kind, item.path]), [
    ['change', '$.a'], ['remove', '$.b'], ['add', '$.c'],
  ]);
});

test('malformed expressions are attached to their workflow step and line', () => {
  const text = supported.replace('agent: engineer', "agent: engineer\n        if: '${{ memory.ready == true'");
  const diagnostic = analyzeApiary(text).diagnostics.find(item => item.message.includes('Malformed expression'));
  assert.ok(diagnostic);
  assert.equal(diagnostic.workflowId, 'build');
  assert.equal(diagnostic.stepId, 'implement');
  assert.ok(diagnostic.line > 0);
});

test('split branches can be connected and removed semantically', () => {
  let text = applyWorkflowEdit(supported, { kind: 'addStep', workflowId: 'build', stepType: 'split', id: 'route' }).analysis.text;
  text = applyWorkflowEdit(text, { kind: 'addBranch', workflowId: 'build', stepId: 'route' }).analysis.text;
  text = applyWorkflowEdit(text, { kind: 'set', workflowId: 'build', stepId: 'route', path: ['branches', 1, 'goto'], value: 'review' }).analysis.text;
  assert.equal(analyzeApiary(text).config.workflows?.[0].steps?.[2].branches?.length, 2);
  text = applyWorkflowEdit(text, { kind: 'deleteBranch', workflowId: 'build', stepId: 'route', branchIndex: 0 }).analysis.text;
  assert.equal(analyzeApiary(text).config.workflows?.[0].steps?.[2].branches?.[0].goto, 'review');
});
