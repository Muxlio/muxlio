import test from 'node:test';
import assert from 'node:assert/strict';
import { defineWorkflow, workflow, task } from '../dist/index.js';

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
