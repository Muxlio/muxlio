import test from 'node:test';
import assert from 'node:assert/strict';
import { defineAsyncWorkflow } from '../dist/async.js';

test('async compilation preserves executor contract requirements', async () => {
  const requirements = {
    environment: { version: '1', digest: 'sha256:' + 'a'.repeat(64), protocolDigest: 'sha256:' + 'b'.repeat(64), runners: { runner: '1.2.3' }, packages: { tool: 'sha256:' + 'c'.repeat(64) }, containers: { sandbox: 'sha256:' + 'd'.repeat(64) }, skills: { review: 'sha256:' + 'e'.repeat(64) }, allowedEnvironment: ['PATH', 'HOME'], permissions: ['workspace-read'] },
    executorVersion: '2', contractVersion: '1', requiredCapabilities: { progress: '1' },
    session: { mode: 'create' }, remoteTask: { id: 'task', url: 'https://example.test/task', contextId: 'remote-context' },
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

test('async compilation rejects environment credential values before exporting', async () => {
  await assert.rejects(() => defineAsyncWorkflow({ name: 'secret', run: async ctx => {
    await ctx.execute('run', { executor: 'test', environment: { version: '1', digest: 'sha256:' + 'a'.repeat(64), protocolDigest: 'sha256:' + 'b'.repeat(64), values: { API_KEY: 'private-value' } } });
    return {};
  } }), error => error.message.includes('public version pins') && !error.message.includes('private-value'));
});
