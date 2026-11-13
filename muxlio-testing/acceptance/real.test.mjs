import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtemp, mkdir, readFile, writeFile, rm, realpath } from 'node:fs/promises';
import { tmpdir, release } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'node:net';
import { once } from 'node:events';
import { test } from 'node:test';
import { realClientConfig } from './real-client-config.mjs';

// No downloaded "latest" CLI, shell interpolation, implicit credentials, or
// fabricated passes. An operator supplies exact installed versions and argv.
const probes = realClientConfig(process.env, process.platform);
const prefix = resolve(process.env.MUXLIO_ACCEPTANCE_PREFIX);
async function freePort() {
  const s = createServer().listen(0, '127.0.0.1');
  await once(s, 'listening');
  const p = s.address().port;
  await new Promise(r => s.close(r));
  return p;
}
for (const probe of probes) test(`real macOS ${probe.client}@${probe.version}`, { timeout: 600000 }, async t => {
  const root = await realpath(await mkdtemp(join(tmpdir(), 'muxlio-real-')));
  let stop = () => {};
  t.after(async () => { try { stop(); } finally { await rm(root, { recursive: true, force: true }); } });
  const workspace = join(root, 'workspace');
  const runtime = join(root, 'runtime');
  await mkdir(workspace);
  const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith('MUXLIO_')));
  Object.assign(env, { MUXLIO_CONFIG: join(runtime, 'config.json'), MUXLIO_ACCEPTANCE_MCP: join(prefix, 'bin/muxlio-mcp'), MUXLIO_ACCEPTANCE_AUDIT: join(root, 'audit.jsonl') });
  let version;
  try { version = execFileSync(probe.binary, probe.versionArgv, { env, encoding: 'utf8', timeout: 10000, stdio: ['ignore', 'pipe', 'pipe'] }).trim(); }
  catch { assert.fail('client-configuration: could not read the pinned real CLI version'); }
  assert.equal(version, probe.version, 'client-configuration: installed CLI version differs from pin');
  const cli = (...args) => {
    try { return execFileSync(join(prefix, 'bin/muxlio'), args, { cwd: workspace, env, timeout: 45000, stdio: ['ignore', 'pipe', 'pipe'], encoding: 'utf8' }); }
    catch { assert.fail(`${args[0] === 'init' ? 'client-configuration' : 'coordinator/worker-adapter'}: installed muxlio ${args[0]} failed (output withheld)`); }
  };
  t.diagnostic(`client=${probe.client}; exactVersion=${version}; os=${process.platform}/${release()}/${process.arch}; node=${process.version}; product=${cli('version').trim()}`);
  cli('init', '--client', probe.client, '--scope', 'project', '--data-dir', runtime, '--workspace', workspace, '--mcp-binary', fileURLToPath(new URL('./audit-mcp.mjs', import.meta.url)));
  const cfg = JSON.parse(await readFile(env.MUXLIO_CONFIG, 'utf8'));
  const port = await freePort();
  Object.assign(cfg, { addr: `127.0.0.1:${port}`, mcp: { listen: `127.0.0.1:${await freePort()}` }, capabilities: ['command'], workerCapacity: 0, workspaces: { project: { workerId: 'local', path: workspace } } });
  cfg.servers.local.url = `http://127.0.0.1:${port}`;
  cfg.executors.command = { allow: true };
  await writeFile(env.MUXLIO_CONFIG, JSON.stringify(cfg));
  let running = false;
  const start = () => { cli('service', 'start', '--config', env.MUXLIO_CONFIG); running = true; };
  stop = () => { if (running) { cli('service', 'stop', '--config', env.MUXLIO_CONFIG); running = false; } };
  const invoke = prompt => {
    try { execFileSync(probe.binary, probe.argv.map(a => a === '{prompt}' ? prompt : a), { cwd: workspace, env, timeout: 180000, stdio: 'ignore' }); return true; }
    catch { return false; }
  };
  const evidence = async names => {
    let events;
    try {
      events = (await readFile(env.MUXLIO_ACCEPTANCE_AUDIT, 'utf8')).split('\n').filter(Boolean).map(JSON.parse);
    } catch {
      assert.fail('client-configuration: no readable MCP evidence; check client discovery and MCP approval');
    }
    for (const name of names) assert.ok(events.filter(e => e.name === name && (name !== 'start_run' || e.expected === true) && (name !== 'get_run' || (e.succeeded === true && e.expected === true))).length >= (name === 'start_run' ? 2 : 1), `client-configuration: missing successful expected ${name} MCP call(s)`);
  };
  const plan = { irVersion: '1.0', name: 'real-client acceptance', root: 'root', subgraphs: { root: { nodes: [{ id: 'work', kind: 'execute', executor: 'command', params: { program: '/bin/sh', args: ['-c', 'printf real-client-result'] }, outputs: { stdout: {} } }], output: { result: { ref: { node: 'work', port: 'stdout' } } } } } };
  const submission = { plan, input: {}, workspaceId: 'project', idempotencyKey: 'real-client-invocation' };
  env.MUXLIO_ACCEPTANCE_SUBMISSION = JSON.stringify(submission);
  start();
  assert.ok(invoke(`Activate the installed Muxlio skill. Use only muxlio MCP tools. Call list_executors. Call start_run twice with exactly these same arguments: ${JSON.stringify(submission)}. The worker is intentionally drained. Report the run ID, then exit without waiting. Do not use shell or HTTP tools.`), 'client-configuration: real CLI submission failed; check login, project trust and MCP approval (transcript withheld)');
  await evidence(['list_executors', 'start_run']);
  const queued = JSON.parse(cli('status', '--server', 'local'));
  const runs = queued.runs ?? queued;
  assert.equal(runs.length, 1, 'submission: expected one durable run');
  assert.equal(runs[0].state, 'running', 'submission: expected queued run before worker restart');
  const id = runs[0].id ?? runs[0].runId;
  assert.ok(id);
  env.MUXLIO_ACCEPTANCE_RUN_ID = id;
  stop();
  cfg.workerCapacity = 1;
  await writeFile(env.MUXLIO_CONFIG, JSON.stringify(cfg));
  start();
  // Empty the audit so prior process evidence cannot satisfy reconnect.
  await writeFile(env.MUXLIO_ACCEPTANCE_AUDIT, '');
  const reconnected = invoke(`Activate the Muxlio skill. Use only muxlio MCP tools. Call list_runs to rediscover the workflow real-client acceptance, then get_run for ${id} until it succeeds. Report its output. Do not use shell or HTTP tools.`);
  const result = JSON.parse(cli('status', '--server', 'local', id));
  assert.ok(!['failed', 'cancelled'].includes(result.state), 'worker-adapter: run failed or was cancelled');
  assert.ok(reconnected, 'client-configuration: real CLI reconnect failed; check login, project trust and MCP approval (transcript withheld)');
  await evidence(['list_runs']);
  assert.equal(result.state, 'succeeded', 'worker-adapter: run failed or did not finish');
  assert.equal(result.output.result, 'real-client-result');
  await evidence(['get_run']);
  t.diagnostic(`PASS submit/disconnect/restart/rediscover/result client=${probe.client} version=${version}`);
});
