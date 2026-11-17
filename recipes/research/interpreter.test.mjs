import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import compiled from './workflow.ts';
import { zodToJsonSchema } from '@muxlio/sdk';
import { createResearchSchemas } from './schemas.ts';

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
    fake: { byExecutor: { 'agent/test': { outputs: {} } }, byNode: {
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

test('real interpreter pauses after forty incomplete passes without synthesis', options, t => {
  const result = run(t, false);
  assert.equal(result.state, 'paused', result.error);
  assert.equal(result.queued.filter(event => event.payload.nodeId === 'iterate').length, 40);
  assert.equal(result.queued.some(event => event.payload.nodeId === 'synthesize'), false);
});

test('real interpreter rejects malformed progress before synthesis', options, t => {
  const result = run(t, true, true);
  assert.equal(result.state, 'failed', result.error);
  assert.equal(result.queued.some(event => event.payload.nodeId === 'synthesize'), false);
});

test('real admission rejects input that exceeds declared research capacity', options, t => {
  const dir = mkdtempSync(join(tmpdir(), 'muxlio-research-admission-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const plan = structuredClone(compiled.plan);
  delete plan.digest;
  plan.subgraphs.root.nodes[0].maxIterations = 10;
  plan.inputSchema = zodToJsonSchema(createResearchSchemas(10).input);
  const input = { topic: 'Research', questions: Array.from({ length: 12 }, (_, i) => 'Question ' + i) };
  for (const [name, value] of Object.entries({ plan, input, fake: {} })) writeFileSync(join(dir, name + '.json'), JSON.stringify(value));
  const result = spawnSync(cli, ['run', join(dir, 'plan.json'), join(dir, 'input.json'),
    '--fake-config', join(dir, 'fake.json'), '--data-dir', join(dir, 'state'), '--json'], {
    encoding: 'utf8', timeout: 10000, maxBuffer: 1024 * 1024,
  });
  assert.equal(result.status, 1);
  const diagnostic = result.stdout + result.stderr;
  assert.match(diagnostic, /questions/);
  assert.match(diagnostic, /10/);
  assert.doesNotMatch(diagnostic, /activation.started|attempt.started/,
    'capacity must be checked before any executor work starts');
});
