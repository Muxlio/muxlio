import test from 'node:test';
import assert from 'node:assert/strict';
import { defineWorkflow, workflow, task } from '../dist/index.js';

test('defineWorkflow sorts requiredExecutors for parity with workflow()', () => {
  const plan = defineWorkflow({
    name: 'exec-order',
    build: (g) => {
      g.execute('first', { executor: 'zebra' });
      g.execute('second', { executor: 'alpha' });
      g.execute('third', { executor: 'mango' });
    },
  }).plan;
  assert.deepEqual(plan.requiredExecutors, ['alpha', 'mango', 'zebra']);
});

test('equivalent symbolic and builder workflows share digest when executor lists match', () => {
  const symbolic = workflow('digest-parity', {
    build: ({ step }) => {
      step('first', task({ executor: 'zebra' }), {});
      step('second', task({ executor: 'alpha' }), {});
    },
  }).plan;
  const builder = defineWorkflow({
    name: 'digest-parity',
    build: (g) => {
      g.execute('first', { executor: 'zebra' });
      g.execute('second', { executor: 'alpha' });
    },
  }).plan;
  assert.deepEqual(symbolic.requiredExecutors, builder.requiredExecutors);
  assert.equal(symbolic.digest, builder.digest);
});

test('defineWorkflow matches workflow requiredExecutors ordering', () => {
  const opts = { executor: 'beta' };
  const symbolic = workflow('parity', {
    build: ({ step }) => {
      step('second', task({ executor: 'zebra' }), {});
      step('first', task(opts), {});
    },
  }).plan.requiredExecutors;
  const builder = defineWorkflow({
    name: 'parity',
    build: (g) => {
      g.execute('second', { executor: 'zebra' });
      g.execute('first', opts);
    },
  }).plan.requiredExecutors;
  assert.deepEqual(symbolic, builder);
});
