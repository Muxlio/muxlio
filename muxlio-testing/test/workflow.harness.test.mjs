import { test } from 'node:test';
import assert from 'node:assert/strict';
import { writeFileSync, mkdtempSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { workflow } from '@muxlio/sdk';
import { evaluateWorkflow, defineDataset, testWorkflow } from '../dist/index.js';
import { z } from 'zod';

function fakeCli(stdoutJson, exitCode) {
  const dir = mkdtempSync(join(tmpdir(), 'muxlio-cli-'));
  const cliPath = join(dir, 'muxlio');
  writeFileSync(
    cliPath,
    `#!/bin/sh\necho ${JSON.stringify(stdoutJson)}\nexit ${exitCode}\n`,
    { mode: 0o755 },
  );
  return cliPath;
}

const compiled = () => workflow('harness', { build: () => ({ answer: 0 }) });

test('nonzero CLI exit cannot report succeeded execution', async () => {
  const cliPath = fakeCli('{"runId":"run-1","state":"succeeded","output":{"answer":1}}', 1);
  await assert.rejects(
    testWorkflow(compiled(), { input: {}, cliPath }),
    (error) => typeof error === 'object' && error !== null && 'stdout' in error,
  );
});

test('nonzero CLI exit still returns structured workflow failures', async () => {
  const cliPath = fakeCli(
    JSON.stringify({ runId: 'run-2', state: 'failed', error: 'fixture failure', output: null }),
    1,
  );
  const result = await testWorkflow(compiled(), { input: {}, cliPath });
  assert.equal(result.state, 'failed');
  assert.equal(result.error, 'fixture failure');
  assert.equal(result.output, null);
});

test('preserves empty string workflow errors from structured CLI failures', async () => {
  const cliPath = fakeCli(JSON.stringify({ runId: 'run-empty', state: 'failed', error: '', output: null }), 1);
  const result = await testWorkflow(compiled(), { input: {}, cliPath });
  assert.equal(result.state, 'failed');
  assert.equal(result.error, '');
});

test('nonzero CLI exit rejects nonterminal or unknown states as harness failures', async () => {
  for (const state of ['running', 'cancelled', 'unexpected']) {
    const cliPath = fakeCli(JSON.stringify({ runId: 'run-state', state }), 1);
    await assert.rejects(testWorkflow(compiled(), { input: {}, cliPath }));
  }
});

test('malformed JSON on failed CLI exit surfaces as harness failure', async () => {
  const cliPath = fakeCli('not-json', 1);
  await assert.rejects(testWorkflow(compiled(), { input: {}, cliPath }));
});

test('preserves falsy workflow outputs from successful CLI JSON', async () => {
  const cliPath = fakeCli(
    JSON.stringify({ runId: 'run-3', state: 'succeeded', output: { answer: 0, ok: false, note: null } }),
    0,
  );
  const result = await testWorkflow(compiled(), { input: {}, cliPath });
  assert.deepEqual(result.output, { answer: 0, ok: false, note: null });
});

test('evaluation harness scores numeric zero outputs from runners', async () => {
  const dataset = defineDataset({
    id: 'zero/v1',
    input: z.object({}),
    output: z.number(),
    cases: [{ id: 'a', input: {} }],
  });
  const { results } = await evaluateWorkflow({
    dataset,
    versions: [{ id: 'v1', workflow: compiled() }],
    runner: {
      id: 'fixture/zero/v1',
      async run() {
        return { state: 'succeeded', output: 0 };
      },
    },
    scorers: [{ id: 'value/v1', score: ({ output }) => output }],
  });
  assert.equal(results[0].execution.success, true);
  assert.equal(results[0].outputError, undefined);
  assert.equal(results[0].scores['value/v1'].value, 0);
});
