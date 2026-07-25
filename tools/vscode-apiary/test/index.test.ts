import { test } from 'node:test';
import assert from 'node:assert/strict';

import { parseApiary, classifyStep, EDITABLE_STEP_TYPES } from '../src/parser';
import { serializeApiary } from '../src/serializer';
import { validateConfig } from '../src/validator';
import { computeDiff } from '../src/diff';
import { renderApiary, sid } from '../src/renderer';

// ── parser ────────────────────────────────────────────────────────────────────

test('parseApiary: parses a minimal config', () => {
  const yaml = `version: "1"\nworkflows:\n  - id: wf1\n    steps:\n      - id: s1\n        agent: eng\n        prompt: hello\n`;
  const cfg = parseApiary(yaml);
  assert.equal(cfg.version, '1');
  assert.equal(cfg.workflows?.[0].id, 'wf1');
  assert.equal(cfg.workflows?.[0].steps?.[0].id, 's1');
});

test('parseApiary: throws on non-object YAML', () => {
  assert.throws(() => parseApiary('- just a list'), /expected an object/);
});

test('classifyStep: agent step with no passthrough is fully editable', () => {
  const step = { id: 's1', agent: 'eng', prompt: 'hello' };
  const e = classifyStep(step);
  assert.equal(e.readOnly, false);
  assert.equal(e.fullyEditable, true);
  assert.deepEqual(e.passthroughFields, []);
});

test('classifyStep: step with env is partially editable', () => {
  const step = { id: 's1', agent: 'eng', prompt: 'hi', env: { FOO: 'bar' } };
  const e = classifyStep(step);
  assert.equal(e.readOnly, false);
  assert.equal(e.fullyEditable, false);
  assert.ok(e.passthroughFields.includes('env'));
});

test('classifyStep: step with depends_on is read-only', () => {
  const step = { id: 's1', depends_on: ['s0'] };
  const e = classifyStep(step);
  assert.equal(e.readOnly, true);
});

test('classifyStep: unsupported type is read-only', () => {
  const step = { id: 's1', type: 'custom_thing' };
  const e = classifyStep(step);
  assert.equal(e.readOnly, true);
});

test('EDITABLE_STEP_TYPES contains all expected types', () => {
  for (const t of ['agent', 'split', 'approval', 'foreach', 'workflow', 'wait_for', 'parallel']) {
    assert.ok(EDITABLE_STEP_TYPES.has(t), `missing: ${t}`);
  }
});

// ── serializer ────────────────────────────────────────────────────────────────

test('serializeApiary: round-trips a simple config', () => {
  const yaml = `version: "1"\nworkflows:\n  - id: wf\n    steps:\n      - id: s1\n        agent: eng\n        prompt: hello\n`;
  const cfg = parseApiary(yaml);
  const out = serializeApiary(cfg);
  const cfg2 = parseApiary(out);
  assert.equal(cfg2.workflows?.[0].id, 'wf');
  assert.equal(cfg2.workflows?.[0].steps?.[0].agent, 'eng');
});

test('serializeApiary: strips undefined fields', () => {
  const cfg = { version: '1', workflows: [{ id: 'wf', steps: [{ id: 's1', agent: undefined }] }] };
  const out = serializeApiary(cfg as never);
  assert.ok(!out.includes('agent: null'));
  assert.ok(!out.includes('undefined'));
});

test('serializeApiary: preserves passthrough fields', () => {
  const yaml = `version: "1"\nworkflows:\n  - id: wf\n    steps:\n      - id: s1\n        agent: eng\n        prompt: hi\n        env:\n          FOO: bar\n`;
  const cfg = parseApiary(yaml);
  const out = serializeApiary(cfg);
  assert.ok(out.includes('FOO: bar'), 'env field must be preserved');
});

// ── validator ────────────────────────────────────────────────────────────────

test('validateConfig: no errors for a valid minimal config', () => {
  const cfg = parseApiary(`version: "1"\nagents:\n  - id: eng\n    model: claude-sonnet-5\nworkflows:\n  - id: wf\n    steps:\n      - id: s1\n        agent: eng\n        prompt: hello\n`);
  const errors = validateConfig(cfg);
  assert.equal(errors.filter(e => e.severity === 'error').length, 0);
});

test('validateConfig: error when agent is missing', () => {
  const cfg = parseApiary(`version: "1"\nagents:\n  - id: eng\n    model: m\nworkflows:\n  - id: wf\n    steps:\n      - id: s1\n        prompt: hello\n`);
  const errors = validateConfig(cfg);
  assert.ok(errors.some(e => e.severity === 'error' && /agent/.test(e.message)));
});

test('validateConfig: error when agent id does not exist', () => {
  const cfg = parseApiary(`version: "1"\nagents:\n  - id: eng\n    model: m\nworkflows:\n  - id: wf\n    steps:\n      - id: s1\n        agent: nonexistent\n        prompt: hi\n`);
  const errors = validateConfig(cfg);
  assert.ok(errors.some(e => e.severity === 'error' && /nonexistent/.test(e.message)));
});

test('validateConfig: warning for unsupported step type', () => {
  const cfg = parseApiary(`version: "1"\nworkflows:\n  - id: wf\n    steps:\n      - id: s1\n        type: custom_thing\n`);
  const errors = validateConfig(cfg);
  assert.ok(errors.some(e => e.severity === 'warning' && /unsupported type/.test(e.message)));
});

test('validateConfig: warning for v1 DAG step', () => {
  const cfg = parseApiary(`version: "1"\nworkflows:\n  - id: wf\n    steps:\n      - id: s1\n        depends_on: [s0]\n`);
  const errors = validateConfig(cfg);
  assert.ok(errors.some(e => e.severity === 'warning' && /v1 DAG/.test(e.message)));
});

test('validateConfig: nodeId is set for step errors', () => {
  const cfg = parseApiary(`version: "1"\nworkflows:\n  - id: wf\n    steps:\n      - id: s1\n        prompt: hi\n`);
  const errors = validateConfig(cfg);
  const err = errors.find(e => e.stepId === 's1' && e.severity === 'error');
  assert.ok(err?.nodeId, 'nodeId must be set');
  assert.ok(err!.nodeId!.startsWith('N_'), 'nodeId must use sid() prefix');
});

// ── diff ────────────────────────────────────────────────────────────────────

test('computeDiff: identical texts → no changes', () => {
  const result = computeDiff('foo\nbar\n', 'foo\nbar\n');
  assert.equal(result.hasChanges, false);
  assert.equal(result.unifiedDiff, 'No changes.');
});

test('computeDiff: added line detected', () => {
  const result = computeDiff('foo\n', 'foo\nbar\n');
  assert.equal(result.hasChanges, true);
  assert.ok(result.unifiedDiff.includes('+bar'));
});

test('computeDiff: removed line detected', () => {
  const result = computeDiff('foo\nbar\n', 'foo\n');
  assert.equal(result.hasChanges, true);
  assert.ok(result.unifiedDiff.includes('-bar'));
});

test('computeDiff: changed line shows both old and new', () => {
  const result = computeDiff('foo\nold\nbaz\n', 'foo\nnew\nbaz\n');
  assert.equal(result.hasChanges, true);
  assert.ok(result.unifiedDiff.includes('-old'));
  assert.ok(result.unifiedDiff.includes('+new'));
});

test('computeDiff: context lines included', () => {
  const old = 'a\nb\nc\nd\ne\nf\ng\n';
  const nw  = 'a\nb\nc\nX\ne\nf\ng\n';
  const result = computeDiff(old, nw);
  assert.ok(result.unifiedDiff.includes(' c'));
  assert.ok(result.unifiedDiff.includes('-d'));
  assert.ok(result.unifiedDiff.includes('+X'));
  assert.ok(result.unifiedDiff.includes(' e'));
});

// ── renderer ────────────────────────────────────────────────────────────────

test('renderApiary: renders agent node', () => {
  const cfg = parseApiary(`version: "1"\nagents:\n  - id: eng\n    model: claude-sonnet-5\nworkflows:\n  - id: wf\n    steps:\n      - id: classify\n        agent: eng\n        prompt: hi\n`);
  const diagrams = renderApiary(cfg);
  assert.equal(diagrams.length, 1);
  assert.ok(diagrams[0].diagram.includes('classify'));
  assert.ok(diagrams[0].diagram.includes('agentNode'));
});

test('renderApiary: renders split node with diamond shape', () => {
  const cfg = parseApiary(`version: "1"\nworkflows:\n  - id: wf\n    steps:\n      - id: gate\n        type: split\n        branches:\n          - if: "x == 1"\n            goto: s2\n      - id: s2\n        agent: eng\n        prompt: hi\n`);
  const diagrams = renderApiary(cfg);
  assert.ok(diagrams[0].diagram.includes('splitNode'));
  // Diamond shape uses {..}
  assert.ok(diagrams[0].diagram.includes('{'));
});

test('renderApiary: renders foreach node', () => {
  const cfg = parseApiary(`version: "1"\nworkflows:\n  - id: wf\n    steps:\n      - id: loop\n        type: foreach\n        items: "steps.prev.output.items"\n`);
  const diagrams = renderApiary(cfg);
  assert.ok(diagrams[0].diagram.includes('foreachNode'));
  assert.ok(diagrams[0].diagram.includes('🔁'));
});

test('renderApiary: renders wait_for node', () => {
  const cfg = parseApiary(`version: "1"\nworkflows:\n  - id: wf\n    steps:\n      - id: wait\n        type: wait_for\n        wait_for:\n          kind: ci\n`);
  const diagrams = renderApiary(cfg);
  assert.ok(diagrams[0].diagram.includes('waitNode'));
  assert.ok(diagrams[0].diagram.includes('⏸'));
});

test('renderApiary: renders workflow (subworkflow) node', () => {
  const cfg = parseApiary(`version: "1"\nworkflows:\n  - id: wf\n    steps:\n      - id: sub\n        type: workflow\n        workflow: other-wf\n`);
  const diagrams = renderApiary(cfg);
  assert.ok(diagrams[0].diagram.includes('workflowNode'));
  assert.ok(diagrams[0].diagram.includes('📂'));
});

test('renderApiary: renders parallel node', () => {
  const cfg = parseApiary(`version: "1"\nworkflows:\n  - id: wf\n    steps:\n      - id: par\n        type: parallel\n        join: any\n`);
  const diagrams = renderApiary(cfg);
  assert.ok(diagrams[0].diagram.includes('parallelNode'));
  assert.ok(diagrams[0].diagram.includes('⫶'));
});

test('renderApiary: interactive mode adds click directives', () => {
  const cfg = parseApiary(`version: "1"\nworkflows:\n  - id: wf\n    steps:\n      - id: s1\n        agent: eng\n        prompt: hi\n`);
  const diagrams = renderApiary(cfg, { interactive: true });
  assert.ok(diagrams[0].diagram.includes('click'));
  assert.ok(diagrams[0].diagram.includes('handleNodeClick'));
});

test('renderApiary: retry back-edge rendered', () => {
  const cfg = parseApiary(`version: "1"\nworkflows:\n  - id: wf\n    steps:\n      - id: s1\n        agent: eng\n        prompt: hi\n      - id: s2\n        agent: eng\n        prompt: hi\n        reject_when: "result == bad"\n        on_reject:\n          restart_from: s1\n          max: 3\n`);
  const diagrams = renderApiary(cfg);
  assert.ok(diagrams[0].diagram.includes('retry'));
  assert.ok(diagrams[0].diagram.includes('max 3x'));
});

test('sid: produces N_ prefixed alphanumeric ids', () => {
  assert.equal(sid('hello world'), 'N_hello_world');
  assert.equal(sid('my-step.id'), 'N_my_step_id');
  assert.equal(sid('wf_step1'), 'N_wf_step1');
});
