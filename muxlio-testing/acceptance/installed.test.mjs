import assert from 'node:assert/strict';
import { spawn, execFileSync } from 'node:child_process';
import { mkdtemp, mkdir, readFile, writeFile, cp, rm, realpath } from 'node:fs/promises';
import { tmpdir, release } from 'node:os';
import { join, resolve } from 'node:path';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { test } from 'node:test';
import { profiles, selectProfiles } from './client-profiles.mjs';
import { MCP } from './mcp-fixture.mjs';

// These are configuration/protocol fixtures, not claims that these CLIs ran.
const profile = process.env.MUXLIO_ACCEPTANCE_PROFILE;
const clients = selectProfiles(profile);
const source = process.env.MUXLIO_ACCEPTANCE_PREFIX;
assert.ok(source, 'Build a release and set MUXLIO_ACCEPTANCE_PREFIX (acceptance is separate from unit CI)');
const delay = ms => new Promise(r => setTimeout(r, ms));
function activation(skill, client) {
  assert.match(skill, client.projectOnly ? /alwaysApply: false/ : /name: muxlio/, 'client-configuration: skill activation metadata');
  assert.match(skill, /start_run/, 'client-configuration: missing delegation instructions');
}
async function port() {
  const s = createServer();
  s.listen(0, '127.0.0.1');
  await once(s, 'listening');
  const p = s.address().port;
  await new Promise(r => s.close(r));
  return p;
}
async function eventually(fn, label, timeout = 30000) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    try { return await fn(); } catch (e) { last = e; }
    await delay(100);
  }
  throw new Error(`${label}: ${last?.message}`, { cause: last });
}

// Read the generated client configuration, rather than reconstructing the
// expected launch command. TOML fixture values are strings and string arrays.
function registration(raw, client) {
  if (client !== 'codex') return JSON.parse(raw).mcpServers.muxlio;
  const section = raw.match(/\[mcp_servers\.muxlio\]([^]*?)(?=\n\[|$)/)?.[1];
  assert.ok(section, 'client-configuration: missing Codex registration');
  const scalar = value => value.startsWith("'") ? value.slice(1, -1) : JSON.parse(value);
  const parse = value => value.startsWith('[')
    ? [...value.matchAll(/'[^']*'|"(?:\\.|[^"\\])*"/g)].map(m => scalar(m[0])) : scalar(value);
  const field = key => parse(section.match(new RegExp(`^${key} = (.+)$`, 'm'))?.[1] ?? 'null');
  const config = raw.match(/^MUXLIO_CONFIG = (.+)$/m)?.[1];
  return { command: field('command'), args: field('args'), env: { MUXLIO_CONFIG: scalar(config) } };
}

for (const client of clients) for (const scope of client.projectOnly ? ['project'] : ['project', 'user']) {
  test(`installed ${process.platform}/${client.name}@${client.version}/${scope}`, { timeout: 180000 }, async t => {
    const root = await realpath(await mkdtemp(join(tmpdir(), 'muxlio-acceptance-')));
    const cleanups = [];
    t.after(async () => {
      try { for (const cleanup of cleanups.reverse()) await cleanup(); }
      finally { await rm(root, { recursive: true, force: true }); }
    });
    const prefix = join(root, 'installed product');
    const workspace = join(root, 'workspace');
    const home = join(root, 'home');
    const runtime = join(root, 'runtime');
    const stub = join(root, 'fixture-bin');
    await Promise.all([cp(resolve(process.env.MUXLIO_ACCEPTANCE_UPGRADE_FROM ?? source), prefix, { recursive: true }), mkdir(workspace), mkdir(home), mkdir(stub)]);
    // Pin Cursor's skill capability discovery independently of ambient installs.
    await writeFile(join(stub, 'cursor'), `#!/bin/sh\nprintf '%s\\n' '${client.version}'\n`, { mode: 0o700 });
    const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('MUXLIO_') && !['HOME', 'CODEX_HOME'].includes(key)));
    Object.assign(env, { HOME: home, CODEX_HOME: '', PATH: `${stub}:${process.env.PATH}`, MUXLIO_CONFIG: join(runtime, 'config.json') });
    const binary = join(prefix, 'bin/muxlio');
    const cli = (...args) => execFileSync(binary, args, { env, cwd: workspace, timeout: 45000, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
    t.diagnostic(`fixture client=${client.name} version=${client.version}; product=${cli('version').trim()}; os=${process.platform}/${release()}/${process.arch}; node=${process.version}`);
    cli('init', '--client', client.name, '--scope', scope, '--data-dir', runtime, '--workspace', workspace);
    const advertised = JSON.parse(cli('integrations', 'list', '--scope', scope, '--json'));
    assert.deepEqual(new Set(advertised.map(c => c.client)), new Set(profiles.map(c => c.name)), 'client-configuration: supported clients and acceptance profiles differ');
    const baseDir = scope === 'project' ? workspace : home;
    const configPath = join(baseDir, client.name === 'claude' && scope === 'user' ? '.claude.json' : client.config);
    const installed = await readFile(configPath, 'utf8');
    const skill = await readFile(join(baseDir, client.skill), 'utf8');
    activation(skill, client);
    const entry = registration(installed, client.name);
    assert.equal(entry.command, join(prefix, 'bin/muxlio-mcp'));
    assert.equal(entry.env.MUXLIO_CONFIG, join(runtime, 'config.json'));

    const p = await port();
    const cfg = JSON.parse(await readFile(env.MUXLIO_CONFIG, 'utf8'));
    Object.assign(cfg, { addr: `127.0.0.1:${p}`, mcp: { listen: `127.0.0.1:${await port()}` }, capabilities: ['command', 'agent/a2a'], workerCapacity: 0,
      workspaces: { project: { workerId: 'local', path: workspace } } });
    cfg.servers.local.url = `http://127.0.0.1:${p}`;
    cfg.executors.command = { allow: true };
    await writeFile(env.MUXLIO_CONFIG, JSON.stringify(cfg));
    let running = false;
    const start = () => { cli('service', 'start', '--config', env.MUXLIO_CONFIG); running = true; };
    const stop = () => { if (running) { cli('service', 'stop', '--config', env.MUXLIO_CONFIG); running = false; } };
    cleanups.push(stop);
    start();

    let remoteSubmissions = 0;
    let remotePolls = 0;
    // One matrix cell additionally recovers a detached operation. The other
    // cells cover queued-worker recovery without waiting for the poll interval.
    const recoverRemote = client.id === 'codex-0.167.0' && scope === 'project';
    let remoteComplete = !recoverRemote;
    let remoteInput;
    const remote = createServer(async (req, res) => {
      res.setHeader('Content-Type', 'application/json');
      if (req.method === 'GET') res.end(JSON.stringify({ protocolVersion: '0.3.0', url: `http://127.0.0.1:${remote.address().port}/rpc`, defaultInputModes: ['text/plain'] }));
      else {
        let body = '';
        for await (const chunk of req) body += chunk;
        const rpc = JSON.parse(body);
        if (rpc.method === 'message/send') {
          remoteSubmissions++;
          remoteInput = rpc.params.message.parts[0].text;
        } else if (rpc.method === 'tasks/get' && rpc.params.id === 'fixture-task') {
          remotePolls++;
        } else {
          res.statusCode = 400;
          res.end(JSON.stringify({ error: 'unexpected fixture request' }));
          return;
        }
        res.end(JSON.stringify({ jsonrpc: '2.0', id: rpc.id, result: { kind: 'task', id: 'fixture-task', contextId: 'context', status: { state: remoteComplete ? 'completed' : 'working' }, artifacts: [{ name: 'report', parts: [{ kind: 'text', text: 'remote result' }] }] } }));
      }
    });
    remote.listen(0, '127.0.0.1');
    await once(remote, 'listening');
    cleanups.push(() => new Promise(r => remote.close(r)));
    const plan = { irVersion: '1.0', name: 'installed cross-adapter acceptance', root: 'root', subgraphs: { root: {
      nodes: [
        { id: 'local', kind: 'execute', executor: 'command', params: { program: '/bin/sh', args: ['-c', 'printf installed-result'] }, outputs: { stdout: {} } },
        { id: 'remote', kind: 'execute', executor: 'agent/a2a', requiredCapabilities: { remoteTask: '1' }, params: { url: `http://127.0.0.1:${remote.address().port}` }, inputs: { source: { ref: { node: 'local', port: 'stdout' } } }, outputs: { report: { path: 'report.txt', mime: 'text/plain' } } },
      ], output: { result: { ref: { node: 'local', port: 'stdout' } }, report: { ref: { node: 'remote', port: 'report' } } },
    } } };
    let mcp = new MCP(entry, env, workspace, { name: `fixture/${client.name}`, version: client.version });
    cleanups.push(async () => { if (mcp) await mcp.close(); });
    await mcp.connect();
    const catalog = await mcp.value('list_executors', {});
    assert.deepEqual(new Set(catalog.executors.map(e => e.executor)), new Set(['command', 'agent/a2a']), 'worker-adapter: installed executor discovery');
    const args = { plan, input: {}, workspaceId: 'project', idempotencyKey: 'installed-invocation' };
    const unauthorized = await mcp.call('start_run', { ...args, workspaceId: 'denied', idempotencyKey: 'denied' });
    assert.ok(unauthorized.isError, 'workspace authorization: unknown workspace accepted');
    assert.match(JSON.stringify(unauthorized), /workspace/i, 'workspace authorization: wrong failure classification');
    const first = await mcp.value('start_run', args);
    assert.ok(first.runId);
    assert.equal((await mcp.value('start_run', args)).runId, first.runId, 'submission: duplicate created another run');
    const conflict = await mcp.call('start_run', { ...args, input: { changed: true } });
    assert.ok(conflict.isError, 'submission: conflicting idempotency key accepted');
    assert.match(JSON.stringify(conflict), /idempotency|conflict/i, 'submission: wrong failure classification');
    assert.equal((await mcp.value('get_run', { runId: first.runId })).state, 'running');
    await mcp.close(); mcp = null; // Actual client process exits before execution.
    assert.equal(remoteSubmissions, 0, 'worker-adapter: drained worker executed a task');
    stop();
    cfg.workerCapacity = 1;
    await writeFile(env.MUXLIO_CONFIG, JSON.stringify(cfg));
    start(); // Restart the installed worker/coordinator with the durable queue.
    mcp = new MCP(entry, env, workspace, { name: `fixture/${client.name}`, version: client.version });
    await mcp.connect();
    if (recoverRemote) {
      await eventually(async () => {
        const { attempts } = await mcp.value('get_attempts', { runId: first.runId });
        assert.ok(attempts.some(a => a.state === 'detached' && a.operationRef?.id === 'fixture-task'), 'remote task has not been durably handed off');
      }, 'worker-adapter: pending remote task');
      await mcp.close(); mcp = null;
      stop(); // Recover an in-flight remote operation as well as queued work.
      remoteComplete = true;
      start();
      mcp = new MCP(entry, env, workspace, { name: `fixture/${client.name}`, version: client.version });
      await mcp.connect();
    }
    const rediscovered = await mcp.value('list_runs', {});
    assert.ok(JSON.stringify(rediscovered).includes(first.runId), 'client-configuration: disconnected run was not rediscoverable');
    const result = await eventually(async () => {
      const run = await mcp.value('get_run', { runId: first.runId });
      assert.equal(run.state, 'succeeded', JSON.stringify(run));
      return run;
    }, 'worker-adapter: eventual cross-adapter completion', recoverRemote ? 60000 : 30000);
    assert.equal(result.output.result, 'installed-result');
    assert.equal(remoteSubmissions, 1, 'worker-adapter: recovery resubmitted the remote operation');
    assert.match(remoteInput, /"source":"installed-result"/, 'worker-adapter: cross-adapter input was lost');
    if (recoverRemote) assert.ok(remotePolls > 0, 'worker-adapter: recovery did not poll the remote task');
    assert.match(result.output.report.digest, /^sha256:[a-f0-9]{64}$/);
    assert.equal((await mcp.value('read_artifact', { digest: result.output.report.digest })).content, 'remote result', 'worker-adapter: A2A artifact contents');
    const attempts = await mcp.value('get_attempts', { runId: first.runId });
    const records = Array.isArray(attempts) ? attempts : attempts.attempts;
    assert.equal(records.filter(a => !a.operationAction).length, 2, 'worker-adapter: expected exactly two execution attempts');
    assert.ok(records.every(a => !a.operationAction || a.operationAction === 'poll'), 'worker-adapter: unexpected recovery action');
    assert.deepEqual(new Set(records.map(a => a.executor)), new Set(['command', 'agent/a2a']));
    assert.equal((await mcp.value('start_run', args)).runId, first.runId);
    await mcp.close(); mcp = null;
    stop();
    // Reinstall the release over the existing prefix, then rerun setup/repair.
    // This is a same-version upgrade; version-to-version probes are opt-in.
    await cp(resolve(source), prefix, { recursive: true });
    cli('init', '--client', client.name, '--scope', scope, '--data-dir', runtime, '--workspace', workspace);
    assert.equal(await readFile(configPath, 'utf8'), installed, 'upgrade: registration changed');
    // An upgrade may update instructions while preserving discovery/activation.
    activation(await readFile(join(baseDir, client.skill), 'utf8'), client);
    t.diagnostic(`upgrade target=${cli('version').trim()}; from=${process.env.MUXLIO_ACCEPTANCE_UPGRADE_FROM ? 'supplied older prefix' : 'same-version reinstall'}`);
    start();
    mcp = new MCP(entry, env, workspace, { name: `fixture/${client.name}`, version: client.version });
    await mcp.connect();
    assert.equal((await mcp.value('get_run', { runId: first.runId })).output.result, 'installed-result', 'upgrade: lost result');
    assert.equal((await mcp.value('get_attempts', { runId: first.runId })).attempts.length, records.length, 'upgrade: repeated effects');
    // A real adapter error must remain distinguishable from client setup.
    const failingPlan = structuredClone(plan);
    failingPlan.subgraphs.root.nodes = [failingPlan.subgraphs.root.nodes[0]];
    failingPlan.subgraphs.root.nodes[0].params.args = ['-c', 'exit 9'];
    failingPlan.subgraphs.root.output = { result: { ref: { node: 'local', port: 'stdout' } } };
    const failed = await mcp.value('start_run', { plan: failingPlan, input: {}, workspaceId: 'project', idempotencyKey: 'adapter-failure' });
    await eventually(async () => {
      assert.equal((await mcp.value('get_run', { runId: failed.runId })).state, 'failed');
    }, 'worker-adapter: command failure was not surfaced');
    await mcp.close(); mcp = null;
    stop();
    cfg.executors.command.allow = false;
    await writeFile(env.MUXLIO_CONFIG, JSON.stringify(cfg));
    start();
    mcp = new MCP(entry, env, workspace, { name: `fixture/${client.name}`, version: client.version });
    await mcp.connect();
    await eventually(async () => {
      const denied = await mcp.call('preflight_run', { plan: failingPlan, input: {}, workspaceId: 'project' });
      assert.ok(denied.isError, 'worker authorization: denied command accepted');
      assert.match(JSON.stringify(denied), /denied|authoriz|policy|disabled/i);
    }, 'worker authorization: policy denial');

  });
}

if (!profile || profile === 'antigravity-unavailable') test('Antigravity registration is explicitly unavailable; no guessed client configuration', async () => {
  const root = await realpath(await mkdtemp(join(tmpdir(), 'muxlio-antigravity-')));
  try {
    const result = spawn(join(resolve(source), 'bin/muxlio'), ['integrations', 'add', '--client', 'antigravity', '--scope', 'project', '--transport', 'http', '--url', 'http://localhost:7602/mcp', '--workspace', root], { env: { PATH: process.env.PATH, HOME: root } });
    let diagnostic = '';
    result.stderr.on('data', c => { diagnostic += c; });
    const [code] = await once(result, 'exit');
    assert.notEqual(code, 0);
    assert.match(diagnostic, /unavailable.*verified/);
    assert.deepEqual(await (await import('node:fs/promises')).readdir(root), []);
  } finally { await rm(root, { recursive: true, force: true }); }
});
