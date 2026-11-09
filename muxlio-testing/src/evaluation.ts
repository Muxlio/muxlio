import { createHash } from 'node:crypto';
import { performance } from 'node:perf_hooks';
import { computeDigest, type CompiledWorkflow, type Plan } from '@muxlio/sdk';
import { testWorkflow, type TestWorkflowOptions, type UsageSummary } from './index.js';

/** A Zod schema or another parser with the same typed contract. */
export interface CaseSchema<T> { parse(value: unknown): T }

export interface EvaluationCase<I, O> {
  id: string;
  input: I;
  expected?: O;
}

export interface EvaluationDataset<I, O> {
  id: string;
  cases: readonly EvaluationCase<I, O>[];
  input: CaseSchema<I>;
  output: CaseSchema<O>;
}

// Remember normalization only while both schemas and case contents are unchanged.
// This avoids applying transforming parsers twice, without trusting mutated cases.
const normalizedDatasets = new WeakMap<object, {
  input: unknown; output: unknown; casesDigest: string;
}>();

/** Validate and normalize cases before starting any executions. */
export function defineDataset<I, O>(dataset: EvaluationDataset<I, O>): EvaluationDataset<I, O> {
  if (!dataset.id.trim()) throw new Error('dataset identity must be nonempty');
  uniqueIds(dataset.cases, 'case');
  const cached = normalizedDatasets.get(dataset);
  if (cached?.input === dataset.input && cached?.output === dataset.output &&
      cached.casesDigest === digest(snapshot(dataset.cases))) return dataset;
  const normalized = {
    ...dataset,
    cases: snapshot(dataset.cases).map(c => ({
      id: c.id,
      input: dataset.input.parse(c.input),
      ...(c.expected === undefined ? {} : { expected: dataset.output.parse(c.expected) }),
    })),
  };
  normalizedDatasets.set(normalized, {
    input: dataset.input, output: dataset.output,
    casesDigest: digest(snapshot(normalized.cases)),
  });
  return normalized;
}

export interface EvaluationVersion {
  id: string;
  workflow: CompiledWorkflow;
}

export interface EvaluationExecution {
  runId?: string;
  state: string;
  output?: unknown;
  error?: string;
  usage?: UsageSummary;
}

export interface EvaluationScorer<I, O> {
  /** Include the scorer version in this identity whenever its implementation changes. */
  id: string;
  score(context: { input: I; expected: O | undefined; output: O }): number | Promise<number>;
}

export interface EvaluationOptions<I, O> {
  dataset: EvaluationDataset<I, O>;
  versions: readonly EvaluationVersion[];
  concurrency?: number;
  scorers?: readonly EvaluationScorer<I, O>[];
  /** JSON configuration pinned into the manifest and supplied to every runner call. */
  configuration?: Omit<TestWorkflowOptions, 'input' | 'dryRun'>;
  /** Custom runners must provide a versioned identity for reproducibility. */
  runner?: {
    id: string;
    run(workflow: CompiledWorkflow, options: Omit<TestWorkflowOptions, 'dryRun'>): Promise<EvaluationExecution>;
  };
}

export interface EvaluationManifest {
  id: string;
  datasetId: string;
  datasetDigest: string;
  versions: { id: string; planDigest: string; plan: Plan }[];
  configuration: Omit<TestWorkflowOptions, 'input' | 'dryRun'>;
  configurationDigest: string;
  concurrency: number;
  scorerIds: string[];
  runnerId: string;
}

export interface EvaluationResult {
  caseId: string;
  versionId: string;
  inputDigest: string;
  durationMs: number;
  execution: Omit<EvaluationExecution, 'usage'> & { success: boolean; usage: UsageSummary | null };
  /** A scorer failure never changes execution success. Failed executions are not scored. */
  scores: Record<string, { value: number | null; error?: string }>;
  outputError?: string;
}

/** Run every version against every case with a global bound on in-flight runs. */
export async function evaluateWorkflow<I, O>(options: EvaluationOptions<I, O>): Promise<{
  manifest: EvaluationManifest;
  results: EvaluationResult[];
}> {
  const concurrency = options.concurrency ?? 1;
  if (!Number.isSafeInteger(concurrency) || concurrency < 1) throw new Error('concurrency must be a positive safe integer');
  uniqueIds(options.versions, 'version');
  const scorers = [...(options.scorers ?? [])].map(s => ({ id: s.id, score: s.score.bind(s) }));
  uniqueIds(scorers, 'scorer');
  const runnerId = options.runner?.id ?? '@muxlio/testing/testWorkflow/v1';
  if (!runnerId.trim()) throw new Error('runner identity must be nonempty');
  const run = options.runner ? options.runner.run.bind(options.runner) : testWorkflow;
  const dataset = defineDataset(options.dataset);
  const parseOutput = dataset.output.parse.bind(dataset.output);
  // Pin JSON values before the first await; never trust caller-supplied plan digests.
  const cases = snapshot(dataset.cases);
  const configuration = pinConfiguration(options.configuration);
  const versions = options.versions.map(v => {
    const plan = snapshot(v.workflow.plan);
    plan.digest = computeDigest(plan);
    return { id: v.id, planDigest: plan.digest, plan };
  });
  const identity = {
    datasetId: dataset.id,
    datasetDigest: digest(cases),
    versions,
    configuration,
    configurationDigest: digest(configuration),
    concurrency,
    scorerIds: scorers.map(s => s.id),
    runnerId,
  };
  const manifest: EvaluationManifest = { id: digest(identity), ...identity };
  const jobs = versions.flatMap(version => cases.map(c => ({ version, c })));
  const results: EvaluationResult[] = new Array(jobs.length);
  let next = 0;
  async function worker() {
    while (next < jobs.length) {
      const index = next++;
      const { version, c } = jobs[index]!;
      const start = performance.now();
      let execution: EvaluationExecution;
      try {
        execution = structuredClone(coerceExecution(
          await run({ plan: snapshot(version.plan) }, { ...snapshot(configuration), input: snapshot(c.input) }),
        ));
      } catch (error) {
        execution = { state: 'error', error: message(error) };
      }
      const result: EvaluationResult = {
        caseId: c.id,
        versionId: version.id,
        inputDigest: digest(c.input),
        durationMs: performance.now() - start,
        execution: { ...execution, success: execution.state === 'succeeded', usage: execution.usage ?? null },
        scores: Object.create(null),
      };
      if (result.execution.success) {
        let output: O;
        try {
          output = parseOutput(execution.output === undefined ? undefined : snapshot(execution.output));
        } catch (error) {
          result.outputError = message(error);
          results[index] = result;
          continue;
        }
        for (const scorer of scorers) {
          try {
            const value = await scorer.score({ input: snapshot(c.input), expected: c.expected === undefined ? undefined : snapshot(c.expected), output: snapshot(output) });
            if (!Number.isFinite(value)) throw new Error('score must be finite');
            result.scores[scorer.id] = { value };
          } catch (error) {
            result.scores[scorer.id] = { value: null, error: message(error) };
          }
        }
      }
      results[index] = result;
    }
  }
  await Promise.all(Array.from({ length: Math.min(concurrency, jobs.length) }, worker));
  return { manifest, results };
}

function pinConfiguration(
  configuration: EvaluationOptions<unknown, unknown>['configuration'],
): Omit<TestWorkflowOptions, 'input' | 'dryRun'> {
  const { dryRun: _ignored, ...rest } = (configuration ?? {}) as TestWorkflowOptions;
  return snapshot(rest);
}

function coerceExecution(raw: unknown): EvaluationExecution {
  if (!raw || typeof raw !== 'object' || typeof (raw as EvaluationExecution).state !== 'string') {
    return { state: 'error', error: 'runner returned invalid execution' };
  }
  const value = raw as EvaluationExecution & Record<string, unknown>;
  return {
    ...(typeof value.runId === 'string' ? { runId: value.runId } : {}),
    state: value.state,
    ...(value.output !== undefined ? { output: value.output } : {}),
    ...(typeof value.error === 'string' ? { error: value.error } : {}),
    ...(value.usage ? { usage: value.usage } : {}),
  };
}

function uniqueIds(items: readonly { id: string }[], kind: string) {
  const ids = new Set<string>();
  for (const { id } of items) {
    if (!id.trim() || ids.has(id)) throw new Error(`${kind} IDs must be nonempty and unique: ${id}`);
    ids.add(id);
  }
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function snapshot<T>(value: T): T {
  const json = JSON.stringify(value, function (key, item: unknown) {
    if ((typeof item === 'number' && !Number.isFinite(item)) ||
        typeof item === 'function' || typeof item === 'symbol' || typeof item === 'bigint' ||
        (item === undefined && Array.isArray(this))) {
      throw new TypeError('evaluation values must be JSON serializable without loss');
    }
    return item;
  });
  if (json === undefined) throw new TypeError('evaluation values must be JSON serializable without loss');
  return JSON.parse(json) as T;
}

function digest(value: unknown): string {
  const canonical = (v: unknown): string => {
    if (Array.isArray(v)) return `[${v.map(canonical).join(',')}]`;
    if (v !== null && typeof v === 'object') {
      const object = v as Record<string, unknown>;
      return `{${Object.keys(object).sort().map(k => `${JSON.stringify(k)}:${canonical(object[k])}`).join(',')}}`;
    }
    return JSON.stringify(v);
  };
  return 'sha256:' + createHash('sha256').update(canonical(value)).digest('hex');
}
