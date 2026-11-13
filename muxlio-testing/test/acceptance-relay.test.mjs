import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { once } from 'node:events';
import { test } from 'node:test';

test('real-client evidence relay forwards frames and excludes unsuccessful calls and payloads', { timeout: 10000 }, async () => {
  const root = await mkdtemp(join(tmpdir(), 'muxlio-relay-'));
  try {
    const fake = join(root, 'server.mjs');
    const audit = join(root, 'audit.jsonl');
    await writeFile(fake, `
import { createInterface } from 'node:readline';
createInterface({ input: process.stdin }).on('line', line => {
  const r = JSON.parse(line);
  if (r.params.name === 'rpc-error') {
    console.log(JSON.stringify({ jsonrpc: '2.0', id: r.id, error: { code: -32603, message: 'private-error' } }));
    return;
  }
  console.log(JSON.stringify({ jsonrpc: '2.0', id: r.id, result: { isError: r.params.name === 'denied', content: [{ type: 'text', text: r.params.name === 'get_run' ? JSON.stringify({ state: r.params.arguments?.pending ? 'running' : 'succeeded', output: 'private-response' }) : 'private-response' }] } }));
});
`);
    const child = spawn(process.execPath, [new URL('../acceptance/audit-mcp.mjs', import.meta.url).pathname, fake], {
      env: { ...process.env, MUXLIO_ACCEPTANCE_MCP: process.execPath, MUXLIO_ACCEPTANCE_AUDIT: audit,
        MUXLIO_ACCEPTANCE_SUBMISSION: JSON.stringify({ secret: 'private-request', input: {} }), MUXLIO_ACCEPTANCE_RUN_ID: 'private-run' },
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    const exit = once(child, 'exit');
    let output = '';
    child.stdout.on('data', c => { output += c; });
    child.stderr.resume();
    const calls = [
      ['start_run', { input: {}, secret: 'private-request' }], // Key order does not matter.
      ['denied', {}],
      ['get_run', { runId: 'private-run' }],
      ['rpc-error', {}],
      ['start_run', { secret: 'different-private-request', input: {} }],
      ['get_run', { runId: 'another-private-run' }],
      ['get_run', { runId: 'private-run', pending: true }],
    ];
    for (const [i, [name, args]] of calls.entries()) {
      child.stdin.write(JSON.stringify({ jsonrpc: '2.0', id: i, method: 'tools/call', params: { name, arguments: args } }) + '\n');
    }
    // Notifications and unsolicited responses must not count as tool evidence.
    child.stdin.write(JSON.stringify({ jsonrpc: '2.0', method: 'tools/call', params: { name: 'notification' } }) + '\n');
    child.stdin.end();
    assert.equal((await exit)[0], 0);
    const frames = output.trim().split('\n').map(JSON.parse);
    assert.equal(frames.length, 8);
    assert.equal(frames[1].result.isError, true);
    assert.equal(frames[0].result.content[0].text, 'private-response');
    const evidence = await readFile(audit, 'utf8');
    assert.deepEqual(evidence.trim().split('\n').map(JSON.parse), [
      { name: 'start_run', expected: true }, { name: 'get_run', expected: true, succeeded: true },
      { name: 'start_run', expected: false }, { name: 'get_run', expected: false, succeeded: true },
      { name: 'get_run', expected: true },
    ]);
    assert.doesNotMatch(evidence, /private-request|private-run|private-response|private-error|secret|denied|rpc-error|notification/);
  } finally { await rm(root, { recursive: true, force: true }); }
});
