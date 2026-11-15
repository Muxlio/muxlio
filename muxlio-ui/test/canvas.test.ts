// Tests for Canvas rendering, interaction engine, source resolution, and artifact affordances.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import test from "node:test";

// Load canvas modules on top of the shared component fixture (which includes
// all rendering helpers, templates, and markdown).
function loadCanvas() {
  const componentFixture = readFileSync(
    new URL("./.generated/components.js", import.meta.url),
    "utf8",
  );
  const markdownFixture = readFileSync(
    new URL("./.generated/markdown.js", import.meta.url),
    "utf8",
  );
  const canvasFiles = [
    "types",
    "source",
    "interactions",
    "registry",
    "render",
  ].map((name) =>
    readFileSync(
      new URL(`./.generated/canvas/${name}.js`, import.meta.url),
      "utf8",
    ),
  );
  const ctx = vm.runInNewContext(
    componentFixture +
      "\n" +
      markdownFixture +
      "\n" +
      canvasFiles.join("\n") +
      "\n;({renderCanvasNode,resolveCanvasData,evaluateJsonPointer,filterDataRows,sortDataRows,CanvasInteractionStore})",
  );
  return ctx;
}

const canvas = loadCanvas();

// ── Renderer: leaf components ───────────────────────────────────────────────

test("heading renders escaped text at the given level", () => {
  const ctx = emptyCtx();
  const html = canvas.renderCanvasNode(
    { type: "heading", id: "h1", level: 2, text: '<b>Hello</b>' },
    ctx,
  ).value;
  assert.match(html, /<h2[^>]*>/);
  assert.match(html, /&lt;b&gt;Hello&lt;\/b&gt;/);
  assert.ok(!html.includes("<b>Hello</b>"), "agent HTML must not be injected raw");
});

test("text renders with the correct variant class", () => {
  const ctx = emptyCtx();
  const html = canvas.renderCanvasNode(
    { type: "text", id: "t1", text: "Hello world", variant: "lead" },
    ctx,
  ).value;
  assert.match(html, /canvas-text--lead/);
  assert.match(html, /Hello world/);
});

test("callout renders tone class and escapes title and text", () => {
  const ctx = emptyCtx();
  const html = canvas.renderCanvasNode(
    {
      type: "callout",
      id: "c1",
      tone: "warning",
      title: "<script>bad()</script>",
      text: "Normal text",
    },
    ctx,
  ).value;
  assert.match(html, /canvas-callout--warning/);
  assert.match(html, /&lt;script&gt;bad\(\)&lt;\/script&gt;/);
  assert.ok(!html.includes("<script>"), "agent script tag must not appear raw");
});

test("code wraps text in a pre/code block and escapes it", () => {
  const ctx = emptyCtx();
  const html = canvas.renderCanvasNode(
    { type: "code", id: "code1", text: "a < b", language: "typescript" },
    ctx,
  ).value;
  assert.match(html, /<pre[^>]*>/);
  assert.match(html, /a &lt; b/);
  assert.match(html, /language-typescript/);
});

test("metric renders label and value", () => {
  const ctx = emptyCtx();
  const html = canvas.renderCanvasNode(
    { type: "metric", id: "m1", label: "Success rate", value: "99.1%" },
    ctx,
  ).value;
  assert.match(html, /Success rate/);
  assert.match(html, /99\.1%/);
});

test("keyValue renders label and value as a definition row", () => {
  const ctx = emptyCtx();
  const html = canvas.renderCanvasNode(
    { type: "keyValue", id: "kv1", label: "Environment", value: "production" },
    ctx,
  ).value;
  assert.match(html, /Environment/);
  assert.match(html, /production/);
});

test("divider renders with spacing class", () => {
  const ctx = emptyCtx();
  const html = canvas.renderCanvasNode(
    { type: "divider", id: "d1", spacing: "spacious" },
    ctx,
  ).value;
  assert.match(html, /canvas-divider--spacious/);
});

// ── Renderer: layout components ─────────────────────────────────────────────

test("stack renders children and applies gap class", () => {
  const ctx = emptyCtx();
  const html = canvas.renderCanvasNode(
    {
      type: "stack",
      id: "s1",
      gap: "compact",
      children: [
        { type: "heading", id: "h1", level: 3, text: "Child" },
      ],
    },
    ctx,
  ).value;
  assert.match(html, /canvas-stack--compact/);
  assert.match(html, /Child/);
});

test("grid clamps columns to 1–12 and renders children", () => {
  const ctx = emptyCtx();
  const html = canvas.renderCanvasNode(
    {
      type: "grid",
      id: "g1",
      columns: 15, // above max → clamped to 12
      children: [
        { type: "metric", id: "m1", label: "Runs", value: 10 },
      ],
    },
    ctx,
  ).value;
  assert.match(html, /repeat\(12,1fr\)/);
  assert.match(html, /Runs/);
});

test("grid with auto-fit uses auto-fit repeat", () => {
  const ctx = emptyCtx();
  const html = canvas.renderCanvasNode(
    {
      type: "grid",
      id: "g2",
      columns: "auto-fit",
      children: [],
    },
    ctx,
  ).value;
  assert.match(html, /auto-fit/);
});

test("section renders open when not collapsed", () => {
  const ctx = emptyCtx();
  const html = canvas.renderCanvasNode(
    {
      type: "section",
      id: "sec1",
      title: "Details",
      children: [],
    },
    ctx,
  ).value;
  assert.match(html, /open/);
  assert.match(html, /Details/);
});

test("section renders closed when collapsed state is set", () => {
  const ctx = emptyCtx({ collapsedSections: new Set(["sec2"]) });
  const html = canvas.renderCanvasNode(
    {
      type: "section",
      id: "sec2",
      title: "Hidden",
      children: [],
    },
    ctx,
  ).value;
  assert.ok(!html.includes(' open"') && !html.includes(' open>'), "section must not be open when collapsed");
});

test("tabs renders all tab buttons and only the active panel visible", () => {
  const ctx = emptyCtx({ activeTabsState: new Map([["tabs1", "b"]]) });
  const html = canvas.renderCanvasNode(
    {
      type: "tabs",
      id: "tabs1",
      tabs: [
        { id: "a", label: "Tab A", children: [{ type: "text", id: "ta", text: "Content A" }] },
        { id: "b", label: "Tab B", children: [{ type: "text", id: "tb", text: "Content B" }] },
      ],
    },
    ctx,
  ).value;
  assert.match(html, /Tab A/);
  assert.match(html, /Tab B/);
  assert.match(html, /Content B/);
  // Panel A is hidden
  assert.match(html, /hidden/);
});

// ── Renderer: data components ────────────────────────────────────────────────

test("table renders column headers and paginated rows", () => {
  const rows = Array.from({ length: 5 }, (_, i) => ({
    name: `Row ${i}`,
    count: i * 10,
  }));
  const ctx = emptyCtx({ sources: new Map([["data", rows]]) });
  const html = canvas.renderCanvasNode(
    {
      type: "table",
      id: "tbl1",
      title: "My Table",
      data: { source: "data" },
      columns: [
        { field: "name", label: "Name", kind: "text" },
        { field: "count", label: "Count", kind: "integer" },
      ],
      sortable: true,
      pageSize: 25,
    },
    ctx,
  ).value;
  assert.match(html, /My Table/);
  assert.match(html, /Name/);
  assert.match(html, /Count/);
  assert.match(html, /Row 0/);
  assert.match(html, /Row 4/);
});

test("table shows empty state when data is unavailable", () => {
  const ctx = emptyCtx();
  const html = canvas.renderCanvasNode(
    {
      type: "table",
      id: "tbl2",
      title: "Empty",
      data: { source: "missing" },
      columns: [{ field: "x", label: "X", kind: "text" }],
    },
    ctx,
  ).value;
  assert.match(html, /not yet available|unavailable/i);
});

test("table percent column formats numbers as percentages", () => {
  const rows = [{ rate: 0.975 }];
  const ctx = emptyCtx({ sources: new Map([["d", rows]]) });
  const html = canvas.renderCanvasNode(
    {
      type: "table",
      id: "tbl3",
      data: { source: "d" },
      columns: [{ field: "rate", label: "Rate", kind: "percent" }],
    },
    ctx,
  ).value;
  assert.match(html, /97\.5%/);
});

test("table respects pageSize and renders pagination when needed", () => {
  const rows = Array.from({ length: 30 }, (_, i) => ({ n: i }));
  const ctx = emptyCtx({ sources: new Map([["rows", rows]]) });
  const html = canvas.renderCanvasNode(
    {
      type: "table",
      id: "tbl4",
      data: { source: "rows" },
      columns: [{ field: "n", label: "N", kind: "integer" }],
      pageSize: 10,
    },
    ctx,
  ).value;
  assert.match(html, /Page 1 of 3/);
});

test("barChart renders bars and shows empty state for empty data", () => {
  const data = [
    { day: "Mon", runs: 10 },
    { day: "Tue", runs: 20 },
  ];
  const ctx = emptyCtx({ sources: new Map([["obs", data]]) });
  const html = canvas.renderCanvasNode(
    {
      type: "barChart",
      id: "bc1",
      title: "Runs by Day",
      data: { source: "obs" },
      categoryField: "day",
      valueField: "runs",
    },
    ctx,
  ).value;
  assert.match(html, /Runs by Day/);
  assert.match(html, /Mon/);
  assert.match(html, /Tue/);

  // empty
  const emptyHtml = canvas.renderCanvasNode(
    {
      type: "barChart",
      id: "bc2",
      title: "Empty",
      data: { source: "missing" },
      categoryField: "day",
      valueField: "runs",
    },
    emptyCtx(),
  ).value;
  assert.match(emptyHtml, /No chart data available|not yet available/i);
});

test("barChart clamps to 2000 data points", () => {
  const data = Array.from({ length: 2500 }, (_, i) => ({ k: String(i), v: i }));
  const ctx = emptyCtx({ sources: new Map([["big", data]]) });
  const html = canvas.renderCanvasNode(
    {
      type: "barChart",
      id: "bc3",
      data: { source: "big" },
      categoryField: "k",
      valueField: "v",
    },
    ctx,
  ).value;
  // The 2001st item key "2000" must not appear
  assert.ok(!html.includes(">2000<"), "must not render beyond 2000 points");
});

test("sparkline renders bar segments for numeric series", () => {
  const ctx = emptyCtx({ sources: new Map([["trend", [5, 10, 15]]]) });
  const html = canvas.renderCanvasNode(
    {
      type: "sparkline",
      id: "sp1",
      title: "Trend",
      data: { source: "trend" },
    },
    ctx,
  ).value;
  assert.match(html, /Trend/);
  assert.match(html, /spark-col/);
});

// ── Renderer: controls ───────────────────────────────────────────────────────

test("select renders options and marks the current state value as selected", () => {
  const ctx = emptyCtx({ state: new Map([["period", "30d"]]) });
  const html = canvas.renderCanvasNode(
    {
      type: "select",
      id: "period",
      label: "Period",
      initial: "7d",
      options: [
        { value: "7d", label: "Last 7 days" },
        { value: "30d", label: "Last 30 days" },
      ],
    },
    ctx,
  ).value;
  assert.match(html, /Period/);
  assert.match(html, /Last 7 days/);
  assert.match(html, /Last 30 days/);
  // The 30d option has selected attribute
  assert.match(html, /value="30d"\s+selected/);
});

test("toggle renders a checked checkbox when state is true", () => {
  const ctx = emptyCtx({ state: new Map([["showFails", true]]) });
  const html = canvas.renderCanvasNode(
    {
      type: "toggle",
      id: "showFails",
      label: "Show failures only",
      initial: false,
    },
    ctx,
  ).value;
  assert.match(html, /checked/);
});

test("search renders with current state value in the input", () => {
  const ctx = emptyCtx({ state: new Map([["q", "hello"]]) });
  const html = canvas.renderCanvasNode(
    {
      type: "search",
      id: "q",
      label: "Search",
      placeholder: "Filter...",
    },
    ctx,
  ).value;
  assert.match(html, /value="hello"/);
  assert.match(html, /Filter\.\.\./);
});

// ── Renderer: depth limit ────────────────────────────────────────────────────

test("renderCanvasNode returns a muted notice at depth > 16", () => {
  // Build a 17-deep stack directly
  const deep: any = { type: "text", id: "leaf", text: "deep" };
  for (let i = 0; i < 17; i++) {
    deep.id = `d${i}`;
  }
  // Call at depth 17 — the guard activates at depth > 16
  const html = canvas.renderCanvasNode(
    { type: "text", id: "overflow", text: "overflow" },
    emptyCtx(),
    17,
  ).value;
  assert.match(html, /depth limit/i);
});

test("unknown component type renders a muted placeholder, not an error", () => {
  const ctx = emptyCtx();
  const html = canvas.renderCanvasNode(
    { type: "not-a-real-component", id: "u1" } as any,
    ctx,
  ).value;
  assert.match(html, /Unsupported component type/);
  assert.ok(!html.includes("<script>"), "must not inject any scripts");
});

// ── Source resolver ──────────────────────────────────────────────────────────

test("resolveCanvasData returns inline array ref unchanged", () => {
  const rows = [{ a: 1 }, { a: 2 }];
  const ctx = emptyCtx();
  const result = canvas.resolveCanvasData(rows, ctx);
  assert.deepEqual(result, rows);
});

test("resolveCanvasData navigates JSON pointers from named sources", () => {
  const data = { daily: [{ day: "Mon", runs: 10 }] };
  const ctx = emptyCtx({ sources: new Map([["obs", data]]) });
  const result = canvas.resolveCanvasData(
    { source: "obs", pointer: "/daily" },
    ctx,
  );
  assert.deepEqual(result, data.daily);
});

test("resolveCanvasData filters rows by equalsState", () => {
  const rows = [
    { period: "7d", day: "Mon", runs: 10 },
    { period: "30d", day: "Week 1", runs: 80 },
  ];
  const ctx = emptyCtx({
    sources: new Map([["obs", rows]]),
    state: new Map([["period", "7d"]]),
  });
  const result = canvas.resolveCanvasData(
    { source: "obs", filter: { field: "period", equalsState: "period" } },
    ctx,
  ) as any[];
  assert.equal(result.length, 1);
  assert.equal(result[0].day, "Mon");
});

test("evaluateJsonPointer returns undefined for prototype-traversal keys", () => {
  const obj = { a: { b: 1 } };
  assert.equal(canvas.evaluateJsonPointer(obj, "/__proto__"), undefined);
  assert.equal(canvas.evaluateJsonPointer(obj, "/constructor"), undefined);
});

test("sortDataRows sorts numbers correctly in both directions", () => {
  const rows = [{ n: 3 }, { n: 1 }, { n: 2 }];
  const asc = canvas.sortDataRows(rows, "n", "asc") as Array<{ n: number }>;
  assert.equal(asc[0].n, 1);
  assert.equal(asc[1].n, 2);
  assert.equal(asc[2].n, 3);
  const desc = canvas.sortDataRows(rows, "n", "desc") as Array<{ n: number }>;
  assert.equal(desc[0].n, 3);
  assert.equal(desc[1].n, 2);
  assert.equal(desc[2].n, 1);
});

test("filterDataRows returns all rows when equalsState key is missing from state", () => {
  const rows = [{ p: "7d" }, { p: "30d" }];
  const result = canvas.filterDataRows(
    rows,
    { field: "p", equalsState: "period" },
    new Map(), // period key absent
  );
  assert.deepEqual(result, rows);
});

// ── Interaction store ────────────────────────────────────────────────────────

test("CanvasInteractionStore initialises state from document controls", () => {
  const doc = {
    version: 1,
    title: "Test",
    content: { type: "heading", id: "h1", level: 1, text: "hi" },
    controls: {
      period: { type: "select", id: "period", label: "Period", initial: "7d", options: [] },
      showFails: { type: "toggle", id: "showFails", label: "Failures", initial: true },
    },
  };
  const store = new canvas.CanvasInteractionStore(doc);
  assert.equal(store.state.get("period"), "7d");
  assert.equal(store.state.get("showFails"), true);
});

test("CanvasInteractionStore dispatches setState and notifies listeners", () => {
  const store = new canvas.CanvasInteractionStore();
  let notified = 0;
  const unsub = store.subscribe(() => notified++);
  store.dispatch({ type: "setState", stateId: "x", value: "hello" });
  assert.equal(store.state.get("x"), "hello");
  assert.equal(notified, 1);
  unsub();
  store.dispatch({ type: "setState", stateId: "x", value: "world" });
  assert.equal(notified, 1, "unsubscribed listener must not be called");
});

test("CanvasInteractionStore sortTable toggles direction on the same field", () => {
  const store = new canvas.CanvasInteractionStore();
  store.dispatch({ type: "sortTable", componentId: "tbl", field: "name" });
  assert.equal(store.tableSort.get("tbl")!.direction, "asc");
  store.dispatch({ type: "sortTable", componentId: "tbl", field: "name" });
  assert.equal(store.tableSort.get("tbl")!.direction, "desc");
  // Different field resets to asc
  store.dispatch({ type: "sortTable", componentId: "tbl", field: "count" });
  assert.equal(store.tableSort.get("tbl")!.direction, "asc");
  assert.equal(store.tableSort.get("tbl")!.field, "count");
});

test("CanvasInteractionStore sortTable resets page to 1", () => {
  const store = new canvas.CanvasInteractionStore();
  store.dispatch({ type: "setPage", componentId: "tbl", page: 3 });
  store.dispatch({ type: "sortTable", componentId: "tbl", field: "name" });
  assert.equal(store.tablePages.get("tbl"), 1);
});

test("CanvasInteractionStore selectTab switches active tab", () => {
  const store = new canvas.CanvasInteractionStore();
  store.dispatch({ type: "selectTab", componentId: "tabs1", tabId: "b" });
  assert.equal(store.activeTabs.get("tabs1"), "b");
});

test("CanvasInteractionStore selectChartPoint deselects when same key dispatched again", () => {
  const store = new canvas.CanvasInteractionStore();
  store.dispatch({ type: "selectChartPoint", componentId: "chart1", key: "Mon" });
  assert.equal(store.selectedPoints.get("chart1"), "Mon");
  store.dispatch({ type: "selectChartPoint", componentId: "chart1", key: "Mon" });
  assert.equal(store.selectedPoints.get("chart1"), undefined);
});

test("CanvasInteractionStore toggleSection collapses and expands", () => {
  const store = new canvas.CanvasInteractionStore();
  store.dispatch({ type: "toggleSection", componentId: "sec1" });
  assert.ok(store.collapsedSections.has("sec1"));
  store.dispatch({ type: "toggleSection", componentId: "sec1" });
  assert.ok(!store.collapsedSections.has("sec1"));
});

// ── Open Canvas affordance in artifact table ──────────────────────────────────

test("renderArtifacts shows Open Canvas link for canvas MIME and plain rows otherwise", () => {
  const workspaceFixture = readFileSync(
    new URL("./.generated/workspace.js", import.meta.url),
    "utf8",
  );
  // Extract just renderArtifacts and its helpers from the workspace fixture.
  // The workspace fixture depends on many globals; we provide minimal stubs.
  const stubGlobals = `
    var renderViewTemplate=function(name,vals){return name+'|'+(vals||[]).map(v=>v&&v.value!==undefined?v.value:'').join('|');};
    var viewValue=function(v){return {value:v};};
    var renderSmallContent=function(v){return v;};
    var renderEmptyState=function(a,b){return 'empty:'+a;};
    var renderPagination=function(){return '';};
    var esc=function(v){return String(v||'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');};
    var runPager=function(){return '';};
    var allArtifacts=[];
    var artifactOffset=0;
    var artifactPageMore=false;
    var artifactQuery='';
  `;
  // Extract only the renderArtifacts function and downloadArtifact from workspace
  const src = workspaceFixture;
  const renderArtifactsStart = src.indexOf("function renderArtifacts()");
  const downloadArtifactStart = src.indexOf("async function downloadArtifact");
  // End at the next function after renderArtifacts (stops before the section handler)
  const renderArtifactsEnd = src.indexOf("\nasync function downloadArtifact");
  const renderArtifactsSrc = src.slice(renderArtifactsStart, renderArtifactsEnd);

  let renderedHtml = "";
  const stubDocument = {
    getElementById: (_id: string) => ({
      get innerHTML(): string { return renderedHtml; },
      set innerHTML(v: string) { renderedHtml = v; },
      value: "",
      querySelectorAll: () => [],
    }),
  };
  const ctx = vm.createContext({
    document: stubDocument,
    renderViewTemplate: (name: string, vals: any[]) =>
      name + "|" + vals.map((v) => (v && v.value !== undefined ? v.value : "")).join("|"),
    viewValue: (v: any) => ({ value: v }),
    renderSmallContent: (v: any) => String(v),
    renderEmptyState: (a: any) => "empty:" + a,
    renderPagination: () => "",
    esc: (v: any) => String(v || "").replace(/&/g, "&amp;").replace(/</g, "&lt;"),
    runPager: () => "",
    allArtifacts: [] as any[],
    artifactOffset: 0,
    artifactPageMore: false,
    artifactQuery: "",
  });
  vm.runInContext(renderArtifactsSrc, ctx);

  // Test: canvas MIME artifact should produce a canvas link
  ctx.allArtifacts = [
    {
      name: "report",
      mime: "application/vnd.muxlio.canvas+json",
      runId: "run-1",
      activationId: "act-1",
      nodeId: "node-compose",
      path: "report.canvas.json",
      digest: "sha256:abc123",
      workflowName: "My Workflow",
    },
  ];
  ctx.renderArtifacts();
  assert.ok(
    renderedHtml.includes("el-inner-html-btn-canvas") ||
      renderedHtml.includes("/artifacts/view/run-1/act-1/report"),
    "canvas artifact must include Open Canvas affordance",
  );

  // Test: non-canvas artifact should NOT produce a canvas link
  renderedHtml = "";
  ctx.allArtifacts = [
    {
      name: "data.json",
      mime: "application/json",
      runId: "run-1",
      activationId: "act-1",
      nodeId: "node-analyze",
      path: "data.json",
      digest: "sha256:def456",
      workflowName: "My Workflow",
    },
  ];
  ctx.renderArtifacts();
  assert.ok(
    !renderedHtml.includes("el-inner-html-btn-canvas"),
    "non-canvas artifact must not include Open Canvas affordance",
  );
});

// ── Schema/renderer parity ────────────────────────────────────────────────────

test("every node type declared in canvasSchema has a renderer in renderCanvasNode", () => {
  // The build generates .generated/canvas-schema.ts from the canonical Go schema.
  // Read it directly — the file is a single var assignment.
  const schemaSource = readFileSync(
    new URL("../.generated/canvas-schema.ts", import.meta.url),
    "utf8",
  );
  // Strip the generated comment and the TypeScript type annotation before eval.
  const stripped = schemaSource
    .replace(/^\/\*.+?\*\/\n/s, "")
    .replace(/^var canvasSchema: [^=]+=/, "var canvasSchema =");
  const schemaCtx = vm.runInNewContext(stripped + "\n;canvasSchema") as Record<string, any>;

  const nodeAnyOf: any[] = schemaCtx?.["$defs"]?.["node"]?.["anyOf"] ?? [];
  assert.ok(nodeAnyOf.length > 0, "schema must define at least one node type");

  const schemaTypes = nodeAnyOf
    .map((e: any) => e?.properties?.type?.const)
    .filter(Boolean) as string[];

  assert.ok(schemaTypes.length > 0, "schema types must be extractable");

  const ctx = emptyCtx();
  for (const type of schemaTypes) {
    const stub: any = { type, id: "parity-" + type };
    if (type === "heading")     { stub.level = 1; stub.text = "T"; }
    if (type === "text")        { stub.text = "T"; }
    if (type === "markdown")    { stub.text = "T"; }
    if (type === "code")        { stub.text = "T"; }
    if (type === "callout")     { stub.text = "T"; }
    if (type === "metric")      { stub.label = "X"; stub.value = 0; }
    if (type === "keyValue")    { stub.label = "X"; stub.value = "Y"; }
    if (type === "status")      { stub.label = "OK"; stub.state = "succeeded"; stub.tone = "green"; }
    if (type === "summary")     { stub.items = [{ label: "X", value: 1 }]; }
    if (type === "table")       { stub.columns = [{ field: "x", label: "X" }]; stub.data = { source: "missing" }; }
    if (type === "json")        { stub.data = null; }
    if (type === "barChart")    { stub.categoryField = "k"; stub.valueField = "v"; stub.data = { source: "missing" }; }
    if (type === "sparkline")   { stub.data = []; }
    if (type === "select")      { stub.label = "X"; stub.options = []; }
    if (type === "toggle")      { stub.label = "X"; }
    if (type === "artifactLink"){ stub.port = "out"; stub.label = "Link"; }
    if (["stack", "grid", "row", "section"].includes(type)) { stub.children = []; }
    if (type === "tabs")        { stub.tabs = [{ id: "a", label: "A", children: [] }]; }

    let result!: { value: string };
    assert.doesNotThrow(
      () => { result = canvas.renderCanvasNode(stub, ctx); },
      `renderCanvasNode must not throw for schema type "${type}"`,
    );
    assert.equal(typeof result.value, "string", `must return ComponentHTML for "${type}"`);
    assert.ok(!result.value.includes("<script"), `must not inject scripts for "${type}"`);
  }
});

function emptyCtx(overrides: Partial<{
  sources: Map<string, unknown>;
  state: Map<string, unknown>;
  activeTabsState: Map<string, string>;
  collapsedSections: Set<string>;
  tableSortState: Map<string, { field: string; direction: "asc" | "desc" }>;
  tablePageState: Map<string, number>;
  selectedPointState: Map<string, string>;
}> = {}): any {
  return {
    sources: overrides.sources ?? new Map(),
    state: overrides.state ?? new Map(),
    resolveData: (ref: unknown) =>
      canvas.resolveCanvasData(ref, {
        sources: overrides.sources ?? new Map(),
        state: overrides.state ?? new Map(),
        resolveData: () => undefined,
      }),
    activeTabsState: overrides.activeTabsState ?? new Map(),
    collapsedSections: overrides.collapsedSections ?? new Set(),
    tableSortState: overrides.tableSortState ?? new Map(),
    tablePageState: overrides.tablePageState ?? new Map(),
    selectedPointState: overrides.selectedPointState ?? new Map(),
  };
}
