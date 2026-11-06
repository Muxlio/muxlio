import assert from 'node:assert/strict';
import { once } from 'node:events';
import { createServer } from 'node:http';
import test from 'node:test';

// Load the server with a known operator credential, independent of host config.
const previousToken = process.env.MUXLIO_TOKEN;
process.env.MUXLIO_TOKEN = 'operator-admin';
const { createUIServer, localUIRequest } = await import('./server.mjs');
if (previousToken === undefined) delete process.env.MUXLIO_TOKEN;
else process.env.MUXLIO_TOKEN = previousToken;

test('implicit UI credentials require a same-origin local request', () => {
  for (const [peer, host, origin, site, expected] of [
    ['127.0.0.1', 'localhost:7601', undefined, undefined, true],
    ['::1', '[::1]:7601', 'http://[::1]:7601', undefined, true],
    ['::ffff:127.0.0.1', '127.0.0.1:7601', undefined, undefined, true],
    ['203.0.113.1', 'localhost:7601', undefined, undefined, false],
    ['127.0.0.1', 'evil.example', undefined, undefined, false],
    ['127.0.0.1', 'localhost:7601', 'https://evil.example', undefined, false],
    ['127.0.0.1', 'localhost:7601', 'null', undefined, false],
    ['127.0.0.1', 'localhost:7601', undefined, 'cross-site', false],
  ]) {
    assert.equal(localUIRequest({ socket: { remoteAddress: peer }, headers: { host, origin, 'sec-fetch-site': site } }), expected);
  }
});

test('UI proxy preserves scoped credentials and denies cross-origin implicit authority', async t => {
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
  const url = `http://127.0.0.1:${ui.address().port}/_ui-api/v1/runs/other/cancel`;
  for (const [headers, expected] of [
    [{}, 'Bearer operator-admin'],
    [{ authorization: 'Bearer scoped' }, 'Bearer scoped'],
    [{ authorization: 'Bearer worker' }, 'Bearer worker'],
    [{ origin: 'https://evil.example' }, ''],
    [{ 'sec-fetch-site': 'cross-site' }, ''],
  ]) {
    const response = await fetch(url, { method: 'POST', headers });
    const body = await response.json();
    assert.equal(body.authorization, expected);
    assert.equal(body.path, '/v1/runs/other/cancel');
  }
});
