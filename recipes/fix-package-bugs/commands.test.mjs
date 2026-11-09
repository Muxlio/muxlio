import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { verifyScript, commitScript } from './commands.ts';
import { verify as verifyTask } from './tasks.ts';

function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'muxlio-package-bugs-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  return root;
}

function verify(t, { fail = '', kind = 'go' } = {}) {
  const root = fixture(t);
  mkdirSync(join(root, 'bin'));
  for (const path of ['muxlio-core', 'muxlio-adapters', 'muxlio-sdk']) mkdirSync(join(root, path));
  const stub = `#!${process.execPath}\nif (process.argv.slice(2).join(' ') === process.env.FAIL_ARGS) process.exit(7);\n`;
  for (const program of ['npm', 'go']) writeFileSync(join(root, 'bin', program), stub, { mode: 0o755 });
  const path = kind === 'npm' ? 'muxlio-sdk' : 'muxlio-core';
  const result = spawnSync(process.execPath, ['-e', verifyScript, kind, path, kind === 'npm' ? '@muxlio/sdk' : './internal/plan'], {
    cwd: root, encoding: 'utf8',
    env: { ...process.env, PATH: join(root, 'bin'), FAIL_ARGS: fail, MUXLIO_OUTPUT_DIR: root },
  });
  const report = JSON.parse(readFileSync(join(root, 'checks.json'), 'utf8'));
  return { result, report };
}

test('successful checks record exact commands and statuses', t => {
  const { result, report } = verify(t);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(report.passed, true);
  assert.equal(report.checks.length, 7);
  assert.deepEqual(report.checks[0].args, ['test', './internal/plan']);
  assert.deepEqual(report.checks[1].args, ['vet', './internal/plan']);
  assert.ok(report.checks.every(check => check.exitCode === 0 && check.passed));
});

test('failed checks fail the gate and later suites still run', t => {
  const { result, report } = verify(t, { fail: 'vet ./internal/plan' });
  assert.equal(result.status, 1);
  assert.equal(report.checks[1].exitCode, 7);
  assert.equal(report.checks[1].passed, false);
  assert.equal(report.checks.at(-1).label, 'adapter regression tests');
  assert.equal(report.checks.at(-1).passed, true);
});

test('npm workspaces use their declared workspace target', t => {
  const { result, report } = verify(t, { kind: 'npm' });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(report.checks[0].args, ['run', 'build', '--workspace', '@muxlio/sdk']);
  assert.deepEqual(report.checks[1].args, ['test', '--workspace', '@muxlio/sdk']);
});

test('verification task passes real command adapter validation and executes checks', {
  skip: process.env.MUXLIO_COMMAND_ADAPTER ? false : 'Set MUXLIO_COMMAND_ADAPTER to a built command adapter',
}, t => {
  const root = fixture(t);
  for (const path of ['bin', 'scratch', 'muxlio-core', 'muxlio-adapters']) mkdirSync(join(root, path));
  symlinkSync(process.execPath, join(root, 'bin', 'node'));
  for (const program of ['npm', 'go']) writeFileSync(join(root, 'bin', program), `#!${process.execPath}\nprocess.exit(0);\n`, { mode: 0o755 });
  const request = {
    executor: 'command', hostExecutionAllowed: true, workspace: root, workDir: join(root, 'scratch'),
    params: verifyTask.def.params,
    inputs: { package: { kind: 'go', path: 'muxlio-core', target: './internal/workers' } },
    outputs: { report: { mime: 'application/json', path: 'checks.json' } },
  };
  const result = spawnSync(process.env.MUXLIO_COMMAND_ADAPTER, [], {
    input: JSON.stringify(request), encoding: 'utf8',
    env: { ...process.env, PATH: join(root, 'bin') },
  });
  assert.equal(result.status, 0, result.stderr || result.stdout);
  const response = JSON.parse(result.stdout);
  assert.ok(!response.error, result.stdout);
  assert.equal(response.outputs.passed, true);
  const report = JSON.parse(readFileSync(join(root, 'scratch', 'checks.json'), 'utf8'));
  assert.equal(report.passed, true);
  assert.equal(report.checks.length, 7);
});

function git(root, args) {
  const result = spawnSync('git', args, { cwd: root, encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}

test('commit handles a clean package without an empty commit and commits fixes once', t => {
  const root = fixture(t);
  git(root, ['init']);
  git(root, ['config', 'user.name', 'Recipe Test']);
  git(root, ['config', 'user.email', 'recipe@example.invalid']);
  git(root, ['config', 'commit.gpgsign', 'false']);
  writeFileSync(join(root, 'source.txt'), 'before\n');
  git(root, ['add', '.']);
  git(root, ['commit', '-m', 'initial']);
  const initial = git(root, ['rev-parse', 'HEAD']);
  const run = () => spawnSync(process.execPath, ['-e', commitScript, 'example-package'], { cwd: root, encoding: 'utf8' });
  const clean = run();
  assert.equal(clean.status, 0, clean.stderr);
  assert.match(clean.stdout, /No changes/);
  assert.equal(git(root, ['rev-parse', 'HEAD']), initial);
  writeFileSync(join(root, 'source.txt'), 'after\n');
  writeFileSync(join(root, 'regression.txt'), 'new test\n');
  const fixed = run();
  assert.equal(fixed.status, 0, fixed.stderr);
  assert.notEqual(git(root, ['rev-parse', 'HEAD']), initial);
  assert.equal(git(root, ['log', '-1', '--format=%s']), 'fix bugs in example-package');
  assert.equal(git(root, ['status', '--porcelain']), '');
  assert.equal(git(root, ['rev-list', '--count', 'HEAD']), '2');
  assert.equal(run().status, 0);
  assert.equal(git(root, ['rev-list', '--count', 'HEAD']), '2');
});
