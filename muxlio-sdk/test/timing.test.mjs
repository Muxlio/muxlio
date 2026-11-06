import test from 'node:test';
import assert from 'node:assert/strict';
import { defineWorkflow, workflow, task } from '../dist/index.js';

test('both authoring APIs preserve independent timing controls', () => {
  const symbolic = workflow('timed', {
    deadlineSeconds: 120,
    build: ({ step }) => {
      step('work', task({ executor: 'test', timeoutSeconds: 30, queueTimeoutSeconds: 10 }), {});
    },
  });
  const builder = defineWorkflow({
    name: 'timed', deadlineSeconds: 120,
    build: (g) => { g.execute('work', { executor: 'test', timeoutSeconds: 30, queueTimeoutSeconds: 10 }); },
  });
  for (const { plan } of [symbolic, builder]) {
    assert.equal(plan.deadlineSeconds, 120);
    assert.equal(plan.subgraphs.root.nodes[0].timeoutSeconds, 30);
    assert.equal(plan.subgraphs.root.nodes[0].queueTimeoutSeconds, 10);
  }
});

test('both authoring APIs pin run-wide admission and accounting limits', () => {
  const limits = { maxAttempts: 20, maxActivations: 100, maxActiveAttempts: 3,
    maxTokens: 50000, maxCostUsd: 2, missingUsage: 'fail-closed' };
  const symbolic = workflow('bounded', { limits, build: () => {} });
  const builder = defineWorkflow({ name: 'bounded', limits, build: () => {} });
  for (const { plan } of [symbolic, builder]) {
    assert.deepEqual(JSON.parse(JSON.stringify(plan)).limits, limits);
  }
  const changed = defineWorkflow({ name: 'bounded', limits: { ...limits, maxAttempts: 21 }, build: () => {} });
  assert.notEqual(builder.plan.digest, changed.plan.digest);
});
