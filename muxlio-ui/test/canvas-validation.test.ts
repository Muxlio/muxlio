import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import test from "node:test";

const schema = JSON.parse(
  readFileSync(
    new URL(
      "../../muxlio-core/internal/canvas/schema/v1.json",
      import.meta.url,
    ),
    "utf8",
  ),
);
const source = readFileSync(
  new URL("./.generated/canvas/validate.js", import.meta.url),
  "utf8",
);
const { validateCanvasDocument, readCanvasText } = vm.runInNewContext(
  source + "\n;({validateCanvasDocument,readCanvasText})",
  {
    canvasSchema: schema,
    TextEncoder,
    TextDecoder,
  },
);
const leaf = { type: "text", id: "text", text: "Hello" };
const doc = (content: any = leaf) => ({
  version: 1,
  title: "Example",
  content,
});

test("Canvas viewer validates the canonical schema and semantic references", () => {
  validateCanvasDocument(doc());
  validateCanvasDocument(
    doc({ type: "keyValue", id: "flag", label: "Enabled", value: false }),
  );
  validateCanvasDocument({
    ...doc({
      type: "table",
      id: "table",
      data: { source: "data", filter: { field: "x", equalsState: "query" } },
      columns: [{ field: "x", label: "X" }],
    }),
    sources: { data: { value: [] } },
    controls: { query: { type: "search", id: "query", initial: "" } },
  });
  const invalid = [
    { ...doc(), title: "" },
    doc({ ...leaf, type: "script" }),
    doc({ ...leaf, onclick: "alert(1)" }),
    doc({ type: "grid", id: "grid", columns: 13, children: [] }),
    doc({ type: "stack", id: "root", children: [leaf, leaf] }),
    doc({
      type: "table",
      id: "table",
      data: { source: "missing" },
      columns: [{ field: "x", label: "X" }],
    }),
    { ...doc(), sources: { data: { value: JSON.parse('{"__proto__":1}') } } },
    doc({ type: "table", id: "table", data: [], columns: [] }),
    { ...doc(), sources: { data: { value: [], artifactPort: "metrics" } } },
  ];
  for (const value of invalid)
    assert.throws(() => validateCanvasDocument(value), /Invalid Canvas/);
});

test("Canvas viewer bounds component count, depth, inline bytes and JSON nesting", () => {
  const children = Array.from({ length: 499 }, (_, i) => ({
    ...leaf,
    id: "n" + i,
  }));
  validateCanvasDocument(doc({ type: "stack", id: "root", children }));
  assert.throws(
    () =>
      validateCanvasDocument(
        doc({
          type: "stack",
          id: "root",
          children: [...children, { ...leaf, id: "overflow" }],
        }),
      ),
    /count/,
  );
  let content: any = leaf;
  for (let i = 0; i < 16; i++)
    content = { type: "stack", id: "depth" + i, children: [content] };
  assert.throws(() => validateCanvasDocument(doc(content)), /depth/);
  assert.throws(
    () =>
      validateCanvasDocument({
        ...doc(),
        sources: { data: { value: "é".repeat(140000) } },
      }),
    /256 KiB/,
  );
  let value: any = 0;
  for (let i = 0; i < 130; i++) value = { nested: value };
  assert.throws(
    () => validateCanvasDocument({ ...doc(), sources: { data: { value } } }),
    /nesting/,
  );
});

test("Canvas downloads enforce actual UTF-8 bytes without trusting Content-Length", async () => {
  assert.equal(await readCanvasText(new Response("hello"), 5), "hello");
  await assert.rejects(readCanvasText(new Response("ééé"), 5), /byte limit/);
  await assert.rejects(
    readCanvasText(
      new Response("hello", { headers: { "Content-Length": "1000" } }),
      5,
    ),
    /byte limit/,
  );
  let cancelled = false;
  const response = new Response(
    new ReadableStream({
      start(controller) {
        controller.enqueue(new TextEncoder().encode("too large"));
      },
      cancel() {
        cancelled = true;
      },
    }),
  );
  await assert.rejects(readCanvasText(response, 5), /byte limit/);
  assert.equal(cancelled, true);
});
