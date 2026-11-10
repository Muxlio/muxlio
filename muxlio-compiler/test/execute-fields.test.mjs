import test from 'node:test';
import assert from 'node:assert/strict';
import { defineAsyncWorkflow } from '../dist/async.js';

test('async compilation records extended execute node fields', async () => {
  const { plan } = await defineAsyncWorkflow({
    name: 'execute-fields',
    run: async ctx => {
      await ctx.execute('run', {
        executor: 'primary',
        fallbackExecutors: ['fallback'],
        workspaceMode: 'shared-write',
        concurrencyKeys: ['package-bugfix-workspace'],
        timeoutSeconds: 600,
        queueTimeoutSeconds: 30,
        retry: { maxAttempts: 3, effect: 'nonretryable' },
      });
      return {};
    },
  });
  const node = plan.subgraphs.root.nodes[0];
  assert.deepEqual(node.fallbackExecutors, ['fallback']);
  assert.equal(node.workspaceMode, 'shared-write');
  assert.deepEqual(node.concurrencyKeys, ['package-bugfix-workspace']);
  assert.equal(node.timeoutSeconds, 600);
  assert.equal(node.queueTimeoutSeconds, 30);
  assert.deepEqual(node.retry, { maxAttempts: 3, effect: 'nonretryable' });
  assert.deepEqual(plan.requiredExecutors, ['fallback', 'primary']);
});

test('async compilation records loop onExhausted', async () => {
  const { plan } = await defineAsyncWorkflow({
    name: 'loop-exhausted',
    run: async ctx => {
      await ctx.loop('verification', true, async () => ({}), {
        maxIterations: 10,
        onExhausted: 'pause',
      });
      return {};
    },
  });
  assert.equal(plan.subgraphs.root.nodes[0].onExhausted, 'pause');
});

test('async compilation rejects traced values embedded in params', async () => {
  await assert.rejects(
  () => defineAsyncWorkflow({
    name: 'traced-params',
    run: async (ctx, input) => {
      await ctx.execute('run', { executor: 'test', params: { field: input.id } });
      return {};
    },
  }),
    /traced values belong in inputs/,
  );
});
