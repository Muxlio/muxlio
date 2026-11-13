import { port } from "./helpers.ts";
import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer, request } from "node:http";
import { mkdtemp, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { createUIServer } from "../src/server.ts";

function setEnvironment(t, values: Record<string, string | undefined>) {
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

test("malformed unauthenticated URLs do not crash the UI server", async (t) => {
  const ui = createUIServer();
  ui.listen(0, "127.0.0.1");
  await once(ui, "listening");
  t.after(() => new Promise((resolve) => ui.close(resolve)));
  const status = (path: string) =>
    new Promise<number>((resolve, reject) => {
      const req = request(
        { host: "127.0.0.1", port: port(ui), path },
        (res) => {
          res.resume();
          res.on("end", () => resolve(res.statusCode!));
        },
      );
      req.on("error", reject);
      req.end();
    });
  for (const path of ["//[", "http://["]) {
    assert.equal(await status(path), 400);
  }
  assert.equal(await status("/missing"), 404);
});

test("UI proxy limits automatic credentials to safe local requests", async (t) => {
  setEnvironment(t, { MUXLIO_TOKEN: "operator-admin" });
  const coordinator = createServer((req, res) => {
    res.writeHead(200, { "content-type": "application/json" });
    res.end(
      JSON.stringify({
        authorization: req.headers.authorization || "",
        origin: req.headers.origin || "",
        path: req.url,
      }),
    );
  });
  coordinator.listen(0, "127.0.0.1");
  await once(coordinator, "listening");
  t.after(() => new Promise((resolve) => coordinator.close(resolve)));
  const ui = createUIServer({
    coordinatorURL: new URL(`http://127.0.0.1:${port(coordinator)}`),
  });
  ui.listen(0, "127.0.0.1");
  await once(ui, "listening");
  t.after(() => new Promise((resolve) => ui.close(resolve)));
  const url = `http://127.0.0.1:${port(ui)}/v1/runs/other/cancel`;
  const rebinding = await new Promise<number>((resolve, reject) => {
    const req = request(
      url,
      { method: "POST", headers: { host: "evil.example" } },
      (res) => {
        res.resume();
        resolve(res.statusCode!);
      },
    );
    req.on("error", reject);
    req.end();
  });
  assert.equal(rebinding, 401);
  for (const authorization of [
    "",
    ["Bearer scoped", "Bearer operator-admin"],
  ]) {
    const status = await new Promise<number>((resolve, reject) => {
      const req = request(url, { method: "POST" }, (res) => {
        res.resume();
        resolve(res.statusCode!);
      });
      req.appendHeader("Authorization", authorization);
      req.on("error", reject);
      req.end();
    });
    assert.equal(
      status,
      401,
      "explicit invalid credentials must not inherit admin access",
    );
  }
  for (const [headers, expected, status] of [
    [{}, "Bearer operator-admin", 200],
    [{ origin: `http://127.0.0.1:${port(ui)}` }, "Bearer operator-admin", 200],
    [{ origin: "null" }, "", 403],
    [
      { authorization: "Bearer scoped", origin: "https://evil.example" },
      "",
      403,
    ],
    [{ authorization: "Bearer scoped" }, "Bearer scoped", 200],
    [{ authorization: "Bearer worker" }, "Bearer worker", 200],
    [{ origin: "http://192.168.1.1:7600" }, "", 403],
    [{ "sec-fetch-site": "cross-site" }, "", 403],
  ]) {
    const response = await fetch(url, {
      method: "POST",
      headers: headers as Record<string, string>,
    });
    assert.equal(response.status, status, JSON.stringify(headers));
    assert.equal(response.headers.get("x-frame-options"), "DENY");
    if (status === 200) {
      const body = await response.json();
      assert.equal(body.authorization, expected);
      assert.equal(body.origin, "");
      assert.equal(body.path, "/v1/runs/other/cancel");
    } else await response.text();
  }
});

test("UI proxy reloads credentials created or rotated after startup", async (t) => {
  const runtime = await mkdtemp(join(tmpdir(), "muxlio-ui-auth-"));
  t.after(() => rm(runtime, { recursive: true, force: true }));
  const configPath = join(runtime, "config.json");
  setEnvironment(t, { MUXLIO_CONFIG: configPath, MUXLIO_TOKEN: undefined });
  let expectedToken = "first-token";
  const coordinator = createServer((req, res) => {
    res.writeHead(
      req.headers.authorization === `Bearer ${expectedToken}` ? 200 : 401,
    );
    res.end();
  });
  coordinator.listen(0, "127.0.0.1");
  await once(coordinator, "listening");
  t.after(() => new Promise((resolve) => coordinator.close(resolve)));
  const ui = createUIServer({
    coordinatorURL: new URL(`http://127.0.0.1:${port(coordinator)}`),
  });
  ui.listen(0, "127.0.0.1");
  await once(ui, "listening");
  t.after(() => new Promise((resolve) => ui.close(resolve)));
  const url = `http://127.0.0.1:${port(ui)}/v1/runs`;
  const check = async (status) => {
    const response = await fetch(url);
    await response.arrayBuffer();
    assert.equal(response.status, status);
  };
  await check(401);
  await writeFile(configPath, JSON.stringify({ token: expectedToken }));
  await check(200);
  expectedToken = "rotated-token";
  await writeFile(configPath, JSON.stringify({ token: expectedToken }));
  await check(200);
  await writeFile(join(runtime, "credential"), expectedToken);
  await writeFile(configPath, JSON.stringify({ tokenFile: "credential" }));
  await check(200);
  expectedToken = "rotated-file-token";
  await writeFile(join(runtime, "credential"), expectedToken);
  await check(200);
  await rm(join(runtime, "credential"));
  await check(401);
});

test("wildcard UI listeners require explicit credentials even on loopback", async (t) => {
  setEnvironment(t, { MUXLIO_TOKEN: "operator-admin" });
  const coordinator = createServer((req, res) => {
    res.end(req.headers.authorization || "none");
  });
  coordinator.listen(0, "127.0.0.1");
  await once(coordinator, "listening");
  t.after(() => new Promise((resolve) => coordinator.close(resolve)));
  const ui = createUIServer({
    coordinatorURL: new URL(`http://127.0.0.1:${port(coordinator)}`),
  });
  ui.listen(0, "0.0.0.0");
  await once(ui, "listening");
  t.after(() => new Promise((resolve) => ui.close(resolve)));
  const url = `http://127.0.0.1:${port(ui)}/v1/runs`;
  assert.equal((await fetch(url)).status, 401);
  const response = await fetch(url, {
    headers: { authorization: "Bearer reader" },
  });
  assert.equal(response.status, 200);
  assert.equal(await response.text(), "Bearer reader");
});
