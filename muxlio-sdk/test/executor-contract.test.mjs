import test from 'node:test';
import assert from 'node:assert/strict';
import { defineWorkflow, workflow, task } from '../dist/index.js';

test('authoring APIs preserve versioned executor requirements through JSON', () => {
  const requirements = {
    environment: { version: '1', digest: 'sha256:' + 'a'.repeat(64), protocolDigest: 'sha256:' + 'b'.repeat(64), runners: { runner: '1.2.3' }, packages: { tool: 'sha256:' + 'c'.repeat(64) }, containers: { sandbox: 'sha256:' + 'd'.repeat(64) }, skills: { review: 'sha256:' + 'e'.repeat(64) }, allowedEnvironment: ['PATH', 'HOME'], permissions: ['workspace-read'] },
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

test('authoring rejects secret values and extra fields in environment exports', () => {
  const base = { version: '1', digest: 'sha256:' + 'a'.repeat(64), protocolDigest: 'sha256:' + 'b'.repeat(64) };
  for (const environment of [
    { ...base, secrets: { api: 'private-value' } },
    { ...base, allowedEnvironment: { API_KEY: 'private-value' } },
    { ...base, allowedEnvironment: ['API_KEY=private-value'] },
    { ...base, runners: { runner: 'private-value' } },
    { ...base, packages: { package: 'private-value' } },
  ]) {
    for (const build of [
      () => defineWorkflow({ name: 'secret', build: g => g.execute('work', { executor: 'test', environment }) }),
      () => workflow('secret', { build: ({ step }) => step('work', task({ executor: 'test', environment }), {}) }).plan,
    ]) assert.throws(build, error => error.message.includes('public version pins') && !error.message.includes('private-value'));
  }
});

test('authoring preserves complete prerelease and build pins', () => {
  const environment = { version: '1', digest: 'sha256:' + 'a'.repeat(64), protocolDigest: 'sha256:' + 'b'.repeat(64), runners: { runner: '1.2.3-beta+build.1' } };
  const { plan } = defineWorkflow({ name: 'pinned', build: g => g.execute('work', { executor: 'test', environment }) });
  assert.deepEqual(plan.subgraphs.root.nodes[0].environment, environment);
});
