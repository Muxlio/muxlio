import assert from 'node:assert/strict';
import test from 'node:test';
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { summarize, percentile, compare } from './compare.mjs';
const manifest = { schemaVersion: 1, suite: 'muxlio-reference/v1', cases: ['recovery', 'coding'], repetitions: 3, costBasis: 'reported' };
const sample = (repetition, overrides = {}) => ({
  schemaVersion: 1, case: 'recovery', repetition, correct: true, wallTimeMs: 100,
  queue: [{ delayMs: 20 }], recoveryCorrect: true, cancellationCorrect: null,
  manualInterventions: 0, manualInterventionRequired: false,
  usage: { costUsd: { observed: null, estimated: null, unknown: 2 } }, ...overrides,
});
test('reports failures, missing repetitions and recovery without treating failure as speedup', () => {
  const report = summarize(manifest, [sample(1), sample(2, { correct: false, wallTimeMs: 1, recoveryCorrect: false, manualInterventions: 1, manualInterventionRequired: true, error: 'reconciliation' })], '/raw');
  const row = report.cases[0];
  assert.equal(row.correctWallMedianMs, 100);
  assert.deepEqual(row.allWallTimeMs, [100, 1]);
  assert.equal(row.failed, 1);
  assert.equal(row.missing, 1);
  assert.deepEqual(row.recovery, [true, false]);
  assert.equal(row.manualInterventions, 1);
  assert.equal(row.manualInterventionRequired, 1);
  assert.equal(report.cases[1].collected, 0);
  assert.equal(report.cases[1].correctWallMedianMs, null);
});
test('preserves unavailable, observed zero, estimates and unknown costs separately', () => {
  const unknown = summarize(manifest, [sample(1)], '/raw').cases[0].costUsd;
  assert.equal(unknown.observedSubtotal, null);
  assert.equal(unknown.estimatedSubtotal, null);
  const known = summarize(manifest, [sample(1), sample(2, { usage: { costUsd: { observed: 0, estimated: 0.25, unknown: 0 } } })], '/raw').cases[0].costUsd;
  assert.equal(known.observedSubtotal, 0);
  assert.equal(known.estimatedSubtotal, 0.25);
  assert.equal(known.unknownAttempts, 2);
});
test('rejects duplicate, mismatched, nonfinite and negative observations', () => {
  for (const observations of [[sample(1), sample(1)], [sample(1, { case: 'other' })], [sample(4)], [sample(1, { wallTimeMs: NaN })], [sample(1, { queue: [{ delayMs: -1 }] })]]) {
    assert.throws(() => summarize(manifest, observations, '/raw'));
  }
});
test('nearest rank percentiles handle empty and unsorted samples', () => {
  assert.equal(percentile([], 0.95), null);
  assert.equal(percentile([300, 100, 200], 0.5), 200);
  assert.equal(percentile([300, 100, 200], 0.95), 300);
});
test('keeps cancelled admissions separate from started queue delays', () => {
  const row = summarize(manifest, [sample(1, { queue: [{ delayMs: 10 }, { delayMs: 999, cancelled: true }] })], '/raw').cases[0];
  assert.equal(row.queueMedianMs, 10);
  assert.equal(row.queueSampleCount, 1);
  assert.equal(row.cancelledQueueSamples, 1);
});
test('rejects malformed cancellation flags instead of silently excluding queue samples', () => {
  for (const cancelled of ['false', 1, null]) {
    assert.throws(() => summarize(manifest, [sample(1, { queue: [{ delayMs: 999, cancelled }] })], '/raw'));
  }
});
test('rejects invalid manifests, intervention counts and cost data', () => {
  for (const override of [{ repetitions: 0 }, { cases: ['coding', 'coding'] }, { cases: null }]) {
    assert.throws(() => summarize({ ...manifest, ...override }, [], '/raw'));
  }
  for (const override of [{ manualInterventions: -1 }, { manualInterventionRequired: null }, { recoveryCorrect: 'yes' },
    { usage: { costUsd: { observed: -1, estimated: null, unknown: 0 } } },
    { usage: { costUsd: { observed: null, estimated: null, unknown: -1 } } }]) {
    assert.throws(() => summarize(manifest, [sample(1, override)], '/raw'));
  }
});
test('reads raw bundles and counts setup failures as missing observations', t => {
  const directory = mkdtempSync(join(tmpdir(), 'muxlio-benchmarks-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  writeFileSync(join(directory, 'manifest.json'), JSON.stringify(manifest));
  mkdirSync(join(directory, 'recovery-001'));
  mkdirSync(join(directory, 'recovery-002'));
  const path = join(directory, 'recovery-001', 'observation.json');
  writeFileSync(path, JSON.stringify(sample(1)));
  const report = compare([directory]);
  assert.equal(report.configurations[0].cases[0].collected, 1);
  assert.equal(report.configurations[0].cases[0].missing, 2);
  writeFileSync(path, 'broken JSON');
  assert.throws(() => compare([directory]));
});
