import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';

test('discovery enumerates both Go modules, npm workspaces and UI deterministically', t => {
  const root = mkdtempSync(join(tmpdir(), 'muxlio-discover-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  for (const path of ['bin', 'muxlio-core', 'muxlio-adapters', 'sdk', 'compiler']) mkdirSync(join(root, path));
  writeFileSync(join(root, 'package.json'), JSON.stringify({ workspaces: ['sdk', 'compiler'] }));
  for (const path of ['sdk', 'compiler']) writeFileSync(join(root, path, 'package.json'), JSON.stringify({ name: '@fixture/' + path }));
  writeFileSync(join(root, 'bin', 'go'), `#!${process.execPath}
if (process.env.FAIL_DISCOVERY) { console.error('discovery failed'); process.exit(1); }
console.log(process.cwd() + '/internal/z');
console.log(process.cwd());
console.log(process.cwd() + '/internal/a');
`, { mode: 0o755 });
  const script = fileURLToPath(new URL('./discover.mjs', import.meta.url));
  const run = (extra = {}) => spawnSync(process.execPath, [script], {
    cwd: root, encoding: 'utf8', env: { ...process.env, PATH: join(root, 'bin'), ...extra },
  });
  const result = run();
  assert.equal(result.status, 0, result.stderr);
  const input = JSON.parse(result.stdout);
  assert.equal(input.packages.length, 9);
  assert.deepEqual(input.packages.slice(0, 3).map(pkg => pkg.target), ['.', './internal/a', './internal/z']);
  assert.equal(input.packages[3].path, 'muxlio-adapters');
  assert.equal(input.packages[6].target, '@fixture/sdk');
  assert.equal(input.packages[7].target, '@fixture/compiler');
  assert.equal(input.packages[8].kind, 'ui');
  assert.equal(new Set(input.packages.map(pkg => pkg.id)).size, input.packages.length);
  assert.equal(run().stdout, result.stdout);
  const out = join(root, 'input.json');
  const written = spawnSync(process.execPath, [script, '--out', out], {
    cwd: root, encoding: 'utf8', env: { ...process.env, PATH: join(root, 'bin') },
  });
  assert.equal(written.status, 0, written.stderr);
  assert.equal(readFileSync(out, 'utf8'), result.stdout);
  const failure = run({ FAIL_DISCOVERY: '1' });
  assert.notEqual(failure.status, 0);
  assert.match(failure.stderr, /Cannot discover muxlio-core/);
  assert.equal(failure.stdout, '');
});
