import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { once } from "node:events";
import { createUIServer } from "../../src/server.ts";

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

test("standalone canvas URL renders and refreshes with the shared navigation", async (t) => {
  const server = createUIServer();
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  t.after(() => new Promise<void>((resolve) => server.close(() => resolve())));
  const address = server.address();
  assert.ok(address && typeof address !== "string");
  const browser = await chromium.launch({
    headless: true,
    channel: process.env.MUXLIO_BROWSER_CHANNEL || "chrome",
  });
  t.after(() => browser.close());
  const page = await browser.newPage();
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route("**/v1/**", (route) => {
    const path = new URL(route.request().url()).pathname;
    let body: unknown;
    if (path.includes("/activations/")) body = { outputs };
    else if (path.endsWith("/snapshots")) body = { snapshots: [] };
    else if (decodeURIComponent(path).endsWith(digest("a"))) body = document;
    else if (decodeURIComponent(path).endsWith(digest("b")))
      body = { questions: [{ text: "Alpha" }, { text: "Beta" }] };
    else
      return route.fulfill({
        body: "# Full research report",
        contentType: "text/markdown",
      });
    return route.fulfill({ json: body });
  });
  await page.goto(
    `http://127.0.0.1:${address.port}/artifacts/view/run%2Fa/act%3Ab/canvas`,
  );
  await page.locator("#canvasRoot h1").waitFor();
  assert.equal(
    await page.locator("#canvasRoot h1").textContent(),
    "Research dashboard",
  );
  assert.equal(await page.locator("#workspaceNavigation").isVisible(), true);
  assert.equal(
    await page.locator("#workspaceNavigation #runsNav").getAttribute("href"),
    "/runs",
  );
  assert.equal(
    await page.locator("#canvasRunLink").getAttribute("href"),
    "/runs/run%2Fa",
  );
  assert.equal(await page.locator("#canvasInspectorSlot").isVisible(), false);
  assert.equal(await page.locator("#canvasRailSlot").isVisible(), false);
  assert.equal(await page.locator("#canvasModePreview").isVisible(), false);
  assert.equal(await page.locator("#canvasModeSource").isVisible(), false);
  assert.equal(
    await page.locator("#canvasToolbarSlot #canvasPinBtn").isVisible(),
    true,
  );
  assert.equal(
    await page.locator("#canvasToolbarSlot .status-badge").count(),
    0,
  );
  await page.locator("#canvasToggleProvenanceBtn").click();
  assert.equal(await page.locator("#canvasInspectorSlot").isVisible(), true);
  assert.equal(await page.locator("#canvasRailSlot").isVisible(), true);
  assert.match(
    await page.locator("#canvasInspectorSlot").innerText(),
    /Display[\s\S]*Canvas[\s\S]*Source JSON[\s\S]*Files[\s\S]*report.canvas.json[\s\S]*Revision[\s\S]*Technical details/i,
  );
  assert.equal(
    await page.locator(".canvas-info-technical").getAttribute("open"),
    null,
  );
  await page.locator(".canvas-info-technical summary").click();
  assert.equal(await page.locator(".canvas-prov-rows").isVisible(), true);
  await page.locator("#canvasCloseInfoBtn").click();
  assert.equal(await page.locator("#canvasRailSlot").isVisible(), false);
  await page.locator("#canvasToggleProvenanceBtn").click();
  await page.locator("#canvasModeSource").click();
  assert.equal(await page.locator("#canvasSourceView").isVisible(), true);
  await page.locator("#canvasModeSource").press("ArrowLeft");
  assert.equal(await page.locator("#canvasRoot").isVisible(), true);
  assert.equal(
    await page.locator("#canvasModePreview").getAttribute("aria-selected"),
    "true",
  );
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.locator("#canvasCopyLinkBtn").click();
  assert.equal(
    await page.evaluate(() => navigator.clipboard.readText()),
    page.url(),
  );
  // Plain HTTP LAN pages have no Clipboard API; the selection fallback must copy.
  await page.evaluate(() => {
    (window as any).testClipboard = navigator.clipboard;
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: undefined,
    });
  });
  await page.locator("#canvasCopyLinkBtn").click();
  assert.equal(
    await page.evaluate(() => (window as any).testClipboard.readText()),
    page.url(),
  );
  const downloadReady = page.waitForEvent("download", { timeout: 10000 });
  await page.locator("#canvasDownloadBtn").click();
  const download = await downloadReady;
  assert.equal(download.suggestedFilename(), "research-dashboard.pdf");
  assert.ok(download.url().startsWith("data:application/pdf;base64,"));
  const pdfPath = await download.path();
  assert.ok(pdfPath);
  assert.equal(readFileSync(pdfPath).subarray(0, 5).toString(), "%PDF-");
  await page.reload();
  await page.locator("#canvasRoot h1").waitFor();
  assert.equal(await page.locator("#canvasRoot").isVisible(), true);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.locator("#canvasToggleProvenanceBtn").click();
  assert.equal(await page.locator("#canvasRailSlot").isVisible(), true);
  assert.equal(await page.locator("#workspaceNavigation").isVisible(), false);
  await page.locator("#menuToggle").click();
  assert.equal(await page.locator("#workspaceNavigation").isVisible(), true);
  assert.deepEqual(errors, []);
});

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
         if(digest==='${digest("b")}')return new Response(JSON.stringify({questions:[{text:'Alpha'},{text:'Beta'}]}),{headers:{'content-type':'text/plain; charset=utf-8'}});
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
          "export",
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
    "Markdown and JSON siblings preserve the shared header, actions, and Info panel",
    async () => {
      const page = await makePage();
      await page.evaluate("loadCanvasPage('run','act','report')");
      assert.equal(
        await page.locator("#canvasRoot .markdown-body h1").textContent(),
        "Full report",
      );
      assert.match(
        await page.locator("#canvasView").innerText(),
        /Research evidence/,
      );
      await page.evaluate("loadCanvasPage('run','act','metrics')");
      assert.equal(
        await page.locator(".canvas-doc-title").textContent(),
        "metrics.json",
      );
      assert.deepEqual(
        await page.locator(".canvas-toolbar-actions button").allTextContents(),
        ["Copy link", "Download", "Pin", "Info"],
      );
      assert.equal(
        await page.locator("#canvasInspectorSlot").isVisible(),
        false,
      );
      await page.locator("#canvasToggleProvenanceBtn").click();
      assert.equal(await page.locator("#canvasRailSlot").isVisible(), true);
      await page.locator("#canvasModeSource").click();
      assert.match(
        await page.locator("#canvasSourceView").innerText(),
        /questions/,
      );
      await page.locator("#canvasModePreview").click();
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
