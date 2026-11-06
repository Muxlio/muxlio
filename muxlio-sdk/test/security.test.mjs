import test from 'node:test';
import assert from 'node:assert/strict';
import { defineWorkflow, workflow, task } from '../dist/index.js';

test('authoring rejects credential values before emitting a plan', () => {
  for (const params of [{ secrets: { api: 'raw-credential' } }, { env: { API_KEY: 'raw-credential' } }]) {
    const opts = { executor: 'test', params };
    for (const build of [
      () => defineWorkflow({ name: 'security', build: g => { g.execute('work', opts); } }),
      () => workflow('security', { build: ({ step }) => { step('work', task(opts), {}); } }).plan,
    ]) {
      assert.throws(build, err => /literal credentials/.test(err.message) && !err.message.includes('raw-credential'));
    }
  }
});

test('security declarations and opaque handles survive authoring', () => {
  const params = {
    secretRefs: { api: 'service-production' },
    executionPolicy: { allowedSecrets: ['api'], trust: 'restricted', network: 'none', filesystem: 'scratch' },
    isolation: { mode: 'required', runtime: 'bwrap' },
  };
  const { plan } = defineWorkflow({ name: 'security', build: g => { g.execute('work', { executor: 'test', params }); } });
  assert.deepEqual(plan.subgraphs.root.nodes[0].params, params);
});
