import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import { once } from 'node:events';

// Exercise the installed registration over real stdio, without client-specific
// behavior in the server or worker. All requests and shutdowns are bounded.
export class MCP {
  constructor(entry, env, cwd, clientInfo = { name: 'muxlio-acceptance-fixture', version: '1.0.0' }) {
    this.clientInfo = clientInfo;
    this.pending = new Map();
    this.id = 0;
    this.child = spawn(entry.command, entry.args, { cwd, env: { ...env, ...entry.env }, stdio: ['pipe', 'pipe', 'pipe'] });
    const fail = message => {
      this.failure = new Error(`client-configuration: ${message}`);
      for (const { reject } of this.pending.values()) reject(this.failure);
    };
    this.child.on('error', () => fail('registered MCP executable could not launch'));
    this.child.on('close', (code, signal) => fail(`MCP exited (code=${code}, signal=${signal})`));
    this.child.stderr.resume(); // Never print credentials or model transcripts.
    this.child.stdin.on('error', () => fail('MCP input closed'));
    createInterface({ input: this.child.stdout }).on('line', line => {
      try {
        const response = JSON.parse(line);
        this.pending.get(response.id)?.resolve(response);
      } catch { /* A malformed response fails the bounded request. */ }
    });
  }

  async request(method, params) {
    if (this.failure) throw this.failure;
    const id = ++this.id;
    let timer;
    try {
      const response = await new Promise((resolve, reject) => {
        this.pending.set(id, { resolve, reject });
        timer = setTimeout(() => reject(new Error(`client-configuration: MCP ${method} timed out`)), 10000);
        this.child.stdin.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n');
      });
      assert.equal(response.error, undefined, `client-configuration: ${JSON.stringify(response.error)}`);
      return response.result;
    } finally {
      clearTimeout(timer);
      this.pending.delete(id);
    }
  }

  async connect() {
    const result = await this.request('initialize', { protocolVersion: '2025-11-25', capabilities: {}, clientInfo: this.clientInfo });
    assert.equal(result.protocolVersion, '2025-11-25', 'client-configuration: incompatible MCP version');
    assert.ok(result.serverInfo.version, 'client-configuration: missing product version');
    this.child.stdin.write(JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized' }) + '\n');
    const { tools } = await this.request('tools/list', {});
    for (const name of ['start_run', 'get_run', 'list_runs', 'list_executors']) {
      assert.ok(tools.some(t => t.name === name), `client-configuration: missing ${name}`);
    }
  }

  call(name, args) {
    return this.request('tools/call', { name, arguments: args });
  }

  async value(name, args) {
    const result = await this.call(name, args);
    assert.ok(!result.isError, `coordinator/worker-adapter: ${name}: ${JSON.stringify(result)}`);
    return JSON.parse(result.content[0].text);
  }

  async close() {
    if (this.child.exitCode !== null || this.child.signalCode !== null || !this.child.pid) return;
    const exited = once(this.child, 'close');
    this.child.stdin.end();
    const kill = setTimeout(() => this.child.kill('SIGKILL'), 2000);
    try { await exited; } finally { clearTimeout(kill); }
  }
}
