import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import compiled from './workflow.ts';

const cli = process.env.MUXLIO_TEST_CLI;
const options = { skip: cli ? false : 'Set MUXLIO_TEST_CLI to a current muxlio binary' };

function run(t, done, malformed = false) {
  const dir = mkdtempSync(join(tmpdir(), 'muxlio-research-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const plan = structuredClone(compiled.plan);
  // Avoid production retry backoff when intentionally returning invalid data.
  if (malformed) {
    for (const graph of Object.values(plan.subgraphs)) {
      for (const node of graph.nodes) if (node.kind === 'execute') node.retry = { maxAttempts: 1, effect: 'retryable' };
    }
    delete plan.digest;
  }
  const files = {
    plan,
    input: { topic: 'Test topic', questions: ['Test question'] },
    fake: { byNode: {
      iterate: { outputs: {
        findings: 'complete findings artifact',
        progress: { iteration: 1, done: malformed ? 'true' : done, answeredCount: done ? 1 : 0,
          partialCount: 0, openCount: done ? 0 : 1, totalQuestions: 1, thisIterationQuestion: null },
      } },
      synthesize: { outputs: { report: 'report artifact', metrics: 'metrics artifact', canvas: 'canvas artifact' } },
    } },
  };
  for (const [name, value] of Object.entries(files)) writeFileSync(join(dir, name + '.json'), JSON.stringify(value));
  const result = spawnSync(cli, ['run', join(dir, 'plan.json'), join(dir, 'input.json'),
    '--fake-config', join(dir, 'fake.json'), '--data-dir', join(dir, 'state'), '--json'], {
    encoding: 'utf8', timeout: 30000, maxBuffer: 8 * 1024 * 1024,
  });
  assert.ok(result.stdout, result.stderr || result.error?.message);
  const output = JSON.parse(result.stdout);
  return { ...output, queued: output.events.filter(event => event.type === 'activation.queued') };
}

test('real interpreter accepts completion and returns final artifacts', options, t => {
  const result = run(t, true);
  assert.equal(result.state, 'succeeded', result.error);
  assert.deepEqual(result.output, { report: 'report artifact', metrics: 'metrics artifact', canvas: 'canvas artifact', iterations: 1 });
  assert.deepEqual(result.queued.map(event => event.payload.nodeId), ['research', 'iterate', 'synthesize']);
});

test('real interpreter pauses after ten incomplete passes without synthesis', options, t => {
  const result = run(t, false);
  assert.equal(result.state, 'paused', result.error);
  assert.equal(result.queued.filter(event => event.payload.nodeId === 'iterate').length, 10);
  assert.equal(result.queued.some(event => event.payload.nodeId === 'synthesize'), false);
});

test('real interpreter rejects malformed progress before synthesis', options, t => {
  const result = run(t, true, true);
  assert.equal(result.state, 'failed', result.error);
  assert.equal(result.queued.some(event => event.payload.nodeId === 'synthesize'), false);
});
