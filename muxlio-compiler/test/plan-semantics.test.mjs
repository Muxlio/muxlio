import test from 'node:test';
import assert from 'node:assert/strict';
import { defineAsyncWorkflow } from '../dist/async.js';

test('async numeric property access preserves the key for runtime object or array access', async () => {
  const { plan } = await defineAsyncWorkflow({
    name: 'numeric-key',
    run: async (ctx, input) => {
      return ctx.execute('read', { executor: 'test', inputs: { value: input.values['0'] } });
    },
  });
  const value = plan.subgraphs.root.nodes[0].inputs.value.expr;
  assert.equal(value.op, 'index');
  assert.equal(value.index, 0);
  assert.equal(value.expr.name, 'values');
});

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
