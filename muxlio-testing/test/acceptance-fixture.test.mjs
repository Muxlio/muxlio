import assert from 'node:assert/strict';
import { test } from 'node:test';
import { MCP } from '../acceptance/mcp-fixture.mjs';
import { profiles, selectProfiles } from '../acceptance/client-profiles.mjs';
import { realClientConfig } from '../acceptance/real-client-config.mjs';

const probe = client => ({ client, binary: `/clients/${client}`, version: 'pinned 1.0.0', versionArgv: ['--version'], argv: ['run', '{prompt}'] });

test('real-client gate requires macOS, an installed prefix and two independent pinned clients', () => {
  const env = { MUXLIO_ACCEPTANCE_PREFIX: '/installed', MUXLIO_REAL_CLIENTS: JSON.stringify([probe('claude'), probe('codex')]) };
  assert.equal(realClientConfig(env, 'darwin').length, 2);
  assert.throws(() => realClientConfig(env, 'linux'), /requires macOS/);
  assert.throws(() => realClientConfig({ ...env, MUXLIO_ACCEPTANCE_PREFIX: '' }, 'darwin'), /absolute MUXLIO_ACCEPTANCE_PREFIX/);
  for (const value of ['bad-json', '{}', 'null']) {
    assert.throws(() => realClientConfig({ ...env, MUXLIO_REAL_CLIENTS: value }, 'darwin'), /must be a JSON array/);
  }
  for (const entries of [[], [probe('codex')], [probe('codex'), probe('codex')]]) {
    assert.throws(() => realClientConfig({ ...env, MUXLIO_REAL_CLIENTS: JSON.stringify(entries) }, 'darwin'), /two independent real clients/);
  }
  for (const invalid of [null, probe('antigravity'), { ...probe('codex'), version: '' }, { ...probe('codex'), binary: 'codex' },
    { ...probe('codex'), argv: ['prefix {prompt}'] }, { ...probe('codex'), argv: ['{prompt}', '{prompt}'] }, { ...probe('codex'), versionArgv: [] }]) {
    assert.throws(() => realClientConfig({ ...env, MUXLIO_REAL_CLIENTS: JSON.stringify([probe('claude'), invalid]) }, 'darwin'), /client-configuration:/);
  }
});

test('acceptance profile selection rejects typos instead of silently skipping clients', () => {
  assert.deepEqual(new Set(profiles.map(p => p.name)), new Set(['claude', 'codex', 'cursor', 'kiro']));
  assert.equal(new Set(profiles.map(p => p.id)).size, profiles.length);
  for (const p of profiles) assert.deepEqual(selectProfiles(p.id), [p]);
  assert.deepEqual(selectProfiles('antigravity-unavailable'), []);
  assert.equal(selectProfiles().length, profiles.length);
  assert.throws(() => selectProfiles('codex-typo'), /client-configuration: unknown acceptance profile/);
});

test('fixture attributes a missing installed executable to client configuration', { timeout: 3000 }, async () => {
  const mcp = new MCP({ command: '/nonexistent/muxlio-acceptance', args: [] }, process.env);
  try {
    await assert.rejects(mcp.connect(), /client-configuration: registered MCP executable could not launch/);
    await assert.rejects(mcp.request('ping', {}), /client-configuration:/);
  } finally { await mcp.close(); }
});

test('fixture rejects pending requests promptly when the installed server exits', { timeout: 3000 }, async () => {
  const mcp = new MCP({ command: process.execPath, args: ['-e', 'process.exit(7)'] }, process.env);
  try {
    await assert.rejects(mcp.connect(), /client-configuration: MCP exited \(code=7/);
  } finally { await mcp.close(); }
});

test('fixture correlates concurrent responses and reports worker tool failures separately', { timeout: 3000 }, async () => {
  const code = `
    const readline = require('node:readline');
    readline.createInterface({ input: process.stdin }).on('line', line => {
      const r = JSON.parse(line);
      setTimeout(() => console.log(JSON.stringify({ jsonrpc: '2.0', id: r.id,
        result: { isError: r.params.name === 'failed', content: [{ text: JSON.stringify(r.params.name) }] }
      })), r.params.name === 'first' ? 50 : 0);
    });
  `;
  const mcp = new MCP({ command: process.execPath, args: ['-e', code] }, process.env);
  try {
    assert.deepEqual(await Promise.all([mcp.value('first', {}), mcp.value('second', {})]), ['first', 'second']);
    await assert.rejects(mcp.value('failed', {}), /coordinator\/worker-adapter: failed/);
  } finally { await mcp.close(); }
});
