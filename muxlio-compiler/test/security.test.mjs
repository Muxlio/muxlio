import test from 'node:test';
import assert from 'node:assert/strict';
import { defineAsyncWorkflow } from '../dist/async.js';

test('async compilation rejects literal credentials without echoing values', async () => {
  await assert.rejects(defineAsyncWorkflow({
    name: 'security', run: async ctx => {
      await ctx.execute('work', { executor: 'test', params: { secrets: { api: 'raw-credential' } } });
      return {};
    },
  }), err => /literal credentials/.test(err.message) && !err.message.includes('raw-credential'));
});
