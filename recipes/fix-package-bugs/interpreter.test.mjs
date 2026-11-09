import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import compiled from './workflow.ts';

const cli = process.env.MUXLIO_TEST_CLI;
const options = { skip: cli ? false : 'Set MUXLIO_TEST_CLI to a current muxlio binary' };

function run(t, passed, packageCount = 2) {
  const root = mkdtempSync(join(tmpdir(), 'muxlio-bugs-interpreter-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const commandOutputs = { passed: true, exitCode: 0, signal: null, stdout: 'command result', stderr: '', durationMs: 1 };
  const files = {
    plan: compiled.plan,
    input: { packages: Array.from({ length: packageCount }, (_, index) => ({
      id: 'package-' + index, path: 'muxlio-sdk', kind: 'npm', target: '@muxlio/sdk',
    })) },
    fake: {
      byExecutor: {
        'agent/kiro': { outputs: { findings: 'findings artifact', notes: 'fix artifact' } },
        'agent/antigravity': { outputs: { report: 'review artifact' } },
        command: { outputs: commandOutputs },
      },
      byNode: { verify: { outputs: { ...commandOutputs, passed, exitCode: passed ? 0 : 1, report: 'check artifact' } } },
    },
  };
  for (const [name, value] of Object.entries(files)) writeFileSync(join(root, name + '.json'), JSON.stringify(value));
  const result = spawnSync(cli, ['run', join(root, 'plan.json'), join(root, 'input.json'),
    '--fake-config', join(root, 'fake.json'), '--data-dir', join(root, 'state'), '--json'], {
    encoding: 'utf8', timeout: 30000, maxBuffer: 8 * 1024 * 1024,
  });
  assert.ok(result.stdout, result.stderr || result.error?.message);
  const run = JSON.parse(result.stdout);
  const queued = run.events.filter(event => event.type === 'activation.queued');
  return { ...run, queued, status: result.status };
}

test('real interpreter finishes and commits each package before starting the next', options, t => {
  const result = run(t, true);
  assert.equal(result.state, 'succeeded', result.error);
  assert.equal(result.status, 0);
  assert.equal(result.output.length, 2);
  assert.ok(result.output.every(pkg => pkg.attempts === 1 && pkg.verification === 'check artifact' && pkg.review === 'review artifact'));
  const commits = result.queued.filter(event => event.payload.nodeId === 'commit');
  assert.equal(commits.length, 2);
  const nextClean = result.queued.filter(event => event.payload.nodeId === 'require-clean-workspace')[1];
  const firstCommitDone = result.events.find(event => event.type === 'activation.succeeded' && event.subjectId === commits[0].subjectId);
  assert.ok(firstCommitDone.seq < nextClean.seq);
  assert.equal(result.queued.some(event => event.payload.nodeId === 'fix'), false);
});

test('real interpreter pauses on failed verification and never commits or advances', options, t => {
  const result = run(t, false);
  assert.equal(result.state, 'paused', result.error);
  const count = id => result.queued.filter(event => event.payload.nodeId === id).length;
  assert.equal(count('review'), 1);
  assert.equal(count('verify'), 10);
  assert.equal(count('fix'), 9);
  assert.equal(count('commit'), 0);
  assert.equal(count('require-clean-workspace'), 1);
});

test('default admission limits allow a complete 42-package sweep', options, t => {
  const result = run(t, true, 42);
  assert.equal(result.state, 'succeeded', result.error);
  assert.equal(result.output.length, 42);
  assert.equal(result.queued.filter(event => event.payload.nodeId === 'commit').length, 42);
});
