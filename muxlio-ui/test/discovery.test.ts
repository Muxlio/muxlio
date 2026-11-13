import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import { test } from "node:test";

test("run discovery restores shareable workspace filters and preserves paging", async () => {
  const source = readFileSync(
    new URL("./.generated/core.js", import.meta.url),
    "utf8",
  );
  const controls = new Map<string, { value: string }>();
  for (const id of [
    "search",
    "sortOrder",
    "stateFilter",
    "workspaceFilter",
    "digestFilter",
    "callerFilter",
    "createdAfterFilter",
    "createdBeforeFilter",
  ])
    controls.set(id, { value: "" });
  const calls: string[] = [];
  const routes: string[] = [];
  const context = vm.createContext({
    URLSearchParams,
    history: {
      replaceState: (_state: unknown, _title: string, path: string) =>
        routes.push(path),
    },
    document: { addEventListener() {} },
    location: {
      pathname: "/runs",
      search:
        "?state=running&workspaceId=project%20%26%20one&planDigest=sha256%3Aone&callerId=me&createdAfter=2026-01-02T00%3A00%3A00Z",
    },
    get: (id: string) => controls.get(id),
    api: async (path: string) => {
      calls.push(path);
      return { runs: [], total: 0 };
    },
  });
  vm.runInContext(source, context);
  context.api = async (path: string) => {
    calls.push(path);
    return { runs: [], total: 0 };
  };
  context.restoreRunDiscoveryFilters();
  assert.equal(controls.get("workspaceFilter")?.value, "project & one");
  assert.equal(controls.get("callerFilter")?.value, "me");
  vm.runInContext("runsPage=2", context);
  await context.fetchRuns();
  const query = new URL(calls[0], "http://coordinator").searchParams;
  assert.equal(query.get("state"), "running");
  assert.equal(query.get("workspaceId"), "project & one");
  assert.equal(query.get("planDigest"), "sha256:one");
  assert.equal(query.get("callerId"), "me");
  assert.equal(query.get("createdAfter"), "2026-01-02T00:00:00.000Z");
  assert.equal(query.get("offset"), "25");
  controls.get("workspaceFilter")!.value = "other";
  await context.fetchRuns();
  assert.equal(
    new URL(calls[1], "http://coordinator").searchParams.get("workspaceId"),
    "other",
  );
  controls.get("workspaceFilter")!.value = "";
  assert.equal(
    new URLSearchParams(context.runDiscoveryQuery()).has("workspaceId"),
    false,
  );
  controls.get("search")!.value = "  workflow name  ";
  assert.equal(
    new URLSearchParams(context.runDiscoveryQuery()).get("q"),
    "workflow name",
  );
  context.loadRuns = () => {};
  context.updateRunDiscoveryFilters();
  assert.equal(vm.runInContext("runsPage", context), 1);
  const shared = new URL(routes[0], "http://coordinator").searchParams;
  assert.equal(shared.get("q"), "workflow name");
  assert.equal(shared.get("planDigest"), "sha256:one");
  assert.equal(shared.has("workspaceId"), false);
  // A shared URL or browser navigation selects a new result set.
  vm.runInContext("runsPage=4", context);
  context.restoreRunDiscoveryFilters();
  assert.equal(vm.runInContext("runsPage", context), 1);
});
