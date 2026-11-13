import { createServer, request as httpRequest } from "node:http";
import { request as httpsRequest } from "node:https";
import { readFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { dirname, isAbsolute, join, resolve, extname, sep } from "node:path";
import { isIP } from "node:net";
import { homedir } from "node:os";

const directory = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const apiURL = new URL(process.env.MUXLIO_API_URL || "http://127.0.0.1:7600");
function configuredToken() {
  if (process.env.MUXLIO_TOKEN?.trim()) return process.env.MUXLIO_TOKEN.trim();
  const configPath =
    process.env.MUXLIO_CONFIG ||
    join(process.env.MUXLIO_HOME || join(homedir(), ".muxlio"), "config.json");
  try {
    const config = JSON.parse(readFileSync(configPath, "utf8"));
    if (typeof config.token === "string" && config.token.trim())
      return config.token.trim();
    if (typeof config.tokenFile === "string" && config.tokenFile.trim()) {
      const tokenPath = isAbsolute(config.tokenFile)
        ? config.tokenFile
        : join(dirname(configPath), config.tokenFile);
      try {
        const token = readFileSync(tokenPath, "utf8").trim();
        if (token) return token;
      } catch {}
    }
    const target = config.defaultServer;
    const profile = config.servers?.[target] || config.profiles?.[target];
    if (
      profile &&
      typeof profile === "object" &&
      typeof profile.token === "string"
    )
      return profile.token.trim();
  } catch {}
  return "";
}
const port = Number(process.env.PORT || 7601);
const host = process.env.HOST || "127.0.0.1";

export function createUIServer({
  coordinatorURL = apiURL,
  uiDirectory = directory,
} = {}) {
  const target =
    coordinatorURL instanceof URL ? coordinatorURL : new URL(coordinatorURL);
  const server = createServer(async (req, res) => {
    res.setHeader("X-Content-Type-Options", "nosniff");
    res.setHeader("X-Frame-Options", "DENY");
    res.setHeader("Referrer-Policy", "same-origin");
    const apiRequest = req.url?.startsWith("/v1/");
    if (apiRequest || req.url === "/v1" || req.url === "/healthz") {
      // Reject browser CSRF before considering automatic local credentials.
      const origins = req.headersDistinct.origin || [];
      const credentials = req.headersDistinct.authorization || [];
      const site = req.headers["sec-fetch-site"];
      if (
        (site && site !== "same-origin" && site !== "none") ||
        (origins.length > 0 &&
          (origins.length !== 1 || origins[0] !== `http://${req.headers.host}`))
      ) {
        res.writeHead(403).end("Cross-origin browser request denied");
        return;
      }
      if (
        credentials.length > 1 ||
        (credentials.length === 1 && !credentials[0].trim())
      ) {
        res.writeHead(401).end("Invalid authorization header");
        return;
      }
      const address = server.address();
      let localHost = false;
      try {
        localHost = isLoopback(new URL(`http://${req.headers.host}`).hostname);
      } catch {}
      const local =
        address &&
        typeof address !== "string" &&
        isLoopback(address.address) &&
        isLoopback(req.socket.remoteAddress || "") &&
        localHost;
      if (!req.headers.authorization && !local && apiRequest) {
        res.writeHead(401).end("Remote UI access requires a bearer credential");
        return;
      }
      const apiToken =
        apiRequest && local && !req.headers.authorization
          ? configuredToken()
          : "";
      const headers = { ...req.headers, host: target.host };
      // The browser Origin was validated against this UI above. It is not the
      // coordinator's origin when the UI is hosted on a separate port.
      delete headers.origin;
      const proxy = (target.protocol === "https:" ? httpsRequest : httpRequest)(
        new URL(req.url, target),
        {
          method: req.method,
          headers: {
            ...headers,
            ...(apiToken ? { authorization: `Bearer ${apiToken}` } : {}),
          },
        },
        (upstream) => {
          res.writeHead(upstream.statusCode || 502, upstream.headers);
          upstream.pipe(res);
        },
      );
      proxy.on("error", () => {
        if (!res.headersSent)
          res.writeHead(502, { "content-type": "text/plain; charset=utf-8" });
        res.end("Coordinator unavailable");
      });
      req.pipe(proxy);
      return;
    }

    if (req.method !== "GET" && req.method !== "HEAD") {
      res.writeHead(405, { allow: "GET, HEAD" }).end();
      return;
    }
    let pathname: string;
    try {
      pathname = new URL(req.url || "/", "http://localhost").pathname;
    } catch {
      res.writeHead(400).end("Invalid request URL");
      return;
    }
    let assetName;
    try {
      assetName = decodeURIComponent(pathname);
    } catch {
      res.writeHead(404).end("Not found");
      return;
    }
    const assetPath = resolve(uiDirectory, "." + assetName);
    const builtAsset =
      assetName.startsWith("/dist/") &&
      assetPath.startsWith(resolve(uiDirectory, "dist") + sep);
    const brandingAsset =
      assetName === "/logo.png" || assetName === "/favicon.ico";
    if (builtAsset || brandingAsset) {
      const contentType = {
        ".js": "text/javascript",
        ".css": "text/css",
        ".map": "application/json",
        ".png": "image/png",
        ".ico": "image/x-icon",
      }[extname(assetName)];
      if (!contentType) {
        res.writeHead(404).end("Not found");
        return;
      }
      try {
        const asset = await readFile(assetPath);
        res.writeHead(200, {
          "content-type": contentType,
          "cache-control": "no-store",
        });
        res.end(req.method === "HEAD" ? undefined : asset);
      } catch {
        res.writeHead(404).end("Not found");
      }
      return;
    }
    if (
      !/^\/(?:runs(?:\/[^/]+)?|transcripts\/[^/]+\/[^/]+|worker-capabilities\/[^/]+\/[^/]+(?:\/[^/]+)?|operations|automation|retention|workers|live|workflows|artifacts|analytics|compare|evidence)\/?$/.test(
        pathname,
      ) &&
      pathname !== "/"
    ) {
      res.writeHead(404).end("Not found");
      return;
    }
    try {
      const html = await readFile(join(uiDirectory, "index.html"));
      res.writeHead(200, {
        "content-type": "text/html; charset=utf-8",
        "content-length": Buffer.byteLength(html),
        "cache-control": "no-store",
      });
      res.end(req.method === "HEAD" ? undefined : html);
    } catch {
      res.writeHead(500).end("Observability UI unavailable");
    }
  });
  return server;
}

function isLoopback(host: string) {
  host = host.replace(/^\[|\]$/g, "").toLowerCase();
  if (host === "localhost" || host === "::1") return true;
  if (host.startsWith("::ffff:")) host = host.slice(7);
  return isIP(host) === 4 && host.startsWith("127.");
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  createUIServer().listen(port, host, () => {
    const displayHost = host === "0.0.0.0" ? "0.0.0.0" : host;
    console.log(
      `Muxlio UI listening at http://${displayHost}:${port} (coordinator: ${apiURL})`,
    );
  });
}
