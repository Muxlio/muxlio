import test from 'node:test';
import assert from 'node:assert/strict';
import compiled from './workflow.ts';

const plan = compiled.plan;

test('implement-features plan structure', () => {
  assert.equal(plan.name, 'implement-features');
  assert.ok(plan.requiredExecutors.includes('agent/codex'), 'must require agent/codex');
  assert.ok(plan.requiredExecutors.includes('command'));

  // No prepare step — the recipe is workspace-agnostic.
  const root = plan.subgraphs.root;
  const prepare = root.nodes.find((n) => n.id === 'prepare');
  assert.equal(prepare, undefined, 'should have no prepare node');

  // Root contains a map over features.
  const mapNode = root.nodes.find((n) => n.kind === 'map');
  assert.ok(mapNode, 'root must have a map node');

  assert.equal(root.nodes.at(-1), mapNode, 'the recipe finishes after the feature map');

  const body = plan.subgraphs[mapNode.bodyRef];

  // implement is the first step in the body.
  const implement = body.nodes.find((n) => n.id === 'implement');
  assert.equal(implement?.executor, 'agent/kiro');
  assert.deepEqual(implement?.fallbackExecutors, ['agent/cursor']);
  const review = body.nodes.find((n) => n.id === 'review');
  assert.equal(review?.executor, 'agent/antigravity');
  assert.deepEqual(review?.fallbackExecutors, ['agent/codex']);
  const fixNode = Object.values(plan.subgraphs).flatMap((g) => g.nodes).find((n) => n.id === 'fix');
  assert.equal(fixNode?.executor, 'agent/kiro');
  assert.deepEqual(fixNode?.fallbackExecutors, ['agent/cursor']);

  const allNodes = Object.values(plan.subgraphs).flatMap((g) => g.nodes);
  assert.equal(allNodes.some((n) => n.id === 'push' ||
    (n.params?.program === 'git' && n.params?.args?.includes('push'))), false,
    'the recipe commits locally and never pushes');
  for (const command of allNodes.filter((n) => n.executor === 'command')) {
    assert.equal(command.outputs?._result, undefined, 'command outputs must be flat');
    assert.ok(command.outputs?.passed, 'command completion exposes passed');
  }
  const fix = allNodes.find((n) => n.id === 'fix');
  assert.deepEqual(fix?.inputs?.failures?.expr?.fields?.stdout,
    { op: 'ref', node: 'verification__cap', port: 'value_stdout' },
    'fix receives captured command details directly');
  const stage = allNodes.find((n) => n.id === 'stage-changes');
  const commit = allNodes.find((n) => n.id === 'commit');
  const cleanCheck = allNodes.find((n) => n.id === 'require-clean-workspace');
  assert.equal(stage?.executor, 'command');
  assert.equal(commit?.executor, 'command');
  assert.equal(cleanCheck?.params?.failOnOutput, true);
  assert.deepEqual(commit?.params?.argTemplates, [null, null, 'implement {{feature.feature}}']);
  for (const node of allNodes.filter((n) => n.id === 'implement' || n.id === 'review' || n.id === 'fix' || n.executor === 'command')) {
    assert.equal(node.workspaceMode, 'shared-write');
    assert.deepEqual(node.concurrencyKeys, ['implement-features-workspace']);
  }

  const verificationLoop = body.nodes.find((n) => n.kind === 'loop');
  const attemptNodes = plan.subgraphs[verificationLoop.bodyRef].nodes;
  const verify = attemptNodes.find((n) => n.id === 'verify');
  assert.equal(verify?.executor, 'command');
  assert.equal(verify?.params?.args?.[0], '-e');
  assert.match(verify?.params?.args?.[1], /muxlio-core/);
  assert.match(verify?.params?.args?.[1], /muxlio-adapters/);
  assert.equal(verify?.params?.onNonZero, 'return');
  assert.deepEqual(verify?.outputs?.passed, { mime: 'application/json', schema: { type: 'boolean' } });
  const capture = attemptNodes.find((n) => n.id === 'verification__cap');
  assert.deepEqual(capture?.inputs?.passed, { expr: { op: 'ref', node: 'verify', port: 'passed' } });

  // Verification loop runs up to 10 times and pauses on exhaustion.
  const loop = body.nodes.find((n) => n.kind === 'loop');
  assert.ok(loop, 'body must have a verification loop');
  assert.equal(loop.maxIterations, 10);
  assert.equal(loop.onExhausted, 'pause');
});
