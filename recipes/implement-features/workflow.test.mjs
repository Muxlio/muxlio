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

  const body = plan.subgraphs[mapNode.bodyRef];

  // implement is the first step in the body.
  const implement = body.nodes.find((n) => n.id === 'implement');
  assert.equal(implement?.executor, 'agent/kiro');

  const allNodes = Object.values(plan.subgraphs).flatMap((g) => g.nodes);
  const stage = allNodes.find((n) => n.id === 'stage-changes');
  const commit = allNodes.find((n) => n.id === 'commit');
  assert.equal(stage?.executor, 'command');
  assert.equal(commit?.executor, 'command');
  assert.deepEqual(commit?.params?.argTemplates, [null, null, 'implement {{feature.feature}}']);
  const commitSHA = allNodes.find((n) => n.id === 'commit-sha');
  assert.equal(commitSHA?.executor, 'command');
  assert.deepEqual(commitSHA?.params?.args, ['rev-parse', 'HEAD']);
  assert.equal(commitSHA?.params?.trimStdout, true);
  const cleanCheck = allNodes.find((n) => n.id === 'require-clean-workspace');
  assert.equal(cleanCheck?.params?.failOnOutput, true);

  const verificationLoop = body.nodes.find((n) => n.kind === 'loop');
  const attemptNodes = plan.subgraphs[verificationLoop.bodyRef].nodes;
  const verify = attemptNodes.find((n) => n.id === 'verify');
  assert.equal(verify?.executor, 'command');
  assert.equal(verify?.params?.args?.[0], '-e');
  assert.match(verify?.params?.args?.[1], /muxlio-core/);
  assert.match(verify?.params?.args?.[1], /muxlio-adapters/);
  assert.equal(verify?.params?.onNonZero, 'return');
  const capture = attemptNodes.find((n) => n.id === 'verification__cap');
  assert.equal(capture?.inputs?.passed?.expr?.op, 'field');
  assert.equal(capture?.inputs?.passed?.expr?.name, 'passed');

  // Commit is guarded by a branch (requiresPassed).
  const commitGuard = body.nodes.find(
    (n) => n.kind === 'branch' && n.id.startsWith('commit_if_passed'),
  );
  assert.ok(commitGuard, 'commit must be guarded by a requiresPassed branch');

  // Verification loop runs up to 10 times and pauses on exhaustion.
  const loop = body.nodes.find((n) => n.kind === 'loop');
  assert.ok(loop, 'body must have a verification loop');
  assert.equal(loop.maxIterations, 10);
  assert.equal(loop.onExhausted, 'pause');
});
