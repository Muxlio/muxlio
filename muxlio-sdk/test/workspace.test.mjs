import test from 'node:test';
import assert from 'node:assert/strict';
import { defineWorkflow, workflow, task } from '../dist/index.js';

test('both authoring APIs preserve the workspace identity independently of mode', () => {
  const opts = { executor: 'test', workspaceMode: 'snapshot' };
  const symbolic = workflow('project', { workspaceId: 'authorized-project', build: ({ step }) => { step('work', task(opts), {}); } });
  const builder = defineWorkflow({ name: 'project', workspaceId: 'authorized-project', build: (g) => { g.execute('work', opts); } });
  for (const { plan } of [symbolic, builder]) {
    assert.equal(plan.workspaceId, 'authorized-project');
    assert.equal(plan.subgraphs.root.nodes[0].workspaceMode, 'snapshot');
  }
});

test('both authoring APIs preserve workspace ownership and exclusive keys', () => {
  const opts = { executor: 'test', workspaceMode: 'shared-write', concurrencyKeys: ['repository', 'external-resource'] };
  const symbolic = workflow('resources', { build: ({ step }) => { step('work', task(opts), {}); } });
  const builder = defineWorkflow({ name: 'resources', build: (g) => { g.execute('work', opts); } });
  for (const { plan } of [symbolic, builder]) {
    const node = plan.subgraphs.root.nodes[0];
    assert.equal(node.workspaceMode, 'shared-write');
    assert.deepEqual(node.concurrencyKeys, opts.concurrencyKeys);
  }
});

test('both authoring APIs preserve an explicitly empty workspace', () => {
  const opts = { executor: 'test', workspaceMode: 'empty' };
  const symbolic = workflow('empty-workspace', { build: ({ step }) => { step('work', task(opts), {}); } });
  const builder = defineWorkflow({ name: 'empty-workspace', build: (g) => { g.execute('work', opts); } });
  for (const { plan } of [symbolic, builder]) {
    assert.equal(plan.subgraphs.root.nodes[0].workspaceMode, 'empty');
  }
});

test('both authoring APIs preserve the immutable prepared workspace content pin', () => {
  const workspaceDigest = `sha256:${'a'.repeat(64)}`;
  const options = { workspaceId: 'prepared', workspaceDigest };
  const symbolic = workflow('prepared', { ...options, build: ({ step }) => { step('work', task({ executor: 'generic' }), {}); } });
  const builder = defineWorkflow({ name: 'prepared', ...options, build: (g) => { g.execute('work', { executor: 'generic' }); } });
  for (const { plan } of [symbolic, builder]) {
    assert.equal(plan.workspaceDigest, workspaceDigest);
    assert.equal(plan.workspaceId, 'prepared');
  }
});
