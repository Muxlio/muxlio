import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

const { chromium } = await import(
  process.env.MUXLIO_BROWSER_MODULE || "playwright"
);

test("activity disclosure stays compact, readable and keyboard accessible across refreshes", async () => {
  const generated = (name) =>
    readFileSync(new URL(`../.generated/${name}.js`, import.meta.url), "utf8");
  const core = generated("core");
  const disclosure = core.slice(
    core.indexOf("const disclosureState="),
    core.indexOf("function uiFetch("),
  );
  assert.ok(disclosure.includes("function replaceHTMLPreservingDetails"));
  const css = readFileSync(
    new URL("../../dist/app.css", import.meta.url),
    "utf8",
  );
  const browser = await chromium.launch({
    headless: true,
    channel: process.env.MUXLIO_BROWSER_CHANNEL || "chrome",
  });
  try {
    const page = await browser.newPage();
    for (const width of [1440, 768, 390, 320]) {
      await page.goto("about:blank");
      await page.setViewportSize({ width, height: 900 });
      await page.setContent(`<style>${css}</style>
        <div id="wbActivityAnnounce" aria-live="polite" aria-atomic="true" class="visually-hidden"></div>
        <section id="wbActivity" class="wb-activity" aria-label="Current activity and attention"></section>
        <div id="graph">Workflow graph</div>`);
      await page.addScriptTag({
        content: `let current='run'; const esc=String;
          ${disclosure}
          ${generated("format")}
          ${generated("usage-summary")}
          ${generated("workbench")}
          const fixtureRun={id:'run',state:'failed',error:'Step failed'};
          const fixtureActs=[
            {id:'r',nodeId:'running',state:'running'},
            {id:'q',nodeId:'queued',state:'queued'},
            {id:'w',nodeId:'waiting',state:'waiting'},
            {id:'p',nodeId:'paused',state:'paused'},
            {id:'f',nodeId:'failed',state:'failed',error:'Step failed'}
          ];
          renderWbActivity(fixtureRun,fixtureActs);`,
      });
      const details = page.locator(".wb-activity-details");
      const summary = details.locator("summary");
      assert.match(await details.ariaSnapshot(), /Current activity/);
      assert.equal(await details.getAttribute("open"), null);
      assert.match(
        await summary.innerText(),
        /1 running · 1 queued · 1 waiting · 1 paused · 1 needs attention/,
      );
      const layout = await page.evaluate(() => {
        const panel = document
          .getElementById("wbActivity")
          .getBoundingClientRect();
        const summary = document
          .querySelector("summary")
          .getBoundingClientRect();
        const text = document
          .querySelector(".wb-activity-strip-summary")
          .getBoundingClientRect();
        const graph = document.getElementById("graph").getBoundingClientRect();
        return {
          summaryHeight: summary.height,
          panelHeight: panel.height,
          fits: text.left >= panel.left && text.right <= panel.right,
          gap: graph.top - panel.bottom,
          overflow: document.documentElement.scrollWidth > innerWidth,
        };
      });
      assert.ok(layout.summaryHeight >= 36);
      assert.ok(layout.panelHeight <= (width > 600 ? 64 : 120));
      assert.ok(layout.fits);
      assert.equal(layout.gap, 0);
      assert.equal(layout.overflow, false);
      await summary.focus();
      await page.keyboard.press("Enter");
      assert.notEqual(await details.getAttribute("open"), null);
      await page.evaluate("renderWbActivity(fixtureRun,fixtureActs.slice(1))");
      assert.notEqual(await details.getAttribute("open"), null);
      await summary.focus();
      await page.keyboard.press("Space");
      assert.equal(await details.getAttribute("open"), null);
      await page.evaluate("renderWbActivity(fixtureRun,fixtureActs)");
      assert.equal(await details.getAttribute("open"), null);
      assert.equal(
        await page.locator("#wbActivityAnnounce").textContent(),
        "Current activity. 1 running. 1 queued. 1 waiting. 1 paused. 1 needs attention.",
      );
    }
  } finally {
    await browser.close();
  }
});
