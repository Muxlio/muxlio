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
const wb = readFileSync(
  new URL("./.generated/workbench.js", import.meta.url),
  "utf8",
);
const esc = (s) =>
  String(s ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
let now = Date.parse("2026-06-01T12:00:00Z");
class Clock extends Date {
  constructor(...args) {
    super(args.length ? args[0] : now);
  }
  static now() {
    return now;
  }
}
const {
  buildWbActivityMarkup,
  materializedStateCounts,
  wbActivityStripSummary,
} = vm.runInNewContext(
  `${format}\n${usage}\n${wb};({buildWbActivityMarkup,materializedStateCounts,wbActivityStripSummary})`,
  {
    Date: Clock,
    esc,
    document: { addEventListener() {} },
    selectedActivation: null,
    runAttempts: [],
    wbActivityAttempts: [],
    wbInputRequestSummary: [],
  },
);

test("materialized counts avoid percentage framing", () => {
  const counts = materializedStateCounts([
    { state: "succeeded" },
    { state: "running" },
    { state: "queued" },
  ]);
  const { html } = buildWbActivityMarkup(
    { id: "run", state: "running", overview: { activations: 3, completed: 1 } },
    [
      { id: "a", nodeId: "task.a", state: "running" },
      {
        id: "b",
        nodeId: "each__0",
        state: "queued",
        queueReason: "Waiting for capacity",
      },
    ],
    [
      {
        activationId: "a",
        state: "running",
        startedAt: "2026-06-01T11:59:00Z",
        retryNumber: 0,
      },
    ],
  );
  assert.match(html, /class="wb-activity-details"[^>]*>/);
  assert.doesNotMatch(
    html,
    /3 materialized|materialized nodes|<details[^>]*\bopen\b/,
  );
  assert.match(html, /wb-activity-strip-summary/);
  assert.match(html, /1 running · 1 waiting/);
  assert.doesNotMatch(html, /\b\d{1,3}%\b|estimated time remaining/i);
  assert.match(html, /Waiting for capacity/);
  assert.equal(counts.succeeded, 1);
  assert.equal(counts.running, 1);
});

test("attention surfaces reconciliation, input, failure, and detached work", () => {
  const acts = [
    { id: "bad", nodeId: "verify", state: "failed", error: "step broke" },
    { id: "run-node", nodeId: "work", state: "running" },
  ];
  const attempts = [
    {
      id: "held",
      activationId: "run-node",
      state: "cancelled",
      awaitReconciliationReason: "Verify remote outcome",
      startedAt: "2026-06-01T11:58:00Z",
      finishedAt: "2026-06-01T11:59:30Z",
    },
  ];
  const requests = [
    {
      requestId: "req-1",
      name: "Approve",
      state: "pending",
      activationId: "run-node",
    },
  ];
  const { html, visible } = buildWbActivityMarkup(
    {
      id: "run",
      state: "running",
      overview: {
        activations: 2,
        heldAttempts: 2,
        attemptStates: { detached: 1 },
      },
    },
    acts,
    attempts,
    requests,
  );
  assert.ok(visible);
  assert.match(html, /Resolve uncertain execution/);
  assert.match(html, /data-ops-action="resolve"/);
  assert.match(html, /Detached remote operations/);
  assert.match(html, /Respond/);
  assert.match(html, /step broke/);
  assert.match(html, /Held 1m 30s/);
  assert.match(html, /Live transcript/);
  assert.doesNotMatch(html, /<script>/);
});

test("terminal succeeded run shows honest empty state", () => {
  const { html, visible, announce } = buildWbActivityMarkup(
    {
      id: "run",
      state: "succeeded",
      overview: { activations: 2, completed: 2 },
    },
    [
      { id: "a", nodeId: "a", state: "succeeded" },
      { id: "b", nodeId: "b", state: "succeeded" },
    ],
    [],
    [],
  );
  assert.ok(visible);
  assert.match(html, /materialized nodes only/);
  assert.match(announce, /succeeded/);
});

test("paused and waiting runs expose elapsed wait when timestamps exist", () => {
  const paused = buildWbActivityMarkup(
    {
      id: "run",
      state: "paused",
      createdAt: "2026-06-01T11:00:00Z",
      updatedAt: "2026-06-01T11:30:00Z",
      overview: { activations: 0 },
    },
    [],
    [],
    [],
  );
  assert.match(paused.html, /Paused 30m 0s/);
  assert.match(paused.html, /Resume run/);
  const waiting = buildWbActivityMarkup(
    {
      id: "run",
      state: "waiting",
      createdAt: "2026-06-01T11:00:00Z",
      updatedAt: "2026-06-01T11:55:00Z",
      overview: { activations: 0 },
    },
    [],
    [],
    [],
  );
  assert.match(waiting.html, /Waiting 5m 0s/);
});

// ── regression: waiting and paused activations use correct badge text ─────
test("waiting activation shows Waiting badge not Queued", () => {
  const { html } = buildWbActivityMarkup(
    { id: "run", state: "running", overview: { activations: 1 } },
    [{ id: "a1", nodeId: "step.work", state: "waiting" }],
    [],
    [],
  );
  assert.doesNotMatch(
    html,
    />Queued<\/span>/,
    "waiting act must not render as Queued",
  );
  assert.match(
    html,
    />Waiting<\/span>/,
    "waiting act must render Waiting badge",
  );
  assert.match(
    html,
    /wb-activity-item--waiting/,
    "waiting act must carry --waiting tone class",
  );
});

test("paused activation shows Paused badge not Queued", () => {
  const { html } = buildWbActivityMarkup(
    { id: "run", state: "running", overview: { activations: 1 } },
    [{ id: "a1", nodeId: "step.work", state: "paused" }],
    [],
    [],
  );
  assert.doesNotMatch(
    html,
    />Queued<\/span>/,
    "paused act must not render as Queued",
  );
  assert.match(html, />Paused<\/span>/, "paused act must render Paused badge");
  assert.match(
    html,
    /wb-activity-item--paused/,
    "paused act must carry --paused tone class",
  );
  assert.match(
    html,
    /class="state paused"/,
    "paused act must have paused CSS state class",
  );
});

// ── regression: running/waiting/paused/queued activations get distinct badges
test("all in-flight activation states render distinct badges", () => {
  const { html } = buildWbActivityMarkup(
    { id: "run", state: "running", overview: { activations: 3 } },
    [
      { id: "a1", nodeId: "step.run", state: "running" },
      { id: "a2", nodeId: "step.wait", state: "waiting" },
      { id: "a3", nodeId: "step.pause", state: "paused" },
      { id: "a4", nodeId: "step.queue", state: "queued" },
    ],
    [],
    [],
  );
  const runningCount = (html.match(/>Running<\/span>/g) || []).length;
  const waitingCount = (html.match(/>Waiting<\/span>/g) || []).length;
  const pausedCount = (html.match(/>Paused<\/span>/g) || []).length;
  const queuedCount = (html.match(/>Queued<\/span>/g) || []).length;
  assert.equal(runningCount, 1, "exactly one Running badge");
  assert.equal(waitingCount, 1, "exactly one Waiting badge");
  assert.equal(pausedCount, 1, "exactly one Paused badge");
  assert.equal(queuedCount, 1, "exactly one Queued badge");
});

// ── regression: activationActivityElapsed uses nowIso for paused/waiting ──
test("elapsed timing uses nowIso for paused and waiting attempts", () => {
  const nowIso = "2026-06-01T12:00:00Z";

  // paused attempt with no finishedAt — must use nowIso
  const { html: hp } = buildWbActivityMarkup(
    { id: "run", state: "running", overview: { activations: 1 } },
    [{ id: "a1", nodeId: "step.work", state: "paused" }],
    [
      {
        activationId: "a1",
        state: "paused",
        startedAt: "2026-06-01T11:59:00Z",
        retryNumber: 0,
        finishedAt: null,
      },
    ],
    [],
    nowIso,
  );
  assert.match(
    hp,
    /Paused 1m 0s/,
    "paused attempt elapsed should reference nowIso",
  );

  // waiting attempt with no finishedAt — must use nowIso
  const { html: hw } = buildWbActivityMarkup(
    { id: "run", state: "running", overview: { activations: 1 } },
    [{ id: "a2", nodeId: "step.work", state: "waiting" }],
    [
      {
        activationId: "a2",
        state: "waiting",
        startedAt: "2026-06-01T11:58:00Z",
        retryNumber: 0,
        finishedAt: null,
      },
    ],
    [],
    nowIso,
  );
  assert.match(
    hw,
    /Waiting 2m 0s/,
    "waiting attempt elapsed should reference nowIso",
  );
});

// ── regression: queued activations never duplicate from inFlight set ──────
test("queued activation appears exactly once and inFlight states do not leak", () => {
  const { html } = buildWbActivityMarkup(
    { id: "run", state: "running", overview: { activations: 2 } },
    [
      { id: "a1", nodeId: "step.run", state: "running" },
      {
        id: "a2",
        nodeId: "step.queue",
        state: "queued",
        queueReason: "Waiting for a worker",
      },
    ],
    [],
    [],
  );
  const queuedCount = (html.match(/>Queued<\/span>/g) || []).length;
  assert.equal(queuedCount, 1, "queued activation must appear exactly once");
  assert.match(html, /Waiting for a worker/, "queued reason must be shown");
});

test("collapsed strip summarizes actionable activity without succeeded totals", () => {
  const succeeded = buildWbActivityMarkup(
    {
      id: "run",
      state: "succeeded",
      overview: { activations: 4, completed: 4 },
    },
    [{ id: "a", nodeId: "a", state: "succeeded" }],
    [],
    [],
  );
  assert.match(succeeded.html, /No actionable activity/);
  assert.doesNotMatch(succeeded.html, /4 materialized|succeeded/);

  const active = buildWbActivityMarkup(
    {
      id: "run",
      state: "running",
      overview: {
        activations: 2,
        heldAttempts: 1,
        attemptStates: { detached: 1 },
      },
    },
    [
      { id: "bad", nodeId: "verify", state: "failed", error: "nope" },
      { id: "run-node", nodeId: "work", state: "running" },
    ],
    [
      {
        id: "held",
        activationId: "run-node",
        state: "cancelled",
        awaitReconciliationReason: "Verify remote outcome",
        startedAt: "2026-06-01T11:58:00Z",
        finishedAt: "2026-06-01T11:59:30Z",
      },
    ],
    [
      {
        requestId: "req-1",
        name: "Approve",
        state: "pending",
        activationId: "run-node",
      },
    ],
  );
  assert.match(active.html, /1 running · \d+ need attention/);
  assert.equal(wbActivityStripSummary([]), "No actionable activity");

  const waiting = buildWbActivityMarkup(
    { id: "run", state: "running" },
    [
      { id: "queued", nodeId: "queued", state: "queued" },
      { id: "waiting", nodeId: "waiting", state: "waiting" },
    ],
    [],
    [],
  );
  assert.equal(waiting.announce, "Current activity. 2 waiting.");
});

test("paused activations stay distinct from waiting and run status", () => {
  const pausedActs = [
    { id: "p1", nodeId: "one", state: "paused" },
    { id: "p2", nodeId: "two", state: "paused" },
  ];
  const paused = buildWbActivityMarkup(
    { id: "run", state: "paused" },
    pausedActs,
    [],
    [],
  );
  assert.match(paused.html, /Run paused · 2 paused/);
  assert.doesNotMatch(
    paused.html,
    /3 paused|[23] waiting|<details[^>]*\bopen\b/,
  );
  assert.equal(paused.announce, "Current activity. Run paused. 2 paused.");
  const mixed = buildWbActivityMarkup(
    { id: "run", state: "running" },
    [
      ...pausedActs,
      { id: "r", nodeId: "work", state: "running" },
      { id: "w", nodeId: "wait", state: "waiting" },
      { id: "q", nodeId: "queue", state: "queued" },
      { id: "f", nodeId: "broken", state: "failed" },
    ],
    [],
    [],
  );
  assert.match(
    mixed.html,
    /1 running · 2 waiting · 2 paused · 1 needs attention/,
  );
  assert.equal(
    mixed.announce,
    "Current activity. 1 running. 2 waiting. 2 paused. 1 needs attention.",
  );
  for (const state of ["created", "waiting"]) {
    const result = buildWbActivityMarkup({ id: "run", state }, [], [], []);
    assert.match(
      result.html,
      new RegExp(`wb-activity-strip-summary subtle">Run ${state}<`),
    );
    assert.equal(result.announce, `Current activity. Run ${state}.`);
  }
});

test("live announcements give context without repeating unchanged activity", () => {
  const panel = { dataset: {}, innerHTML: "" };
  const announcements = [];
  const announcer = {
    set textContent(value) {
      announcements.push(value);
    },
  };
  const { renderWbActivity } = vm.runInNewContext(
    `${format}\n${usage}\n${wb};({renderWbActivity})`,
    {
      Date: Clock,
      esc,
      document: {
        addEventListener() {},
        getElementById: (id) =>
          id === "wbActivity"
            ? panel
            : id === "wbActivityAnnounce"
              ? announcer
              : null,
      },
    },
  );
  const run = { id: "run", state: "running" };
  renderWbActivity(run, [
    { id: "a", nodeId: "work", state: "running" },
    { id: "b", nodeId: "queue", state: "queued" },
  ]);
  assert.deepEqual(announcements, ["Current activity. 1 running. 1 waiting."]);
  renderWbActivity(run, [
    { id: "a", nodeId: "work", state: "running" },
    {
      id: "b",
      nodeId: "queue",
      state: "queued",
      queueReason: "Capacity changed",
    },
  ]);
  assert.equal(
    announcements.length,
    1,
    "detail changes must not repeat an unchanged status",
  );
  renderWbActivity({ ...run, state: "paused" }, [
    { id: "a", nodeId: "work", state: "paused" },
    { id: "b", nodeId: "queue", state: "paused" },
  ]);
  assert.equal(announcements.at(-1), "Current activity. Run paused. 2 paused.");
  renderWbActivity({ ...run, state: "succeeded" }, [
    { id: "a", nodeId: "work", state: "succeeded" },
  ]);
  assert.equal(announcements.at(-1), "Run succeeded. No actionable activity.");
  renderWbActivity({ id: "other", state: "running" }, [
    { id: "a", nodeId: "work", state: "running" },
  ]);
  assert.equal(announcements.at(-1), "Current activity. 1 running.");
});
