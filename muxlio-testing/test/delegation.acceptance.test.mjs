import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { cases, context, assertSemantics } from './fixtures/delegation-cases.mjs';

test('delegation rubric rejects semantic regressions independently of client syntax', () => {
  for (const scenario of cases) {
    const expected = scenario.expected;
    assertSemantics(assert, structuredClone(expected), expected);
    // Every constrained field must be checked, including false, zero and arrays.
    for (const key of Object.keys(expected)) {
      const changed = { ...expected, [key]: null };
      assert.throws(() => assertSemantics(assert, changed, expected), { code: 'ERR_ASSERTION' });
    }
  }
  for (const [id, regression] of [
    ['one-off', { decision: 'muxlio' }],
    ['independent-review', { submissions: 1 }],
    ['workspace-blocked', { submissions: 1 }],
    ['simulation', { evidence: 'verified' }],
    ['accepted-run', { evidence: 'verified' }],
    ['worker-task', { recursiveDelegation: true }],
  ]) {
    const scenario = cases.find(c => c.id === id);
    assert.throws(() => assertSemantics(assert, { ...scenario.expected, ...regression }, scenario.expected));
  }
});

// Opt-in: this invokes real authoring clients and may consume model quota.
// No provider CLI syntax or adapter protocol belongs in this harness.
const driverConfig = process.env.MUXLIO_DELEGATION_DRIVERS;
test('prompt acceptance: clients preserve shared delegation semantics', { skip: !driverConfig }, () => {
  const drivers = JSON.parse(driverConfig);
  assert.ok(Object.keys(drivers).length >= 2, 'compare at least two authoring clients');
  const instructions = readFileSync(new URL('../../muxlio-skills/SKILL.md', import.meta.url), 'utf8');
  const vocabulary = {
    decision: 'local | muxlio | blocked | worker',
    steps: 'ordered semantic roles, e.g. research, draft, verify, rewrite, audit',
    dependencies: 'pairs of semantic roles [before, after]',
    executorSelection: 'advertised | client-name',
    preflight: 'required | blocked | omitted',
    approval: 'pending | not-required',
    boundedRecovery: 'boolean', recursiveDelegation: 'boolean', submissions: 'number of new runs',
    evidence: 'none | simulation | submitted | verified', inspectRunId: 'existing run ID',
    schedule: '{ day, time, timezone, created }',
  };
  for (const scenario of cases) {
    let baseline;
    for (const [client, command] of Object.entries(drivers)) {
      assert.ok(Array.isArray(command) && command.length > 0 && command.every(s => typeof s === 'string'));
      const result = JSON.parse(execFileSync(command[0], command.slice(1), {
        input: JSON.stringify({ client, instructions, vocabulary,
          prompt: `You are working in ${client}. ${scenario.prompt}`,
          context: { ...context, ...scenario.context }, mode: 'sandbox-proposal-only' }),
        encoding: 'utf8', timeout: 120_000, maxBuffer: 4 * 1024 * 1024,
      }));
      assert.equal(typeof result.response, 'string');
      assert.ok(result.response.trim(), `${client}/${scenario.id}: missing native response`);
      assertSemantics(assert, result.semantics, scenario.expected);
      const projection = Object.fromEntries(Object.keys(scenario.expected).map(key => [key, result.semantics[key]]));
      if (baseline) assert.deepEqual(projection, baseline, `${client}/${scenario.id}: client-specific behavior`);
      baseline = projection;
    }
  }
});
