import test from 'node:test';
import assert from 'node:assert/strict';
import compiled from './workflow.ts';

const plan = compiled.plan;

test('implement-features plan structure', () => {
  assert.equal(plan.name, 'implement-features');
  assert.ok(plan.requiredExecutors.includes('agent/codex'), 'must require agent/codex');
  assert.ok(!plan.requiredExecutors.includes('command/go-test'));

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
  assert.equal(implement?.executor, 'agent/codex');

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
