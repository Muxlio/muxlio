import test from 'node:test';
import assert from 'node:assert/strict';
import compiled from './workflow.ts';

const plan = compiled.plan;
const allNodes = Object.values(plan.subgraphs).flatMap(graph => graph.nodes);
const node = id => allNodes.find(node => node.id === id);

test('package sweep pins bounded serial execution and a concrete input contract', () => {
  assert.equal(plan.name, 'fix-package-bugs');
  assert.equal(plan.inputSchema.type, 'object');
  const packages = plan.inputSchema.properties.packages;
  assert.equal(packages.maxItems, 200);
  assert.deepEqual(packages.items.required, ['id', 'path', 'kind', 'target']);
  assert.deepEqual(packages.items.properties.kind.enum, ['go', 'npm', 'ui']);
  assert.deepEqual(plan.requiredExecutors, ['agent/codex', 'agent/kiro', 'command']);
  assert.equal(plan.limits.maxActiveAttempts, 1);
  const map = plan.subgraphs.root.nodes[0];
  assert.equal(map.kind, 'map');
  assert.equal(map.concurrency, 1);
  assert.equal(map.maxItems, 200);
  assert.deepEqual(map.key, { op: 'field', expr: { op: 'item' }, name: 'id' });
  for (const execute of allNodes.filter(node => node.kind === 'execute')) {
    assert.equal(execute.workspaceMode, 'shared-write', execute.id);
    assert.deepEqual(execute.concurrencyKeys, ['package-bugfix-workspace'], execute.id);
    assert.ok(execute.timeoutSeconds > 0);
  }
});

test('audit waits for a clean workspace and repair consumes evidence', () => {
  assert.equal(node('require-clean-workspace').params.failOnOutput, true);
  assert.deepEqual(node('audit').inputs.__after_0.ref, { node: 'require-clean-workspace', port: 'passed' });
  assert.deepEqual(node('repair').inputs.findings.ref, { node: 'audit', port: 'findings' });
  assert.equal(node('audit').fallbackExecutors[0], 'agent/codex');
  assert.equal(node('audit').retry.maxAttempts, 3);
});

test('every verification attempt reviews the diff and passes review verdict to the command gate', () => {
  const loop = node('verification');
  assert.equal(loop.maxIterations, 5);
  assert.equal(loop.onExhausted, 'pause');
  const body = plan.subgraphs[loop.bodyRef];
  assert.ok(body.nodes.some(node => node.id === 'review'));
  assert.ok(body.nodes.some(node => node.id === 'verify'));
  assert.deepEqual(node('verify').inputs.approved.ref, { node: 'review', port: 'approved' });
  assert.deepEqual(node('verify').inputs.feedback.ref, { node: 'review', port: 'feedback' });
  assert.equal(node('verify').params.onNonZero, 'return');
  assert.equal(node('verify').outputs.report.path, 'checks.json');
  assert.equal(node('verify').outputs.passed.schema.type, 'boolean');
  assert.deepEqual(node('fix-verification').inputs.failures.expr.fields.stdout,
    { op: 'ref', node: 'verification__cap', port: 'value_stdout' });
  assert.deepEqual(node('fix-verification').inputs.checks.ref,
    { node: 'verification__cap', port: 'value_report' });
});

test('commits are guarded, nonretryable and local', () => {
  const guard = allNodes.find(node => node.kind === 'branch' && node.id.startsWith('commit_if_passed'));
  assert.ok(guard);
  assert.ok(plan.subgraphs[guard.thenRef].nodes.some(node => node.id === 'commit'));
  assert.equal(plan.subgraphs[guard.elseRef].nodes.length, 0);
  assert.equal(node('commit').retry.effect, 'nonretryable');
  assert.equal(node('commit').retry.maxAttempts, 1);
  assert.equal(node('commit').params.onNonZero, 'fail');
  assert.equal(allNodes.some(node => node.id === 'push'), false);
  const map = plan.subgraphs.root.nodes[0];
  const output = plan.subgraphs[map.bodyRef].output;
  assert.ok(output.verification, 'successful check artifact must appear in the package result');
  assert.ok(output.passed);
});
