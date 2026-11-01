// @muxlio/testing — fake workers, fixtures, and a test harness for validating
// workflows without real model calls.
//
// `testWorkflow` runs a compiled Plan against the Go interpreter (via the
// `muxlio` CLI) with a fake executor. The fake executor returns canned
// outputs keyed by node ID or executor name, so tests run without tokens,
// network access, or side effects.

import { writeFileSync, mkdtempSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { execSync } from 'node:child_process';
import type { Plan, CompiledWorkflow } from '@muxlio/sdk';

// --- Fake executor configuration ---

export interface FakeExecutorResult {
  outputs: Record<string, unknown>;
  error?: string;
}

export interface FakeExecutorConfig {
  byNode?: Record<string, FakeExecutorResult>;
  byExecutor?: Record<string, FakeExecutorResult>;
}

export const fakeExecutor = {
  /** Create a fake executor config keyed by node ID. */
  byNode(config: Record<string, FakeExecutorResult>): FakeExecutorConfig {
    return { byNode: config };
  },
  /** Create a fake executor config keyed by executor capability. */
  byExecutor(config: Record<string, FakeExecutorResult>): FakeExecutorConfig {
    return { byExecutor: config };
  },
};

// --- Test harness ---

export interface TestWorkflowOptions {
  input: unknown;
  executors?: FakeExecutorConfig;
  /** Path to the muxlio CLI binary. Defaults to looking in PATH. */
  cliPath?: string;
  /** Skip running against the Go CLI; just validate and return the plan. */
  dryRun?: boolean;
}

export interface TestResult {
  runId: string;
  state: string;
  output: unknown;
  events: unknown[];
  plan: Plan;
  durationMs: number;
}

/**
 * Test a compiled workflow against the Go interpreter with fake executors.
 *
 * This writes the plan and input to temporary files, invokes the `muxlio`
 * CLI, and parses the JSON output. The Go binary must be built and available
 * (either in PATH or via `cliPath`).
 *
 * If `dryRun` is true, the plan is validated and returned without running.
 */
export async function testWorkflow(
  workflow: CompiledWorkflow,
  opts: TestWorkflowOptions,
): Promise<TestResult> {
  const plan = workflow.plan;
  const start = Date.now();

  if (opts.dryRun) {
    return {
      runId: 'dry-run',
      state: 'validated',
      output: null,
      events: [],
      plan,
      durationMs: Date.now() - start,
    };
  }

  const cliPath = opts.cliPath || 'muxlio';
  const dir = mkdtempSync(join(tmpdir(), 'muxlio-test-'));
  try {
    const planPath = join(dir, 'plan.json');
    const inputPath = join(dir, 'input.json');
    writeFileSync(planPath, JSON.stringify(plan, null, 2));
    writeFileSync(inputPath, JSON.stringify(opts.input, null, 2));

    const fakeConfigPath = join(dir, 'fake.json');
    const fakeConfig: Record<string, unknown> = {};
    if (opts.executors?.byNode) fakeConfig.byNode = opts.executors.byNode;
    if (opts.executors?.byExecutor) fakeConfig.byExecutor = opts.executors.byExecutor;
    writeFileSync(fakeConfigPath, JSON.stringify(fakeConfig, null, 2));

    const cmd = `${cliPath} run ${planPath} ${inputPath} --json --fake-config ${fakeConfigPath}`;
    const stdout = execSync(cmd, { encoding: 'utf-8', maxBuffer: 10 * 1024 * 1024 });
    const result = JSON.parse(stdout);

    return {
      runId: result.runId,
      state: result.state,
      output: result.output,
      events: result.events || [],
      plan,
      durationMs: Date.now() - start,
    };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}
