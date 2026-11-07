// Summarize raw observations without equating workloads, models, or environments.
import { readFileSync, readdirSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { pathToFileURL } from 'node:url';

const read = path => JSON.parse(readFileSync(path, 'utf8'));
export function percentile(values, fraction) {
  if (!values.length) return null;
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.max(0, Math.ceil(fraction * sorted.length) - 1)];
}
function availableSum(values) {
  const reported = values.filter(v => v !== null && v !== undefined);
  return reported.length ? reported.reduce((a, b) => a + b, 0) : null;
}
export function summarize(manifest, observations, rawDirectory) {
  if (manifest.schemaVersion !== 1 || manifest.suite !== 'muxlio-reference/v1' ||
      !Number.isInteger(manifest.repetitions) || manifest.repetitions < 1 ||
      !Array.isArray(manifest.cases) || !manifest.cases.length ||
      manifest.cases.some(name => typeof name !== 'string' || !name) ||
      new Set(manifest.cases).size !== manifest.cases.length) throw Error('unsupported suite or invalid manifest');
  const seen = new Set();
  for (const o of observations) {
    const id = `${o.case}/${o.repetition}`;
    if (o.schemaVersion !== 1 || !manifest.cases.includes(o.case) || seen.has(id) ||
        !Number.isInteger(o.repetition) || o.repetition < 1 || o.repetition > manifest.repetitions ||
        !Number.isFinite(o.wallTimeMs) || o.wallTimeMs < 0 || typeof o.correct !== 'boolean' ||
        !Number.isInteger(o.manualInterventions) || o.manualInterventions < 0 ||
        typeof o.manualInterventionRequired !== 'boolean' ||
        ![true, false, null].includes(o.recoveryCorrect) ||
        ![true, false, null].includes(o.cancellationCorrect) ||
        !Array.isArray(o.queue) || o.queue.some(q => !q || !Number.isFinite(q.delayMs) || q.delayMs < 0 ||
          (q.cancelled !== undefined && typeof q.cancelled !== 'boolean')) ||
        !o.usage?.costUsd ||
        !['observed', 'estimated'].every(key => o.usage.costUsd[key] === null ||
          (Number.isFinite(o.usage.costUsd[key]) && o.usage.costUsd[key] >= 0)) ||
        !Number.isInteger(o.usage.costUsd.unknown) || o.usage.costUsd.unknown < 0) {
      throw Error(`invalid or duplicate observation ${id}`);
    }
    seen.add(id);
  }
  return {
    rawDirectory, manifest,
    cases: manifest.cases.map(name => {
      const samples = observations.filter(o => o.case === name);
      const correct = samples.filter(o => o.correct);
      const queue = samples.flatMap(o => o.queue.filter(q => !q.cancelled).map(q => q.delayMs));
      return {
        case: name, planned: manifest.repetitions, collected: samples.length,
        missing: manifest.repetitions - samples.length,
        correct: correct.length, failed: samples.length - correct.length,
        // Failed and cancelled runs remain visible. Never treat a fast failure as a speedup.
        allWallTimeMs: samples.map(o => o.wallTimeMs),
        correctWallMedianMs: percentile(correct.map(o => o.wallTimeMs), 0.5),
        correctWallP95Ms: percentile(correct.map(o => o.wallTimeMs), 0.95),
        queueSampleCount: queue.length, queueMedianMs: percentile(queue, 0.5), queueP95Ms: percentile(queue, 0.95),
        cancelledQueueSamples: samples.flatMap(o => o.queue).filter(q => q.cancelled).length,
        recovery: samples.map(o => o.recoveryCorrect),
        cancellation: samples.map(o => o.cancellationCorrect),
        manualInterventions: samples.reduce((n, o) => n + o.manualInterventions, 0),
        manualInterventionRequired: samples.filter(o => o.manualInterventionRequired).length,
        costUsd: {
          observedSubtotal: availableSum(samples.map(o => o.usage.costUsd.observed)),
          estimatedSubtotal: availableSum(samples.map(o => o.usage.costUsd.estimated)),
          unknownAttempts: samples.reduce((n, o) => n + o.usage.costUsd.unknown, 0),
          // Subtotals never represent the total cost when any measurements are missing.
          basis: manifest.costBasis,
        },
        samples: samples.map(o => ({ repetition: o.repetition, runId: o.run?.id ?? null, error: o.error ?? null })),
      };
    }),
  };
}
export function compare(directories) {
  return {
    schemaVersion: 1,
    interpretation: 'Descriptive configuration comparison only. Different providers, models, hardware, concurrency, warm caches, or fault boundaries are not equivalent. No speedup or quality claims are inferred. Failed and missing samples are retained; cost subtotals may be incomplete.',
    configurations: directories.map(directory => {
      const dir = resolve(directory);
      const manifest = read(join(dir, 'manifest.json'));
      const observations = readdirSync(dir, { withFileTypes: true })
        .filter(entry => entry.isDirectory() && /-\d+$/.test(entry.name))
        .flatMap(entry => {
          try {
            return [read(join(dir, entry.name, 'observation.json'))];
          } catch (error) {
            // Setup may fail after making a case directory but before creating
            // a run. Keep that repetition missing rather than hiding the suite.
            if (error.code === 'ENOENT') return [];
            throw error;
          }
        });
      return summarize(manifest, observations, dir);
    }),
  };
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  if (process.argv.length < 3) throw Error('usage: node benchmarks/compare.mjs RAW_DIRECTORY [RAW_DIRECTORY ...]');
  process.stdout.write(`${JSON.stringify(compare(process.argv.slice(2)), null, 2)}\n`);
}
