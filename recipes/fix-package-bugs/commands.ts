// Embedded in the plan: execution never depends on importing a recipe file
// from the target workspace. All variable values travel as argv, never code.
export const verifyScript = String.raw`
const {spawnSync} = require('node:child_process');
const {writeFileSync} = require('node:fs');
const {join} = require('node:path');
const [kind, path, target] = process.argv.slice(1);
const checks = kind === 'go'
  ? [['package tests', 'go', ['test', target], path], ['package vet', 'go', ['vet', target], path]]
  : kind === 'npm'
    ? [['package build', 'npm', ['run', 'build', '--workspace', target], '.'], ['package tests', 'npm', ['test', '--workspace', target], '.']]
    : kind === 'ui'
      ? [['UI tests', 'npm', ['--prefix', path, 'test'], '.']]
      : [];
if (!checks.length) throw new Error('Unsupported package kind: ' + kind);
checks.push(
  ['repository build', 'npm', ['run', 'build'], '.'],
  ['repository tests', 'npm', ['test'], '.'],
  ['UI regression tests', 'npm', ['--prefix', 'muxlio-ui', 'test'], '.'],
  ['core regression tests', 'go', ['test', './...'], 'muxlio-core'],
  ['adapter regression tests', 'go', ['test', './...'], 'muxlio-adapters'],
);
let failed = false;
const results = [];
for (const [label, program, args, cwd] of checks) {
  const start = Date.now();
  console.log('\n=== ' + label + ' ===');
  const result = spawnSync(program, args, {cwd, stdio: 'inherit', timeout: 600000});
  const exitCode = result.status;
  const error = result.error?.message;
  const passed = !error && !result.signal && exitCode === 0;
  results.push({label, program, args, cwd, passed, exitCode, signal: result.signal, error, durationMs: Date.now() - start});
  console.log('\n=== ' + label + ': ' + (passed ? 'passed' : 'FAILED') + ', exit ' + exitCode + (error ? ', ' + error : '') + ' ===');
  if (!passed) failed = true;
}
writeFileSync(join(process.env.MUXLIO_OUTPUT_DIR, 'checks.json'), JSON.stringify({passed: !failed, checks: results}, null, 2));
process.exitCode = failed ? 1 : 0;
`;

export const commitScript = String.raw`
const {spawnSync} = require('node:child_process');
function git(args) {
  const result = spawnSync('git', args, {encoding: 'utf8'});
  if (result.error || result.signal || result.status !== 0) {
    process.stderr.write(result.stderr || result.error?.message || 'git failed');
    process.exit(1);
  }
  return result.stdout;
}
if (!git(['status', '--porcelain']).trim()) {
  console.log('No changes: package audited, reviewed and verified.');
} else {
  git(['add', '-A']);
  process.stdout.write(git(['commit', '-m', 'fix bugs in ' + process.argv[1]]));
  if (git(['status', '--porcelain']).trim()) {
    console.error('Workspace is dirty after commit; stop before the next package.');
    process.exit(1);
  }
  process.stdout.write(git(['rev-parse', 'HEAD']));
}
`;
