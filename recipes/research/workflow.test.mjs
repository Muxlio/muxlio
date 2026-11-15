import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import compiled from './workflow.ts';
import { input, progress } from './schemas.ts';

const plan = compiled.plan;
const loop = plan.subgraphs.root.nodes[0];
const body = plan.subgraphs[loop.bodyRef];
const iterate = body.nodes.find(node => node.id === 'iterate');
const synthesis = plan.subgraphs.root.nodes.find(node => node.id === 'synthesize');

test('research input accepts the example and bounds question count and depth', () => {
  input.parse(JSON.parse(readFileSync(new URL('./input.example.json', import.meta.url))));
  input.parse({ topic: 'Topic', questions: ['Question'] });
  for (const value of [
    { topic: '', questions: ['Question'] },
    { topic: 'Topic', questions: [] },
    { topic: 'Topic', questions: [''] },
    { topic: 'Topic', questions: Array(21).fill('Question') },
    { topic: 'Topic', questions: ['Question'], depth: 'unbounded' },
  ]) assert.equal(input.safeParse(value).success, false);
});

test('research carries the last complete artifact and increments a runtime counter', () => {
  assert.equal(plan.name, 'research');
  assert.equal(loop.kind, 'loop');
  assert.equal(loop.maxIterations, 10);
  assert.equal(loop.onExhausted ?? 'pause', 'pause');
  assert.deepEqual(loop.initState.value, { done: false, attempts: 0, findings: null, progress: null });
  assert.deepEqual(loop.while, { op: 'not', expr: { op: 'field', expr: { op: 'state' }, name: 'done' } });
  assert.deepEqual(iterate.inputs.previousFindings, {
    expr: { op: 'field', expr: { op: 'state' }, name: 'findings' },
  });
  assert.deepEqual(iterate.inputs.iteration, {
    expr: { op: 'add', left: { op: 'field', expr: { op: 'state' }, name: 'attempts' }, right: { op: 'literal', value: 1 } },
  });
  assert.deepEqual(body.output.findings, { ref: { node: 'iterate', port: 'findings' } });
  assert.deepEqual(body.output.done.expr, {
    op: 'field', expr: { op: 'ref', node: 'iterate', port: 'progress' }, name: 'done',
  });
  assert.equal(iterate.outputs.progress.path, undefined, 'progress must be a JSON value, not an artifact reference');
  assert.equal(iterate.outputs.progress.schema.properties.done.type, 'boolean');
  assert.deepEqual(synthesis.inputs.findings.expr, {
    op: 'field', expr: { op: 'ref', node: 'research', port: 'result' }, name: 'findings',
  });
  assert.deepEqual(synthesis.inputs.iterations.expr, {
    op: 'field', expr: { op: 'ref', node: 'research', port: 'result' }, name: 'attempts',
  });
});

test('progress rejects malformed loop control data', () => {
  const valid = { iteration: 1, done: true, answeredCount: 1, partialCount: 0, openCount: 0, totalQuestions: 1, thisIterationQuestion: null };
  progress.parse(valid);
  for (const patch of [{ done: 'true' }, { iteration: 0 }, { answeredCount: -1 }, { openCount: 0.5 }]) {
    assert.equal(progress.safeParse({ ...valid, ...patch }).success, false);
  }
});

test('final artifacts share one activation for Canvas sibling port resolution', () => {
  assert.deepEqual(synthesis.outputs, {
    report: { mime: 'text/markdown', path: 'report.md' },
    metrics: { mime: 'application/json', path: 'metrics.json' },
    canvas: { mime: 'application/vnd.muxlio.canvas+json', path: 'report.canvas.json' },
  });
  for (const port of ['report', 'metrics', 'canvas']) {
    assert.deepEqual(plan.subgraphs.root.output[port], { ref: { node: 'synthesize', port } });
  }
  assert.equal(plan.limits.maxActiveAttempts, 1);
  assert.ok(plan.limits.maxAttempts >= (loop.maxIterations + 1) * iterate.retry.maxAttempts);
});
