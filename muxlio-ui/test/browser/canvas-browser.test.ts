import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

const { chromium } = await import(
  process.env.MUXLIO_BROWSER_MODULE || "playwright"
);
const generated = (name: string) =>
  readFileSync(new URL(`../.generated/${name}.js`, import.meta.url), "utf8");
const schema = JSON.parse(
  readFileSync(
    new URL(
      "../../../muxlio-core/internal/canvas/schema/v1.json",
      import.meta.url,
    ),
    "utf8",
  ),
);
const digest = (suffix: string) => "sha256:" + suffix.repeat(64);
const document = {
  version: 1,
  title: "Research dashboard",
  sources: { metrics: { artifactPort: "metrics" } },
  content: {
    type: "stack",
    id: "root",
    children: [
      { type: "heading", id: "heading", level: 1, text: "Research dashboard" },
      { type: "search", id: "query", label: "Question", initial: "" },
      {
        type: "table",
        id: "questions",
        data: {
          source: "metrics",
          pointer: "/questions",
          filter: { field: "text", equalsState: "query" },
        },
        columns: [{ field: "text", label: "Question" }],
      },
      {
        type: "artifactLink",
        id: "report-link",
        port: "report",
        label: "Full research report",
      },
    ],
  },
};
const outputs = {
  canvas: {
    digest: digest("a"),
    mime: "application/vnd.muxlio.canvas+json",
    path: "report.canvas.json",
  },
  metrics: {
    digest: digest("b"),
    mime: "application/json",
    path: "metrics.json",
  },
  report: { digest: digest("c"), mime: "text/markdown", path: "report.md" },
};

test("Canvas viewer loads real API shapes and preserves working interactions", async (t) => {
  const browser = await chromium.launch({
    headless: true,
    channel: process.env.MUXLIO_BROWSER_CHANNEL || "chrome",
  });
  t.after(() => browser.close());
  const makePage = async (config: Record<string, any> = {}) => {
    const page = await browser.newPage();
    page.setDefaultTimeout(4000);
    t.after(() => page.close());
    await page.setContent('<section id="canvasView"></section>');
    await page.addScriptTag({
      content:
        generated("components") +
        "\n" +
        generated("markdown") +
        "\n" +
        generated("json") +
        "\n" +
        `var canvasSchema=${JSON.stringify(schema)}; var requests=[];
       const fixture=${JSON.stringify({ outputs, document, ...config })};
       async function api(path,opts){requests.push({path,opts});
         if(path.includes('/activations/'))return {id:'act',outputs:fixture.outputs};
         if(path.includes('/snapshots?'))return fixture.snapshotPages ? fixture.snapshotPages[path.includes('after=10')?1:0] : {snapshots:[]};
         if(path==='/v1/retention')return {updated:true};
         throw Error('Unexpected API '+path);
       }
       async function uiFetch(path){requests.push({path}); const digest=decodeURIComponent(path.split('/').at(-1));
         if(digest==='${digest("a")}')return new Response(fixture.raw || JSON.stringify(fixture.document));
         if(digest==='${digest("b")}')return new Response(JSON.stringify({questions:[{text:'Alpha'},{text:'Beta'}]}),{headers:{'content-type':'application/json'}});
         if(digest==='${digest("c")}')return new Response('# Full report\\n\\nResearch evidence.');
         if(digest==='${digest("d")}')return new Response(JSON.stringify({...fixture.document,title:'Draft dashboard'}));
         throw Error('Unexpected artifact '+path);
       }
       function message(text,error){globalThis.lastMessage={text,error};}
       ` +
        [
          "types",
          "source",
          "validate",
          "interactions",
          "registry",
          "render",
          "view",
        ]
          .map((n) => generated("canvas/" + n))
          .join("\n"),
    });
    return page;
  };

  await t.test(
    "committed outputs and sibling sources render without the graph endpoint",
    async () => {
      const page = await makePage();
      await page.evaluate("loadCanvasPage('run','act','canvas')");
      assert.equal(
        await page.locator("#canvasRoot h1").textContent(),
        "Research dashboard",
      );
      assert.match(
        await page.locator("#questions").innerText(),
        /Alpha[\s\S]*Beta/,
      );
      assert.equal(
        await page.evaluate("requests.some(r=>r.path.endsWith('/graph'))"),
        false,
      );
      assert.equal(
        await page.locator("#report-link a").getAttribute("href"),
        "/artifacts/view/run/act/report",
      );
      await page.locator("#canvasPinBtn").click();
      assert.deepEqual(
        await page.evaluate(
          "JSON.parse(requests.find(r=>r.path==='/v1/retention').opts.body)",
        ),
        { kind: "artifact", id: digest("a"), pinned: true },
      );
      assert.equal(await page.evaluate("lastMessage.text"), "Artifact pinned.");

      const search = page.locator("#ctrl-query input");
      await search.focus();
      await page.keyboard.type("Alpha");
      assert.equal(await search.inputValue(), "Alpha");
      assert.equal(
        await search.evaluate((el) => el === el.ownerDocument.activeElement),
        true,
      );
      assert.equal(
        await search.evaluate((el: HTMLInputElement) => el.selectionStart),
        5,
      );
      assert.match(await page.locator("#questions").innerText(), /Alpha/);
      assert.doesNotMatch(await page.locator("#questions").innerText(), /Beta/);
      await page.keyboard.press("ArrowLeft");
      await page.keyboard.type("X");
      assert.equal(await search.inputValue(), "AlphXa");
      assert.equal(
        await search.evaluate((el: HTMLInputElement) => el.selectionStart),
        5,
      );
    },
  );

  await t.test(
    "Markdown and JSON siblings use ordinary artifact previews",
    async () => {
      const page = await makePage();
      await page.evaluate("loadCanvasPage('run','act','report')");
      assert.equal(
        await page.locator(".panel-body h1").textContent(),
        "Full report",
      );
      assert.match(
        await page.locator("#canvasView").innerText(),
        /Research evidence/,
      );
      await page.evaluate("loadCanvasPage('run','act','metrics')");
      assert.match(await page.locator("#canvasView").innerText(), /questions/);
      assert.doesNotMatch(
        await page.locator("#canvasView").innerText(),
        /Unsupported Version|Malformed Canvas/,
      );
    },
  );

  await t.test(
    "drafts and selected revisions unwrap snapshot events across pages",
    async () => {
      const snapshot = {
        seq: 20,
        subjectId: "attempt-2",
        payload: {
          snapshot: {
            name: "canvas",
            digest: digest("d"),
            mime: outputs.canvas.mime,
          },
        },
      };
      const page = await makePage({
        outputs: { metrics: outputs.metrics },
        snapshotPages: [
          {
            snapshots: [
              {
                seq: 10,
                payload: { snapshot: { name: "other", digest: digest("c") } },
              },
            ],
            nextAfter: 10,
            hasMore: true,
          },
          { snapshots: [snapshot], nextAfter: 20, hasMore: false },
        ],
      });
      await page.evaluate("loadCanvasPage('run','act','canvas')");
      assert.equal(
        await page.locator(".canvas-doc-title").textContent(),
        "Draft dashboard",
      );
      assert.equal(
        await page.locator("#canvasRevisionSelect").inputValue(),
        "seq-20",
      );
      await page.evaluate(
        "loadCanvasPage('run','act','canvas','attempt-2',20)",
      );
      assert.equal(
        await page.locator("#canvasRevisionSelect").inputValue(),
        "seq-20",
      );
      assert.equal(
        await page.evaluate(
          "requests.some(r=>r.path.includes('attemptId=attempt-2'))",
        ),
        true,
      );
      await page.evaluate("loadCanvasPage('run','act','canvas',undefined,99)");
      assert.match(
        await page.locator("#canvasView").innerText(),
        /Requested snapshot is unavailable/,
      );
    },
  );

  await t.test(
    "invalid or oversized Canvas is rejected before rendering or fetching sources",
    async () => {
      const invalid = await makePage({
        document: { ...document, content: { type: "script", id: "bad" } },
      });
      await invalid.evaluate("loadCanvasPage('run','act','canvas')");
      assert.match(
        await invalid.locator("#canvasView").innerText(),
        /Invalid Canvas/,
      );
      assert.equal(
        await invalid.evaluate(
          `requests.some(r=>r.path.includes(encodeURIComponent('${digest("b")}')))`,
        ),
        false,
      );
      const large = await makePage({ raw: "é".repeat(600000) });
      await large.evaluate("loadCanvasPage('run','act','canvas')");
      assert.match(
        await large.locator("#canvasView").innerText(),
        /byte limit/,
      );
    },
  );
});
