import test from 'node:test';
import assert from 'node:assert/strict';
import { defineWorkflow, workflow, task } from '../dist/index.js';

test('authoring APIs preserve versioned executor requirements through JSON', () => {
  const requirements = {
    executorVersion: '2', contractVersion: '1',
    requiredCapabilities: { progress: '1', typedResult: '1', session: '2', remoteTask: '1' },
    retry: { effect: 'idempotent-with-key', idempotencyKey: 'invoice-123', maxAttempts: 3 },
    session: { mode: 'resume', handle: { id: 'opaque', executor: 'test', runnerVersion: 'runner-1', ownerWorker: 'worker', ownerEnvironment: 'env', expiresAt: '2099-01-01T00:00:00Z', recoverable: true } }, remoteTask: { id: 'task', url: 'https://example.test/task' },
  };
  const opts = { executor: 'test', ...requirements };
  const symbolic = workflow('contract', { build: ({ step }) => { step('work', task(opts), {}); } });
  const builder = defineWorkflow({ name: 'contract', build: (g) => { g.execute('work', opts); } });
  for (const { plan } of [symbolic, builder]) {
    const node = JSON.parse(JSON.stringify(plan)).subgraphs.root.nodes[0];
    for (const [key, value] of Object.entries(requirements)) assert.deepEqual(node[key], value);
  }
});
