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
  const loop = root.nodes.at(-1);
  assert.equal(loop.kind, 'loop');
  assert.equal(loop.maxIterations, 3);
  const value = { op: 'field', expr: { op: 'state' }, name: 'value' };
  const pass = { op: 'field', expr: { op: 'state' }, name: 'pass' };
  // The bound is part of `while`, so reaching it ends the loop instead of pausing.
  assert.deepEqual(loop.while, {
    op: 'and',
    left: { op: 'lt', left: value, right: { op: 'literal', value: 3 } },
    right: { op: 'lt', left: pass, right: { op: 'literal', value: 3 } },
  });
  const body = plan.subgraphs[loop.bodyRef];
  assert.deepEqual(body.output, {
    value: { expr: { op: 'add', left: value, right: { op: 'literal', value: 1 } } },
    pass: { expr: { op: 'add', left: pass, right: { op: 'literal', value: 1 } } },
  });
  assert.deepEqual(root.output.result, { expr: { op: 'field', expr: { op: 'ref', node: 'refine', port: 'result' }, name: 'value' } });
});

test('refine with zero iterations returns the initial value without a loop', () => {
  const { plan } = defineWorkflow({
    name: 'refine-zero',
    build(g) {
      const result = refine(g, 'refine', {
        initial: { expr: literal(7) },
        maxIterations: 0,
        continueWhile: () => literal(true),
        refine: () => ({ expr: literal(0) }),
      });
      g.output({ result });
    },
  });
  assert.deepEqual(plan.subgraphs.root.nodes, []);
  assert.deepEqual(plan.subgraphs.root.output.result, { expr: { op: 'literal', value: 7 } });
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
