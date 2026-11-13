import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import { test as regression } from "node:test";
regression("timing regression", async () => {
  const core = readFileSync(
    new URL("./.generated/core.js", import.meta.url),
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
  let now = Date.parse("2026-01-01T00:01:00Z");
  class Clock extends Date {
    constructor(...args) {
      super(args.length ? args[0] : now);
    }
    static now() {
      return now;
    }
  }
  const context = vm.createContext({
    Date: Clock,
    URLSearchParams,
    document: { addEventListener() {} },
    location: { pathname: "/runs", search: "" },
    currentRoute: () => "/runs",
    get: () => null,
    runAttempts: [],
  });
  vm.runInContext(
    readFileSync(new URL("./.generated/format.js", import.meta.url), "utf8") +
      usage +
      "\n" +
      core +
      "\n" +
      wb,
    context,
  );
  const run = {
    id: "r",
    state: "running",
    createdAt: "2026-01-01T00:00:00Z",
    updatedAt: "2026-01-01T00:00:10Z",
    totalExecutionTimeMs: 0,
  };
  context.run = run;
  const table = () => vm.runInContext("runTable([run])", context);
  const stats = () => vm.runInContext("buildWbStats(run,[])", context);
  assert.match(table(), /1m 0s/);
  assert.match(table(), />0 ms</, "history displays measured zero");
  assert.match(stats(), /1m 0s/);
  now += 10000;
  assert.match(
    table(),
    /1m 10s/,
    "active elapsed advances despite unchanged updatedAt",
  );
  assert.match(stats(), /1m 10s/);
  run.state = "created";
  assert.match(stats(), /1m 10s/, "queue time counts");
  run.state = "succeeded";
  assert.match(stats(), /10\.0 s/, "terminal elapsed stops");
  assert.match(table(), /10\.0 s/);
  for (const state of ["waiting", "paused"]) {
    run.state = state;
    assert.match(stats(), /1m 10s/);
  }
  assert.equal(
    context.sumExecutionTime([
      { state: "failed", executionDurationMs: 300 },
      { state: "succeeded", executionDurationMs: 500 },
    ]),
    800,
  );
  assert.equal(context.sumExecutionTime([{ executionDurationMs: 0 }]), 0);
  assert.equal(context.sumExecutionTime([{ state: "succeeded" }]), null);
  assert.equal(
    context.sumExecutionTime([
      { executionDurationMs: 30 },
      { state: "succeeded" },
    ]),
    30,
    "measured subtotal ignores attempts without a reported duration",
  );
  assert.equal(
    context.sumExecutionTime([
      { executionDurationMs: 30 },
      { state: "running" },
    ]),
    30,
    "active total uses completed measurements",
  );
  context.runAttempts = [
    { executionDurationMs: 70000 },
    { executionDurationMs: 80000 },
  ];
  assert.match(stats(), /2m 30s/, "parallel durations can exceed elapsed");
  // fetchRuns is shared by initial history, overview and periodic refresh.
  vm.runInContext(
    `api=async path=>path.startsWith('/v1/runs?')?{runs:[{id:'r'}],total:1,states:{succeeded:1}}:{overview:{executionDurationMs:0}}`,
    context,
  );
  const first = await context.fetchRuns(),
    refreshed = await context.fetchRuns();
  assert.equal(first[0].totalExecutionTimeMs, 0);
  assert.equal(refreshed[0].totalExecutionTimeMs, 0, "refresh retains totals");
  console.log("Run timing checks passed");
});
