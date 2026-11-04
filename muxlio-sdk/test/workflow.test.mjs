import test from 'node:test';
import assert from 'node:assert/strict';
import { workflow, task, artifact } from '../dist/index.js';
import { z } from 'zod';

test('command tasks infer process outputs without declarations', () => {
  const { plan } = workflow('inferred-command', {
    build: ({ step }) => {
      const result = step('run', task({ executor: 'command' }), {});
      return {
        passed: result.passed, exitCode: result.exitCode, signal: result.signal,
        stdout: result.stdout, stderr: result.stderr, durationMs: result.durationMs,
      };
    },
  });
  const root = plan.subgraphs.root;
  for (const port of ['passed', 'exitCode', 'signal', 'stdout', 'stderr', 'durationMs']) {
    assert.ok(root.nodes[0].outputs[port].schema, `${port} has runtime validation`);
    assert.deepEqual(root.output[port], { ref: { node: 'run', port } });
  }
  assert.equal(root.nodes[0].outputs._result, undefined);
});

test('command tasks merge explicit outputs without mutating the definition', () => {
  const outputs = { notes: artifact.markdown('notes.md'), stdout: z.string().min(1) };
  const command = task({ executor: 'command', outputs });
  assert.deepEqual(Object.keys(outputs), ['notes', 'stdout']);
  const { plan } = workflow('custom-command', {
    build: ({ step }) => {
      const result = step('run', command, {});
      return { passed: result.passed, notes: result.notes, stdout: result.stdout };
    },
  });
  const node = plan.subgraphs.root.nodes[0];
  assert.equal(node.outputs.notes.path, 'notes.md');
  assert.deepEqual(node.outputs.stdout.schema, { type: 'string', minLength: 1 });
  assert.deepEqual(node.outputs.passed.schema, { type: 'boolean' });
});

test('step after adds control dependency binding', () => {
  const review = task({
    executor: 'agent/codex',
    outputs: { report: artifact.markdown('review.md') },
  });
  const verify = task({
    executor: 'agent/codex',
    outputs: { passed: z.boolean() },
  });
  const { plan } = workflow('after-order', {
    build: ({ step }) => {
      const reviewed = step('review', review, {});
      step('verify', verify, { workspace: 'unused' }, { after: reviewed });
    },
  });
  const verifyNode = plan.subgraphs.root.nodes.find((n) => n.id === 'verify');
  const afterKey = Object.keys(verifyNode.inputs).find((k) => k.startsWith('__after_'));
  assert.ok(afterKey, 'expected __after_* input');
  assert.deepEqual(verifyNode.inputs[afterKey].ref, { node: 'review', port: 'report' });
});

test('nested symbolic inputs compile to object expressions', () => {
  const echo = task({
    executor: 'fake/echo',
    outputs: { value: z.string() },
  });
  const { plan } = workflow('nested-input', {
    input: z.object({
      features: z.array(z.object({ id: z.string(), feature: z.string() })),
    }),
    build: ({ input, each, step }) => {
      each(input.features, { key: (f) => f.id }, (feature) => {
        step('run', echo, { feature: { id: feature.id, feature: feature.feature } });
      });
    },
  });
  const mapNode = plan.subgraphs.root.nodes.find((n) => n.kind === 'map');
  const body = plan.subgraphs[mapNode.bodyRef];
  const run = body.nodes.find((n) => n.id === 'run');
  assert.equal(run.inputs.feature.expr.op, 'object');
  assert.equal(run.inputs.feature.expr.fields.id.op, 'field');
  assert.equal(run.inputs.feature.expr.fields.feature.op, 'field');
});

test('repeatUntil lifts an outer after dependency onto the loop node', () => {
  const review = task({
    executor: 'agent/codex',
    outputs: { report: artifact.markdown('review.md') },
  });
  const verify = task({
    executor: 'agent/codex',
    outputs: { passed: z.boolean(), output: artifact.markdown('verification.md') },
  });
  const fix = task({
    executor: 'agent/cursor',
    outputs: { notes: artifact.markdown('fix.md') },
  });
  const { plan } = workflow('loop-after', {
    build: ({ step, repeatUntil }) => {
      const reviewed = step('review', review, {});
      repeatUntil('verification', {
        attempt: () => step('verify', verify, {}, { after: reviewed }),
        until: (r) => r.passed,
        otherwise: () => step('fix', fix, {}),
        maxAttempts: 2,
      });
    },
  });
  const loop = plan.subgraphs.root.nodes.find((n) => n.id === 'verification');
  const afterKey = Object.keys(loop.inputs).find((k) => k.startsWith('__after_'));
  assert.ok(afterKey, 'expected loop __after_* input');
  assert.deepEqual(loop.inputs[afterKey].ref, { node: 'review', port: 'report' });
});

test('workflow compiles repeatUntil with valid branch condition expr', () => {
  const verify = task({
    executor: 'agent/codex',
    outputs: { passed: z.boolean(), output: artifact.markdown('verification.md') },
  });
  const fix = task({
    executor: 'agent/cursor',
    outputs: { notes: artifact.markdown('fix.md') },
  });

  const { plan } = workflow('loop-shape', {
    input: z.object({ ok: z.boolean() }),
    build: ({ repeatUntil, step }) => {
      repeatUntil('v', {
        attempt: () => step('verify', verify, {}),
        until: r => r.passed,
        otherwise: r => step('fix', fix, { failures: r.output }),
        maxAttempts: 2,
        onExhausted: 'pause',
      });
    },
  });

  const loop = plan.subgraphs.root.nodes.find((n) => n.kind === 'loop');
  assert.ok(loop);
  assert.equal(loop.onExhausted, 'pause');
  assert.equal(loop.stateRef, undefined);

  const body = plan.subgraphs[loop.bodyRef];
  const branch = body.nodes.find((n) => n.kind === 'branch');
  assert.equal(branch.condition.op, 'and');

  const fixSG = plan.subgraphs[branch.thenRef];
  const fixNode = fixSG.nodes.find((n) => n.id === 'fix');
  assert.equal(fixNode.inputs.failures.ref.node, 'v__cap');
  assert.equal(fixNode.inputs.failures.ref.port, 'value_output');
});

test('identical workflow source yields identical digests', () => {
  const def = {
    name: 'digest-stable',
    input: z.object({ id: z.string() }),
    build: ({ input, step }) => {
      const echo = task({
        executor: 'fake/echo',
        outputs: { value: z.string() },
      });
      step('run', echo, { id: input.id });
    },
  };
  const a = workflow(def.name, def).plan.digest;
  const b = workflow(def.name, def).plan.digest;
  assert.equal(a, b);
});

test('unknown step output port fails at compile time', () => {
  const echo = task({ executor: 'fake/echo', outputs: { value: z.string() } });
  assert.throws(() => {
    workflow('bad-port', {
      build: ({ step }) => {
        const s = step('run', echo, {});
        void s.notARealPort;
      },
    }).plan;
  }, /not declared on step/);
});
