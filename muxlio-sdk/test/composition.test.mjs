import test from 'node:test';
import assert from 'node:assert/strict';
import { defineWorkflow, input, literal, refine, speculate } from '../dist/index.js';

test('refine compiles to a bounded state-carrying loop', () => {
  const { plan } = defineWorkflow({
    name: 'refine-test',
    build(g) {
      const initial = g.project('initial', { inputs: { value: { expr: literal(0) } } }).output('value');
      const result = refine(g, 'refine', {
        initial,
        maxIterations: 3,
        continueWhile: (state) => g.lt(state, literal(3)),
        refine: (_ctx, state) => ({ expr: g.add(state, literal(1)) }),
      });
      g.output({ result });
    },
  });

  const root = plan.subgraphs.root;
  assert.equal(root.nodes.at(-1).kind, 'loop');
  assert.equal(root.nodes.at(-1).maxIterations, 3);
  const body = plan.subgraphs[root.nodes.at(-1).bodyRef];
  assert.deepEqual(body.output, { value: { expr: { op: 'add', left: { op: 'state' }, right: { op: 'literal', value: 1 } } } });
});

test('speculate maps candidates and selects a deterministic position', () => {
  const { plan } = defineWorkflow({
    name: 'speculate-test',
    build(g) {
      const candidates = input();
      const result = speculate(g, 'speculate', {
        candidates,
        select: 1,
        evaluate: (_ctx, candidate) => ({ expr: candidate }),
      });
      g.output({ result });
    },
  });

  const root = plan.subgraphs.root;
  assert.equal(root.nodes[0].kind, 'map');
  assert.deepEqual(root.output.result.expr, {
    op: 'field',
    expr: { op: 'index', expr: { op: 'ref', node: 'speculate', port: 'items' }, index: 1 },
    name: 'value',
  });
});

test('composition helpers reject invalid bounds and selections', () => {
  const build = (configure) => defineWorkflow({ name: 'invalid', build: configure });
  assert.throws(() => build((g) => refine(g, 'r', {
    initial: { expr: literal(0) }, maxIterations: -1,
    continueWhile: () => literal(false), refine: () => ({ expr: literal(0) }),
  })), /maxIterations/);
  assert.throws(() => build((g) => speculate(g, 's', {
    candidates: input(), select: -1, evaluate: (_ctx, c) => ({ expr: c }),
  })), /select/);
});
