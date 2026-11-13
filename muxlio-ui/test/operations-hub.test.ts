import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import test from "node:test";

const format = readFileSync(
  new URL("./.generated/format.js", import.meta.url),
  "utf8",
);
const usage = readFileSync(
  new URL("./.generated/usage-summary.js", import.meta.url),
  "utf8",
);
const operations = readFileSync(
  new URL("./.generated/operations.js", import.meta.url),
  "utf8",
);
const hub = readFileSync(
  new URL("./.generated/operations-hub.js", import.meta.url),
  "utf8",
);
const helpers = vm.runInNewContext(
  `${format}\n${usage}\n${operations}\n${hub};({evidenceDocument,reportRows,evidenceMarkup,hubAccounting,hubClientPage,comparisonMarkup,attemptRowMarkup})`,
  {
    document: {
      addEventListener() {},
      getElementById() {
        return null;
      },
    },
    loadWorkers() {},
    showWbTab() {},
    esc: (s) =>
      String(s ?? "").replace(
        /[&<>"']/g,
        (c) =>
          ({
            "&": "&amp;",
            "<": "&lt;",
            ">": "&gt;",
            '"': "&quot;",
            "'": "&#39;",
          })[c],
      ),
    stateClass: () => "",
    date: String,
    current: null,
  },
);

test("cancelled attempts with unresolved effects can be resolved", () => {
  const row = helpers.attemptRowMarkup({
    id: "attempt",
    runId: "run",
    state: "cancelled",
    awaitReconciliationReason: "External outcome unknown",
  });
  assert.match(row, /Resolve uncertain execution/);
  assert.doesNotMatch(
    helpers.attemptRowMarkup({ id: "done", state: "cancelled" }),
    /Resolve uncertain execution/,
  );
});

test("worker operations show configured slots and zero capacity as draining", async () => {
  const elements = Object.fromEntries(
    ["workerRoster", "workerCapacitySummary", "workerList"].map((id) => [
      id,
      { innerHTML: "" },
    ]),
  );
  const context = vm.createContext({
    document: {
      addEventListener() {},
      getElementById: (id) => elements[id],
    },
    loadWorkers() {},
    attemptRowMarkup() {},
    renderRunOperations() {},
    showWbTab() {},
    api: async () => ({
      serverTime: "2026-01-01T00:00:00Z",
      registeredWorkers: [
        {
          workerId: "accepting",
          capacity: 4,
          lastSeen: "2026-01-01T00:00:00Z",
        },
        { workerId: "paused", capacity: 0, lastSeen: "2026-01-01T00:00:00Z" },
      ],
      workers: [
        {
          workerId: "paused",
          runId: "finishing",
          activationId: "work",
          leaseUntil: "2026-01-01T00:01:00Z",
          generation: 1,
          state: "active",
        },
      ],
    }),
    replaceHTMLPreservingDetails: (el, html) => (el.innerHTML = html),
    stateClass: String,
    esc: String,
    date: String,
    fmtDuration: String,
  });
  vm.runInContext(hub, context);
  await vm.runInContext("loadWorkers()", context);
  assert.match(elements.workerRoster.innerHTML, /Occupied \/ configured slots/);
  assert.match(
    elements.workerRoster.innerHTML,
    /accepting.*state idle.*0 \/ 4/,
  );
  assert.match(
    elements.workerRoster.innerHTML,
    /paused.*state draining.*1 \/ 0/,
  );
  assert.match(elements.workerCapacitySummary.innerHTML, /0 \/ 4/);
});

test("evidence import recognizes actual evaluation and raw benchmark bundles", () => {
  const report = { manifest: { datasetId: "typed" }, results: [] };
  assert.equal(helpers.evidenceDocument([report]).kind, "evaluation");
  const raw = helpers.evidenceDocument([
    { suite: "muxlio-reference/v1", schemaVersion: 1 },
    { schemaVersion: 1, case: "generic", correct: false },
  ]);
  assert.equal(raw.kind, "benchmark");
  assert.equal(raw.document.observations[0].correct, false);
  assert.throws(() => helpers.evidenceDocument([{ random: "file" }]));
});
test("evaluation views retain zero scores and unavailable usage without claiming missing cases", () => {
  const report = {
    name: "<script>bad</script>",
    kind: "evaluation",
    document: {
      manifest: { datasetId: "cases", versions: [{ id: "v1" }, { id: "v2" }] },
      results: [
        {
          versionId: "v1",
          caseId: "<img>",
          durationMs: 0,
          execution: {
            state: "succeeded",
            success: true,
            accounting: {
              tokens: { knownSubtotal: "0", completeness: "partial" },
              costs: [],
              credits: [],
            },
          },
          scores: { quality: { value: 0 } },
        },
      ],
    },
  };
  const html = helpers.evidenceMarkup(report);
  assert.match(html, /quality: 0/);
  assert.match(html, /Tokens: 0 · partial/);
  assert.match(html, /Expected case identities are not included/);
  assert.match(html, /v2/);
  assert.doesNotMatch(html, /<script>|<img>|NaN|undefined/);
});
test("benchmark views retain failed and uncollected repetitions", () => {
  const report = {
    name: "recorded",
    kind: "benchmark",
    document: {
      manifest: {
        suite: "muxlio-reference/v1",
        cases: ["generic"],
        repetitions: 3,
      },
      observations: [
        { case: "generic", repetition: 1, correct: false, wallTimeMs: 3600000 },
      ],
    },
  };
  const html = helpers.evidenceMarkup(report);
  assert.match(html, /0 \/ 1/);
  assert.match(html, /Missing cases<\/span><strong>2<\/strong>/);
  assert.match(html, /1h 0m/);
  assert.match(
    helpers.hubAccounting({
      tokens: { knownSubtotal: null, completeness: "unknown" },
      costs: [
        {
          currency: "USD",
          basis: "api-equivalent",
          value: { knownSubtotal: "0", completeness: "partial" },
        },
      ],
    }),
    /USD · API-equivalent estimate: 0 · partial/,
  );
});
test("large imported result collections render bounded pages", () => {
  const rows = Array.from({ length: 125 }, (_, n) => n);
  let count;
  const html = helpers.hubClientPage("results", rows, (items) => {
    count = items.length;
    return items.join(",");
  });
  assert.equal(count, 50);
  assert.match(html, /data-offset="50"/);
  assert.doesNotMatch(html, /,124/);
});
