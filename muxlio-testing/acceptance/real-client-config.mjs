import assert from 'node:assert/strict';
import { isAbsolute } from 'node:path';
import { profiles } from './client-profiles.mjs';

export function realClientConfig(env, platform) {
  assert.equal(platform, 'darwin', 'client-configuration: real-client release acceptance requires macOS');
  assert.ok(env.MUXLIO_ACCEPTANCE_PREFIX && isAbsolute(env.MUXLIO_ACCEPTANCE_PREFIX), 'client-configuration: set an absolute MUXLIO_ACCEPTANCE_PREFIX');
  let probes;
  try { probes = JSON.parse(env.MUXLIO_REAL_CLIENTS ?? '[]'); }
  catch { assert.fail('client-configuration: MUXLIO_REAL_CLIENTS must be a JSON array'); }
  assert.ok(Array.isArray(probes), 'client-configuration: MUXLIO_REAL_CLIENTS must be a JSON array');
  const supported = new Set(profiles.map(p => p.name));
  const strings = args => Array.isArray(args) && args.length > 0 && args.every(a => typeof a === 'string');
  for (const p of probes) {
    assert.ok(p && supported.has(p.client), 'client-configuration: select a verified MCP setup client; Antigravity setup is unavailable');
    assert.ok(typeof p.binary === 'string' && isAbsolute(p.binary), 'client-configuration: client binary must be an absolute path');
    assert.ok(typeof p.version === 'string' && p.version.trim() === p.version && p.version.length > 0, 'client-configuration: pin the exact complete trimmed CLI version output');
    assert.ok(strings(p.argv) && p.argv.filter(a => a === '{prompt}').length === 1, 'client-configuration: argv must contain exactly one whole {prompt} argument');
    assert.ok(strings(p.versionArgv), 'client-configuration: supply versionArgv');
  }
  assert.ok(new Set(probes.map(p => p.client)).size >= 2, 'client-configuration: configure at least two independent real clients');
  return probes;
}
