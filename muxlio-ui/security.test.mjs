import assert from 'node:assert/strict';
import { once } from 'node:events';
import { createServer } from 'node:http';
import { mkdtemp, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { createUIServer } from './server.mjs';

function setEnvironment(t, values) {
  for (const [key, value] of Object.entries(values)) {
    const previous = process.env[key];
    if (value === undefined) delete process.env[key];
    else process.env[key] = value;
    t.after(() => {
      if (previous === undefined) delete process.env[key];
      else process.env[key] = previous;
    });
  }
}

test('UI proxy requires no browser credentials and preserves explicit scopes', async t => {
  setEnvironment(t, { MUXLIO_TOKEN: 'operator-admin' });
  const coordinator = createServer((req, res) => {
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ authorization: req.headers.authorization || '', path: req.url }));
  });
  coordinator.listen(0, '127.0.0.1');
  await once(coordinator, 'listening');
  t.after(() => new Promise(resolve => coordinator.close(resolve)));
  const ui = createUIServer({ coordinatorURL: `http://127.0.0.1:${coordinator.address().port}` });
  ui.listen(0, '127.0.0.1');
  await once(ui, 'listening');
  t.after(() => new Promise(resolve => ui.close(resolve)));
  const url = `http://127.0.0.1:${ui.address().port}/v1/runs/other/cancel`;
  for (const [headers, expected, status] of [
    [{}, 'Bearer operator-admin', 200],
    [{ authorization: 'Bearer scoped' }, 'Bearer scoped', 200],
    [{ authorization: 'Bearer worker' }, 'Bearer worker', 200],
    [{ origin: 'http://192.168.1.1:7600' }, 'Bearer operator-admin', 200],
    [{ 'sec-fetch-site': 'cross-site' }, 'Bearer operator-admin', 200],
  ]) {
    const response = await fetch(url, { method: 'POST', headers });
    const body = await response.json();
    assert.equal(response.status, status, JSON.stringify(headers));
    assert.equal(body.authorization, expected);
    if (status === 200) assert.equal(body.path, '/v1/runs/other/cancel');
  }
});

test('UI proxy reloads credentials created or rotated after startup', async t => {
  const runtime = await mkdtemp(join(tmpdir(), 'muxlio-ui-auth-'));
  t.after(() => rm(runtime, { recursive: true, force: true }));
  const configPath = join(runtime, 'config.json');
  setEnvironment(t, { MUXLIO_CONFIG: configPath, MUXLIO_TOKEN: undefined });
  let expectedToken = 'first-token';
  const coordinator = createServer((req, res) => {
    res.writeHead(req.headers.authorization === `Bearer ${expectedToken}` ? 200 : 401);
    res.end();
  });
  coordinator.listen(0, '127.0.0.1');
  await once(coordinator, 'listening');
  t.after(() => new Promise(resolve => coordinator.close(resolve)));
  const ui = createUIServer({ coordinatorURL: `http://127.0.0.1:${coordinator.address().port}` });
  ui.listen(0, '127.0.0.1');
  await once(ui, 'listening');
  t.after(() => new Promise(resolve => ui.close(resolve)));
  const url = `http://127.0.0.1:${ui.address().port}/v1/runs`;
  const check = async status => {
    const response = await fetch(url);
    await response.arrayBuffer();
    assert.equal(response.status, status);
  };
  await check(401);
  await writeFile(configPath, JSON.stringify({ token: expectedToken }));
  await check(200);
  expectedToken = 'rotated-token';
  await writeFile(configPath, JSON.stringify({ token: expectedToken }));
  await check(200);
  await writeFile(join(runtime, 'credential'), expectedToken);
  await writeFile(configPath, JSON.stringify({ tokenFile: 'credential' }));
  await check(200);
  expectedToken = 'rotated-file-token';
  await writeFile(join(runtime, 'credential'), expectedToken);
  await check(200);
  await rm(join(runtime, 'credential'));
  await check(401);
});
