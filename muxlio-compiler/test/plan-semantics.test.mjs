import test from 'node:test';
import assert from 'node:assert/strict';
import { defineAsyncWorkflow } from '../dist/async.js';

test('async compilation sorts requiredExecutors for canonical digests', async () => {
  const { plan } = await defineAsyncWorkflow({
    name: 'exec-order',
    run: async ctx => {
      await ctx.execute('first', { executor: 'zebra' });
      await ctx.execute('second', { executor: 'alpha' });
      await ctx.execute('third', { executor: 'mango' });
      return {};
    },
  });
  assert.deepEqual(plan.requiredExecutors, ['alpha', 'mango', 'zebra']);
});
