import test from 'node:test';
import assert from 'node:assert/strict';
import { defineAsyncWorkflow } from '../dist/async.js';

test('async compilation preserves executor contract requirements', async () => {
  const requirements = {
    executorVersion: '2', contractVersion: '1', requiredCapabilities: { progress: '1' },
    session: { mode: 'create' }, remoteTask: { id: 'task', url: 'https://example.test/task' },
  };
  const { plan } = await defineAsyncWorkflow({
    name: 'contract', run: async ctx => {
      await ctx.execute('run', { executor: 'test', ...requirements });
      return {};
    },
  });
  const node = JSON.parse(JSON.stringify(plan)).subgraphs.root.nodes[0];
  for (const [key, value] of Object.entries(requirements)) assert.deepEqual(node[key], value);
});
