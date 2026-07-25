import assert from 'node:assert/strict';
import test from 'node:test';
import { renderApiary } from './renderer';

test('renderer includes supported editable node types and click identities', () => {
  const [result] = renderApiary({ workflows: [{ id: 'delivery', steps: [
    { id: 'build', agent: 'engineer' },
    { id: 'ci', type: 'wait_for', wait_for: { kind: 'ci' } },
    { id: 'approve', type: 'approval' },
    { id: 'deploy', type: 'workflow', workflow: 'release' },
  ] }] });
  assert.equal(result.workflowId, 'delivery');
  assert.match(result.diagram, /wait: ci/);
  assert.match(result.diagram, /subworkflow|release/);
  assert.match(result.diagram, /click N_delivery_build selectNode/);
});
