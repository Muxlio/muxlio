import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import { test } from "node:test";

const { chromium } = await import(
  process.env.MUXLIO_BROWSER_MODULE || "playwright"
);

test("usage subtotals stay visible in compact desktop and phone stats", async () => {
  const source = ["format", "usage-summary", "workbench"]
    .map((name) =>
      readFileSync(
        new URL(`../.generated/${name}.js`, import.meta.url),
        "utf8",
      ),
    )
    .join("\n");
  const { buildWbStats } = vm.runInNewContext(source + ";({buildWbStats})", {
    esc: (value) => String(value ?? ""),
    document: { addEventListener() {} },
  });
  const css = readFileSync(
    new URL("../../dist/app.css", import.meta.url),
    "utf8",
  );
  const measure = (value, complete) => ({
    collectedSubtotal: value,
    knownSubtotal: value,
    effectiveTotal: complete ? value : null,
    completeness: complete ? "complete" : "partial",
  });
  const browser = await chromium.launch({
    headless: true,
    channel: process.env.MUXLIO_BROWSER_CHANNEL || "chrome",
  });
  try {
    const page = await browser.newPage();
    for (const width of [1440, 768, 390, 320]) {
      await page.setViewportSize({ width, height: 900 });
      for (const complete of [false, true]) {
        for (const accounting of [
          { tokens: measure("9223372036854775807", complete) },
          { tokens: measure("0", complete) },
          { referenceValue: measure("3.42", complete) },
          {
            credits: [
              {
                provider: "fixture",
                unit: "request-credit",
                value: measure("0.18065595854063016", complete),
              },
            ],
          },
          {
            costs: [
              {
                currency: "EUR",
                basis: "provider-metered",
                value: measure("0.000123456789", complete),
              },
            ],
          },
        ]) {
          const stats = buildWbStats(
            {
              state: "succeeded",
              createdAt: "2026-01-01T00:00:00Z",
              updatedAt: "2026-01-01T00:01:00Z",
              overview: {
                activations: 2,
                completed: 2,
                attempts: 2,
                executionDurationMs: 0,
                accounting,
              },
            },
            [],
          );
          await page.setContent(
            `<style>${css}</style><div class="stat-row wb-stats">${stats}</div>`,
          );
          const usage = page.locator(".wb-usage");
          assert.equal(
            (await usage.innerText()).includes("subtotal"),
            !complete,
          );
          assert.equal(await usage.locator(".stat-val").count(), 1);
          assert.equal(await usage.locator(".stat-label").count(), 1);
          const layout = await page.evaluate(() => {
            const row = document
              .querySelector(".stat-row")
              .getBoundingClientRect();
            return [...document.querySelectorAll(".wb-usage > div")].map(
              (el) => {
                const range = document.createRange();
                range.selectNodeContents(el);
                const rect = range.getBoundingClientRect();
                return {
                  lines: range.getClientRects().length,
                  fits: rect.left >= row.left && rect.right <= row.right,
                };
              },
            );
          });
          for (const { lines, fits } of layout) {
            assert.equal(lines, 1, `one line per value/label at ${width}px`);
            assert.ok(fits, `usage text fits the stats row at ${width}px`);
          }
          assert.ok(
            await page.evaluate(
              () => document.documentElement.scrollWidth <= innerWidth,
            ),
            `no horizontal page overflow at ${width}px`,
          );
        }
      }
    }
  } finally {
    await browser.close();
  }
});
