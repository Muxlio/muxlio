import { port } from "./helpers.ts";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import { test } from "node:test";
import { once } from "node:events";
import { createUIServer } from "../src/server.ts";

function router(url) {
  const listeners: Record<string, (...args: any[]) => void> = {},
    changes = [],
    events = [];
  let location = new URL(url);
  const ctx = vm.createContext({
    URL,
    document: { addEventListener: (n, f) => (listeners[n] = f) },
    get location() {
      return location;
    },
    PopStateEvent: class {
      type: string;
      constructor(type) {
        this.type = type;
      }
    },
    window: {
      history: {
        pushState(_, __, path) {
          location = new URL(path, location);
          changes.push(path);
        },
        replaceState(_, __, path) {
          location = new URL(path, location);
          changes.push(path);
        },
      },
      dispatchEvent: (e) => events.push(e.type),
    },
  });
  const source = readFileSync(
    new URL("./.generated/core.js", import.meta.url),
    "utf8",
  ).split("const $=")[0];
  vm.runInContext(source, ctx);
  return {
    ctx,
    listeners,
    changes,
    events,
    get url() {
      return location;
    },
  };
}
test("normal paths use browser history, preserving encoded IDs and analytics queries", () => {
  const r = router("http://localhost/runs");
  r.ctx.navigateTo("/runs/a%2Fb");
  assert.equal(r.url.pathname, "/runs/a%2Fb");
  assert.deepEqual(r.events, ["popstate"]);
  r.ctx.navigateTo("/analytics?range=7d&workflow=demo");
  assert.equal(r.ctx.currentRoute(), "/analytics?range=7d&workflow=demo");
  r.ctx.navigateTo("https://elsewhere.test/runs/x");
  assert.equal(r.changes.length, 2);
  assert.equal(r.ctx.isUIPath("/v1/runs"), false);
});
test("legacy bookmarks migrate without adding a history entry", () => {
  for (const [old, want] of [
    ["#run/a%2Fb", "/runs/a%2Fb"],
    ["#/run/run-a", "/runs/run-a"],
    ["#analytics?range=7d", "/analytics?range=7d"],
    [
      "#worker-capability/w/generic%2Fecho/1",
      "/worker-capabilities/w/generic%2Fecho/1",
    ],
  ]) {
    const r = router("http://localhost/" + old);
    r.ctx.migrateLegacyRoute();
    assert.equal(r.ctx.currentRoute(), want);
    assert.equal(r.url.hash, "");
    assert.equal(r.events.length, 0);
  }
});
test("links preserve native modified clicks, external links and page anchors", () => {
  const r = router("http://localhost/");
  let prevented = 0;
  const link = (href) => ({
    target: "",
    hasAttribute: () => false,
    getAttribute: () => href,
  });
  const click = (href, extra = {}) =>
    r.listeners.click({
      button: 0,
      target: { closest: () => link(href) },
      preventDefault() {
        prevented++;
      },
      ...extra,
    });
  click("/runs/a");
  assert.equal(prevented, 1);
  assert.equal(r.url.pathname, "/runs/a");
  click("/runs/b", { ctrlKey: true });
  click("https://elsewhere.test/runs/a");
  click("#worker-roster-local");
  assert.equal(prevented, 1);
  assert.equal(r.url.pathname, "/runs/a");
});
test("navigate strips query string before extracting transcript and worker-capability IDs", () => {
  // navigate() must split on the path (query-stripped) not the full currentRoute() string.
  // A direct page load with a query string, or navigateTo() with a query on a transcript/wc
  // URL, would otherwise bleed '?...' into the actId or capability name used in API calls.
  const fs = readFileSync;
  const coreSrc = fs(new URL("./.generated/core.js", import.meta.url), "utf8");
  // Extract currentRoute + navigate so we can run them with mocked globals.
  const crSrc = coreSrc.slice(
    coreSrc.indexOf("function currentRoute()"),
    coreSrc.indexOf("\nfunction isUIPath"),
  );
  const navSrc = coreSrc.slice(
    coreSrc.indexOf("\nfunction navigate()"),
    coreSrc.indexOf("\nasync function fetchRuns()"),
  );
  let loadedTranscript = null,
    loadedWC = null;
  function makeCtx(url) {
    let loc = new URL(url);
    return vm.createContext({
      URL,
      get location() {
        return loc;
      },
      message() {},
      setView() {},
      loadTranscriptPage(runId, actId) {
        loadedTranscript = { runId, actId };
      },
      openRun() {},
      loadOpsDashboard() {},
      loadAutomation() {},
      loadRetention() {},
      loadWorkerCapabilityPage(...args) {
        loadedWC = args;
      },
      loadWorkers() {},
      loadLive() {},
      loadRuns() {},
      loadWorkflows() {},
      loadArtifacts() {},
      loadAnalytics() {},
      loadOverview() {},
      analyticsFiltersFromURL() {},
      current: null,
    });
  }
  // Test transcript URL with query string
  {
    const ctx = makeCtx("http://localhost/transcripts/runA/act%2FB?debug=1");
    vm.runInContext(crSrc + navSrc, ctx);
    ctx.navigate();
    assert.ok(
      loadedTranscript,
      "loadTranscriptPage should be called for transcript route",
    );
    assert.equal(
      loadedTranscript.runId,
      "runA",
      "runId must not contain query string",
    );
    assert.equal(
      loadedTranscript.actId,
      "act/B",
      "actId must be decoded path segment, not contain query string",
    );
  }
  // Test worker-capabilities URL with query string
  {
    loadedWC = null;
    const ctx = makeCtx(
      "http://localhost/worker-capabilities/w/cap%2Fecho?debug=1",
    );
    vm.runInContext(crSrc + navSrc, ctx);
    ctx.navigate();
    assert.ok(
      loadedWC,
      "loadWorkerCapabilityPage should be called for worker-capabilities route",
    );
    assert.equal(loadedWC[0], "w", "workerId must not contain query string");
    assert.equal(
      loadedWC[1],
      "cap/echo",
      "capability name must be decoded path segment, not contain query string",
    );
  }
});
test("development server serves direct app routes and keeps unknown APIs/assets as 404", async () => {
  const server = createUIServer();
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  try {
    const root = `http://127.0.0.1:${port(server)}`;
    for (const path of [
      "/runs",
      "/runs/run-a",
      "/runs/a%2Fb",
      "/runs/a%2Fb/",
      "/analytics?range=7d",
      "/transcripts/run-a/node%2Fa",
      "/worker-capabilities/local/generic%2Fecho/1",
      "/worker-capabilities/local/generic%2Fecho/",
    ]) {
      const response = await fetch(root + path);
      assert.equal(response.status, 200, path);
      assert.match(await response.text(), /<title>Muxlio/);
      const head = await fetch(root + path, { method: "HEAD" });
      assert.equal(head.status, 200);
      assert.equal(await head.text(), "");
    }
    const page = await (await fetch(root + "/analytics")).text();
    for (const match of page.matchAll(/<script\b[^>]*src="([^"]+)"/g)) {
      const response = await fetch(root + match[1]);
      assert.equal(response.status, 200, match[1]);
      assert.match(response.headers.get("content-type"), /javascript/);
    }
    for (const path of [
      "/missing",
      "/dist/not-real.js",
      "/js/core.js",
      "/workspace.js",
      "/src/core.ts",
      "/runs/a/unexpected",
    ])
      assert.equal((await fetch(root + path)).status, 404, path);
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});

test("compiled assets are served without registering individual filenames", async (t) => {
  const { mkdtemp, mkdir, writeFile, rm } = await import("node:fs/promises");
  const { tmpdir } = await import("node:os");
  const { join } = await import("node:path");
  const directory = await mkdtemp(join(tmpdir(), "muxlio-ui-assets-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  await mkdir(join(directory, "dist"));
  await writeFile(
    join(directory, "dist", "chunk-a1b2.js"),
    "export const ready = true;",
  );
  const server = createUIServer({ uiDirectory: directory });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  t.after(() => new Promise<void>((resolve) => server.close(() => resolve())));
  const root = `http://127.0.0.1:${port(server)}`;
  const response = await fetch(root + "/dist/chunk-a1b2.js");
  assert.equal(response.status, 200);
  assert.match(response.headers.get("content-type") || "", /javascript/);
  assert.match(await response.text(), /ready = true/);
  assert.equal((await fetch(root + "/dist/")).status, 404);
  assert.equal((await fetch(root + "/src/server.ts")).status, 404);
  assert.equal(
    (await fetch(root + "/dist/%2e%2e%2fsrc%2fserver.ts")).status,
    404,
  );
});
