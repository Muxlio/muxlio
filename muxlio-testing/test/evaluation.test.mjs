import { test } from 'node:test';
import assert from 'node:assert/strict';
import { z } from 'zod';
import { workflow } from '@muxlio/sdk';
import { defineDataset, evaluateWorkflow, testWorkflow } from '../dist/index.js';

const compiled = () => workflow('evaluation', { build: () => ({ answer: 42, extra: true }) });
const dataset = () => defineDataset({
  id: 'questions/v1', input: z.object({ question: z.string() }), output: z.object({ answer: z.number() }),
  cases: [
    { id: 'a', input: { question: 'a' }, expected: { answer: 42 } },
    { id: 'b', input: { question: 'b' } },
    { id: 'c', input: { question: 'c' } },
  ],
});
const versions = () => [{ id: 'v1', workflow: compiled() }, { id: 'v2', workflow: compiled() }];

test('bounded runs preserve ordering, execution status, zero and unavailable usage independently of scores', async () => {
  let active = 0, peak = 0;
  const { manifest, results } = await evaluateWorkflow({
    dataset: dataset(), versions: versions(), concurrency: 2,
    runner: { id: 'fixture/v1', async run(w, { input }) {
      active++; peak = Math.max(peak, active);
      await new Promise(resolve => setTimeout(resolve, input.question === 'a' ? 15 : 1));
      active--;
      if (input.question === 'c') throw new Error('unavailable');
      return { state: input.question === 'b' ? 'failed' : 'succeeded', error: input.question === 'b' ? 'execution failed' : undefined,
        output: { answer: 0 }, usage: input.question === 'a' ? { tokens: { observed: 0, estimated: null, unknown: 0 }, costUsd: { observed: null, estimated: null, unknown: 1 } } : undefined };
    } },
    scorers: [
      { id: 'match/v1', score: ({ output, expected }) => Number(output.answer === expected?.answer) },
      { id: 'broken/v1', score: () => { throw new Error('scorer failed'); } },
      { id: 'nan/v1', score: () => NaN },
    ],
  });
  assert.equal(peak, 2);
  assert.deepEqual(results.map(r => [r.versionId, r.caseId]), [['v1','a'],['v1','b'],['v1','c'],['v2','a'],['v2','b'],['v2','c']]);
  assert.equal(results[0].execution.success, true);
  assert.equal(results[0].scores['match/v1'].value, 0);
  assert.equal(results[0].scores['broken/v1'].error, 'scorer failed');
  assert.equal(results[0].scores['nan/v1'].value, null);
  assert.equal(results[0].execution.usage.tokens.observed, 0);
  assert.equal(results[1].execution.success, false);
  assert.equal(results[1].execution.error, 'execution failed');
  assert.equal(results[1].execution.usage, null);
  assert.deepEqual(Object.keys(results[1].scores), []);
  assert.equal(results[2].execution.error, 'unavailable');
  assert.ok(results.every(r => r.durationMs >= 0));
  assert.match(manifest.id, /^sha256:/);
});

test('pins plans, inputs and configuration and gives stable content identities', async () => {
  const d = dataset(), v = versions(), config = { executors: { byNode: {} } };
  const runner = { id: 'fixture/v1', async run(w, opts) {
    assert.equal(opts.input.question.length, 1);
    assert.equal(w.plan.name, 'evaluation');
    w.plan.name = 'runner mutation';
    opts.input.question = 'runner mutation';
    opts.executors.byNode.changed = { outputs: {} };
    return { state: 'succeeded', output: { answer: 42 } };
  } };
  const options = { dataset: d, versions: v, configuration: config, runner };
  const pending = evaluateWorkflow(options);
  d.cases[1].input.question = 'caller mutation';
  v[0].workflow.plan.name = 'caller mutation';
  config.executors.byNode.changed = { outputs: {} };
  const first = await pending;
  const second = await evaluateWorkflow({ ...options, dataset: dataset(), versions: versions(), configuration: { executors: { byNode: {} } } });
  assert.equal(first.manifest.id, second.manifest.id);
  assert.equal(first.manifest.versions[0].plan.name, 'evaluation');
  const identityBase = { dataset: dataset(), versions: versions(), runner: { id: 'fixture/v1', run: async () => ({ state: 'failed' }) } };
  const baselineId = (await evaluateWorkflow(identityBase)).manifest.id;
  for (const override of [
    { configuration: { cliPath: 'different' } },
    { dataset: { ...dataset(), cases: [{ id: 'a', input: { question: 'x' }, expected: { answer: 1 } }] } },
    { scorers: [{ id: 'score/v2', score: () => 1 }] },
    { runner: { ...runner, id: 'fixture/v2' } },
    { versions: [{ id: 'v1', workflow: workflow('changed', { build: () => ({ answer: 1 }) }) }] },
  ]) {
    // Identity checks don't need the mutation assertions of the original runner.
    const result = await evaluateWorkflow({ dataset: dataset(), versions: versions(), runner: { id: 'fixture/v1', run: async () => ({ state: 'failed' }) }, ...override });
    assert.notEqual(result.manifest.id, baselineId);
  }
});

test('validates all cases before execution and rejects duplicate identities and invalid concurrency', async () => {
  let calls = 0;
  const base = { dataset: dataset(), versions: versions(), runner: { id: 'fixture/v1', async run() { calls++; return { state: 'succeeded' }; } } };
  for (const concurrency of [0, -1, 1.5, Infinity]) await assert.rejects(evaluateWorkflow({ ...base, concurrency }));
  await assert.rejects(evaluateWorkflow({ ...base, dataset: { ...dataset(), cases: [{ id: 'a', input: { question: 4 } }] } }));
  await assert.rejects(evaluateWorkflow({ ...base, dataset: { ...dataset(), cases: [{ id: 'a', input: { question: 'a' }, expected: { answer: 'invalid' } }] } }));
  await assert.rejects(evaluateWorkflow({ ...base, versions: [versions()[0], versions()[0]] }), /unique/);
  await assert.rejects(evaluateWorkflow({ ...base, scorers: [{ id: 'x', score: () => 1 }, { id: 'x', score: () => 0 }] }), /unique/);
  await assert.rejects(evaluateWorkflow({ ...base, dataset: { ...dataset(), cases: [dataset().cases[0], dataset().cases[0]] } }), /unique/);
  assert.equal(calls, 0);
});

test('output validation and optional expectations do not change execution success', async () => {
  const result = await evaluateWorkflow({ dataset: dataset(), versions: versions(),
    runner: { id: 'fixture/v1', async run(w, { input }) { return { state: 'succeeded', output: input.question === 'a' ? 'invalid' : { answer: 42 } }; } },
    scorers: [{ id: 'optional/v1', score: ({ expected }) => expected === undefined ? 1 : 0 }],
  });
  assert.equal(result.results[0].execution.success, true);
  assert.ok(result.results[0].outputError);
  assert.equal(result.results[1].scores['optional/v1'].value, 1);
});

test('real CLI harness evaluates pinned plans and retains structured execution failures', { skip: !process.env.MUXLIO_TEST_CLI }, async () => {
  const { task } = await import('@muxlio/sdk');
  const cliPath = process.env.MUXLIO_TEST_CLI;
  const result = await evaluateWorkflow({ dataset: dataset(), versions: versions(), concurrency: 2, configuration: { cliPath },
    scorers: [{ id: 'match/v1', score: ({ output }) => Number(output.answer === 42) }],
  });
  assert.ok(result.results.every(r => r.execution.success && r.scores['match/v1'].value === 1));
  const failing = workflow('failure', { build: ({ step }) => {
    const value = step('fail', task({ executor: 'fixture', outputs: { answer: z.number() } }), {});
    return { answer: value.answer };
  } });
  const failed = await testWorkflow(failing, { input: {}, cliPath, executors: { byNode: { fail: { outputs: {}, error: 'fixture failure' } } } });
  assert.equal(failed.state, 'failed');
  assert.ok(failed.error);
  assert.equal(failed.usage.tokens.observed, null);
  assert.ok(failed.usage.tokens.unknown > 0);
});

test('normalizes transforming case parsers once and revalidates caller changes', async () => {
  const d = defineDataset({ id: 'transformed/v1',
    input: z.number().transform(n => n + 1), output: z.number().transform(n => n + 10),
    cases: [{ id: 'a', input: 1, expected: 2 }],
  });
  const run = async () => evaluateWorkflow({ dataset: d, versions: versions(),
    runner: { id: 'fixture/v1', async run(w, { input }) {
      assert.equal(input, 2);
      return { state: 'succeeded', output: 2 };
    } },
    scorers: [{ id: 'match/v1', score: ({ output, expected }) => Number(output === expected) }],
  });
  assert.equal(defineDataset(d), d);
  for (let i = 0; i < 2; i++) {
    const report = await run();
    assert.ok(report.results.every(r => r.execution.success && r.scores['match/v1'].value === 1));
  }
  d.cases[0].input = 'invalid';
  await assert.rejects(run());
});

test('rejects lossy JSON inputs and configuration before launching any runs', async () => {
  let calls = 0;
  const base = { versions: versions(), runner: { id: 'fixture/v1', async run() {
    calls++; return { state: 'succeeded', output: 0 };
  } } };
  for (const input of [NaN, Infinity, -Infinity, [undefined], () => 1, Symbol('value'), 1n]) {
    await assert.rejects(evaluateWorkflow({ ...base,
      dataset: { id: 'json/v1', input: z.any(), output: z.any(), cases: [{ id: 'a', input }] },
    }), /JSON serializable/);
  }
  await assert.rejects(evaluateWorkflow({ ...base, dataset: dataset(),
    configuration: { executors: { byNode: { a: { outputs: { score: NaN } } } } },
  }), /JSON serializable/);
  assert.equal(calls, 0);
});

test('isolates returned execution values from runner reuse and output parser mutations', async () => {
  const shared = { state: 'succeeded', output: { answer: 0 },
    usage: { tokens: { observed: 0, estimated: null, unknown: 0 }, costUsd: { observed: null, estimated: null, unknown: 1 } },
  };
  let n = 0;
  const report = await evaluateWorkflow({ dataset: { ...dataset(), output: { parse(value) {
    const answer = value.answer;
    value.answer = -1;
    return { answer };
  } } }, versions: versions(),
    runner: { id: 'reuse/v1', async run() {
      shared.output.answer = ++n;
      shared.usage.tokens.observed = n;
      return shared;
    } },
    scorers: [{ id: 'answer/v1', score: ({ output }) => output.answer }],
  });
  assert.deepEqual(report.results.map(r => r.execution.output.answer), [1, 2, 3, 4, 5, 6]);
  assert.deepEqual(report.results.map(r => r.execution.usage.tokens.observed), [1, 2, 3, 4, 5, 6]);
  assert.deepEqual(report.results.map(r => r.scores['answer/v1'].value), [1, 2, 3, 4, 5, 6]);
});

test('non-JSON runner outputs report output errors independently of execution success', async () => {
  const report = await evaluateWorkflow({ dataset: dataset(), versions: versions(),
    runner: { id: 'invalid-output/v1', async run() {
      return { state: 'succeeded', output: { answer: NaN } };
    } },
    scorers: [{ id: 'unused/v1', score: () => assert.fail('invalid output was scored') }],
  });
  assert.ok(report.results.every(r => r.execution.success && r.outputError && Object.keys(r.scores).length === 0));
});
