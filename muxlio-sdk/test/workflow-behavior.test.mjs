import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const dir = dirname(fileURLToPath(import.meta.url));
const plan = JSON.parse(readFileSync(join(dir, 'fixtures/repeat-until-10.plan.json'), 'utf8'));

test('repeatUntil fixture wires fix failures to capture report ref', () => {
  const loop = plan.subgraphs.root.nodes[0];
  const body = plan.subgraphs[loop.bodyRef];
  const branch = body.nodes.find((n) => n.kind === 'branch');
  assert.equal(branch.condition.op, 'and');
  const fixSG = plan.subgraphs[branch.thenRef];
  const fix = fixSG.nodes.find((n) => n.id === 'fix');
  assert.deepEqual(fix.inputs.failures.ref, { node: 'loop__cap', port: 'value_report' });
});
