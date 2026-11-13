import test from 'node:test';
import assert from 'node:assert/strict';
import { defineAsyncWorkflow } from '../dist/index.js';

test('async compilation binds the prepared workspace content revision', async () => {
  const workspaceDigest = `sha256:${'a'.repeat(64)}`;
  const { plan } = await defineAsyncWorkflow({
    name: 'prepared', workspaceId: 'prepared', workspaceDigest,
    run(ctx) { return ctx.execute('work', { executor: 'generic' }); },
  });
  assert.equal(plan.workspaceId, 'prepared');
  assert.equal(plan.workspaceDigest, workspaceDigest);
  const { plan: changed } = await defineAsyncWorkflow({
    name: 'prepared', workspaceId: 'prepared', workspaceDigest: `sha256:${'b'.repeat(64)}`,
    run(ctx) { return ctx.execute('work', { executor: 'generic' }); },
  });
  assert.notEqual(plan.digest, changed.digest);
});
