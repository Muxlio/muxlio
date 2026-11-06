import { createServer, request as httpRequest } from 'node:http';
import { readFileSync } from 'node:fs';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, isAbsolute, join } from 'node:path';
import { homedir } from 'node:os';
import { isIP } from 'node:net';

const directory = dirname(fileURLToPath(import.meta.url));
const apiURL = new URL(process.env.MUXLIO_API_URL || 'http://127.0.0.1:7600');
function configuredToken() {
  if (process.env.MUXLIO_TOKEN?.trim()) return process.env.MUXLIO_TOKEN.trim();
  const configPath = process.env.MUXLIO_CONFIG || join(process.env.MUXLIO_HOME || join(homedir(), '.muxlio'), 'config.json');
  try {
    const config = JSON.parse(readFileSync(configPath, 'utf8'));
    if (typeof config.token === 'string' && config.token.trim()) return config.token.trim();
    if (typeof config.tokenFile === 'string' && config.tokenFile.trim()) {
      const tokenPath = isAbsolute(config.tokenFile) ? config.tokenFile : join(dirname(configPath), config.tokenFile);
      try {
        const token = readFileSync(tokenPath, 'utf8').trim();
        if (token) return token;
      } catch {}
    }
    const target = config.defaultServer;
    const profile = config.servers?.[target] || config.profiles?.[target];
    if (profile && typeof profile === 'object' && typeof profile.token === 'string') return profile.token.trim();
  } catch {}
  return '';
}
const apiToken = configuredToken();
const port = Number(process.env.PORT || 7601);
const host = process.env.HOST || '127.0.0.1';

// Implicit operator credentials belong only to the same-origin local UI.
export function localUIRequest(req) {
  const loopback = value => {
    const ip = value?.replace(/^::ffff:/i, '');
    return ip === '::1' || (isIP(ip || '') === 4 && ip.startsWith('127.'));
  };
  if (!loopback(req.socket.remoteAddress)) return false;
  try {
    const local = new URL(`http://${req.headers.host}`);
    if (local.hostname !== 'localhost' && !loopback(local.hostname.replace(/^\[|\]$/g, ''))) return false;
    if (req.headers['sec-fetch-site'] === 'cross-site') return false;
    if (req.headers.origin && req.headers.origin !== local.origin) return false;
    return true;
  } catch { return false; }
}

export function createUIServer({ coordinatorURL = apiURL, uiDirectory = directory } = {}) {
  const target = coordinatorURL instanceof URL ? coordinatorURL : new URL(coordinatorURL);
  return createServer(async (req, res) => {
    const uiAPI = req.url?.startsWith('/_ui-api/v1/');
    if (uiAPI || req.url === '/v1' || req.url?.startsWith('/v1/') || req.url === '/healthz') {
      const upstreamPath = uiAPI ? req.url.slice('/_ui-api'.length) : req.url;
      const proxy = httpRequest(new URL(upstreamPath, target), {
        method: req.method,
        headers: {
          ...req.headers,
          host: target.host,
          ...(uiAPI && apiToken && !req.headers.authorization && localUIRequest(req) ? { authorization: `Bearer ${apiToken}` } : {}),
        },
      }, (upstream) => {
        res.writeHead(upstream.statusCode || 502, upstream.headers);
        upstream.pipe(res);
      });
      proxy.on('error', () => {
        if (!res.headersSent) res.writeHead(502, { 'content-type': 'text/plain; charset=utf-8' });
        res.end('Coordinator unavailable');
      });
      req.pipe(proxy);
      return;
    }

    if (req.method !== 'GET' && req.method !== 'HEAD') {
      res.writeHead(405, { allow: 'GET, HEAD' }).end();
      return;
    }
    const pathname = new URL(req.url || '/', 'http://localhost').pathname;
    const staticAssets = {
      '/app.css': 'text/css',
      '/workspace.css': 'text/css',
      '/workspace.js': 'text/javascript',
      '/js/core.js': 'text/javascript',
      '/js/markdown.js': 'text/javascript',
      '/js/workbench.js': 'text/javascript',
      '/js/inspector.js': 'text/javascript',
      '/js/live.js': 'text/javascript',
      '/js/transcript.js': 'text/javascript',
      '/js/analytics.js': 'text/javascript',
      '/js/boot.js': 'text/javascript',
      '/logo.png': 'image/png',
      '/favicon.ico': 'image/x-icon',
    };
    if (staticAssets[pathname]) {
      try {
        const asset = await readFile(join(uiDirectory, pathname.slice(1)));
        res.writeHead(200, { 'content-type': staticAssets[pathname], 'cache-control': 'no-store' });
        res.end(req.method === 'HEAD' ? undefined : asset);
      } catch { res.writeHead(404).end('Not found'); }
      return;
    }
    if (pathname !== '/') {
      res.writeHead(404).end('Not found');
      return;
    }
    try {
      const html = await readFile(join(uiDirectory, 'index.html'));
      res.writeHead(200, { 'content-type': 'text/html; charset=utf-8', 'content-length': Buffer.byteLength(html), 'cache-control': 'no-store' });
      res.end(req.method === 'HEAD' ? undefined : html);
    } catch {
      res.writeHead(500).end('Observability UI unavailable');
    }
  });
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  createUIServer().listen(port, host, () => {
    const displayHost = host === '0.0.0.0' ? '0.0.0.0' : host;
    console.log(`Muxlio UI listening at http://${displayHost}:${port} (coordinator: ${apiURL})`);
  });
}
