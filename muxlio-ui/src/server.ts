import { createServer, request as httpRequest } from "node:http";
import { readFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { dirname, isAbsolute, join, resolve, extname, sep } from "node:path";
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
  return createServer(async (req, res) => {
    const apiRequest = req.url?.startsWith("/v1/");
    if (apiRequest || req.url === "/v1" || req.url === "/healthz") {
      // UI clients on the trusted local network do not need credentials.
      // The coordinator may create or rotate credentials while the UI stays up.
      const apiToken =
        apiRequest && !req.headers.authorization ? configuredToken() : "";
      const proxy = httpRequest(
        new URL(req.url, target),
        {
          method: req.method,
          headers: {
            ...req.headers,
            host: target.host,
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
    const pathname = new URL(req.url || "/", "http://localhost").pathname;
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
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  createUIServer().listen(port, host, () => {
    const displayHost = host === "0.0.0.0" ? "0.0.0.0" : host;
    console.log(
      `Muxlio UI listening at http://${displayHost}:${port} (coordinator: ${apiURL})`,
    );
  });
}
