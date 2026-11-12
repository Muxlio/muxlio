import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import { test as regression } from "node:test";
regression("snapshots regression", async () => {
  const source = readFileSync(
    new URL("./.generated/inspector.js", import.meta.url),
    "utf8",
  );
  const start = source.indexOf("let snapshotPollTimer=");
  const end = source.indexOf("/* ── shared helpers", start);
  const panel = { innerHTML: "", querySelectorAll: () => [] };
  const calls = [];
  const snapshot = (seq, digest) => ({
    seq,
    subjectId: "attempt",
    timestamp: "now",
    payload: { snapshot: { name: "report", digest } },
  });
  const context: Record<string, any> = {
    selectedActivation: "act",
    current: "run",
    TRANSCRIPT_POLL_MS: 1200,
    clearTimeout() {},
    setTimeout() {
      throw Error("finished attempts must not poll");
    },
    $: () => panel,
    esc: String,
    date: String,
    api: async (path) => {
      calls.push(path);
      return calls.length === 1
        ? { snapshots: [snapshot(3, "old")], nextAfter: 3, hasMore: true }
        : { snapshots: [snapshot(5, "new")], nextAfter: 5, hasMore: false };
    },
  };
  vm.createContext(context);
  vm.runInContext(source.slice(start, end), context);
  await context.loadInspectorSnapshots({ id: "act", state: "failed" }, "run");
  assert.equal(
    calls.length,
    1,
    "opening the inspector must fetch only one snapshot page",
  );
  assert.match(calls[0], /limit=20/);
  assert.match(panel.innerHTML, /data-snapshot-page="1"/);
  vm.runInContext("snapshotPageIndex=1", context);
  await context.loadInspectorSnapshots({ id: "act", state: "failed" }, "run");
  assert.equal(calls.length, 2);
  assert.match(calls[1], /after=3/);
  assert.match(panel.innerHTML, /not committed final outputs/);
  assert.match(panel.innerHTML, /seq 5/);
  assert.doesNotMatch(
    panel.innerHTML,
    /seq 3/,
    "pages replace previous snapshot DOM",
  );
  assert.match(panel.innerHTML, /attempt/);
  console.log("snapshot inspector pagination and provenance checks passed");
});
