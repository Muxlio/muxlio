import assert from "node:assert/strict";
import { once } from "node:events";
import { test } from "node:test";
import { createUIServer } from "../../src/server.ts";
import { port } from "../helpers.ts";

const { chromium } = await import(
  process.env.MUXLIO_BROWSER_MODULE || "playwright"
);

test("run detail collection actions preserve drafts and original input", async (t) => {
  const ui = createUIServer({ coordinatorURL: new URL("http://127.0.0.1:1") });
  ui.listen(0, "127.0.0.1");
  await once(ui, "listening");
  t.after(() => ui.close());
  const browser = await chromium.launch({
    headless: true,
    channel: process.env.MUXLIO_BROWSER_CHANNEL,
  });
  try {
    const page = await browser.newPage();
    const run = "/v1/runs/run-a";
    const collections = `${run}/collections`;
    const itemSchema = {
      type: "object",
      required: ["key", "n"],
      properties: { key: { type: "string" }, n: { type: "integer" } },
    };
    const initial = { key: "seed", n: 1 };
    const jobs = { open: true, items: [{ id: initial.key, value: initial }] };
    const extra = { open: true, items: [] };
    let canAppend = true;
    let declared = true;
    const posts: [string, string | null][] = [];
    await page.route("**/v1/**", async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      const path = url.pathname;
      const send = (json: unknown, status = 200) =>
        route.fulfill({ json, status });
      if (path === run) {
        return send({
          id: "run-a",
          state: "waiting",
          input: { items: [initial] },
          overview: { activations: 0, attempts: 0 },
        });
      }
      if (path === collections) {
        return send({
          canAppend,
          collections: declared
            ? [
                { name: "jobs", itemSchema },
                { name: "extra", itemSchema },
              ]
            : [],
        });
      }
      if (path === `${collections}/jobs`) return send({ collection: jobs });
      if (path === `${collections}/extra`) return send({ collection: extra });
      if (request.method() === "POST") {
        const body = request.postData();
        posts.push([path, body]);
        if (path === `${collections}/extra/close`) {
          extra.open = false;
          return send({ collection: extra });
        }
        if (path === `${collections}/jobs/items`) {
          if (!jobs.open)
            return send(
              { message: "Collection closed by another caller" },
              409,
            );
          const item = JSON.parse(body);
          if (!Number.isInteger(item.n))
            return send({ message: "Item n must be an integer" }, 422);
          if (item.key === "retry")
            return send({ message: "Temporary storage failure" }, 503);
          jobs.items.push({ id: item.key, value: item });
          return send({ collection: jobs, accepted: true });
        }
      }
      return send({
        activations: [],
        events: [],
        outputs: [],
        attempts: [],
        requests: [],
      });
    });
    const openRunPage = async () => {
      await page.goto(`http://127.0.0.1:${port(ui)}/runs/run-a`);
      await page.getByRole("tab", { name: "Collections" }).click();
    };
    await openRunPage();
    const executionTab = page.getByRole("tab", { name: "Executions" });
    await executionTab.press("End");
    assert.equal(
      await page
        .getByRole("tab", { name: "Collections" })
        .getAttribute("aria-selected"),
      "true",
    );
    const collection = (name: string) =>
      page.locator("#runCollections section").filter({
        has: page.getByRole("heading", { name, exact: true }),
      });
    const jobSection = collection("jobs");
    const extraSection = collection("extra");
    const draft = jobSection.getByRole("textbox", { name: "Item JSON" });
    const add = jobSection.getByRole("button", { name: "Add item" });
    const submit = jobSection.getByRole("button", { name: "Submit item" });
    const status = jobSection.getByRole("status");
    const accepted = jobSection.getByRole("listitem");
    await add.click();
    assert.match(
      await jobSection.getByText('"required"', { exact: false }).textContent(),
      /"n"/,
    );
    await draft.fill("{");
    await submit.click();
    await status
      .getByText("Enter a valid JSON item", { exact: false })
      .waitFor();
    assert.equal(posts.length, 0);
    const wrongType = '{"key":"b","n":"wrong"}';
    await draft.fill(wrongType);
    await submit.click();
    await status.getByText("Item n must be an integer").waitFor();
    assert.equal(await draft.inputValue(), wrongType);
    const retry = '{"key":"retry","n":2}';
    await draft.fill(retry);
    await submit.click();
    await status.getByText("Temporary storage failure").waitFor();
    await page.evaluate("refreshRunCollections('run-a')");
    assert.equal(await draft.inputValue(), retry);
    const valid = '{"key":"b","n":9007199254740993}';
    await draft.fill(valid);
    await submit.click();
    await status.getByText("Item accepted.").waitFor();
    assert.deepEqual(posts.at(-1), [`${collections}/jobs/items`, valid]);
    assert.deepEqual(await accepted.locator("strong").allTextContents(), [
      "seed",
      "b",
    ]);

    await page.getByRole("tab", { name: "Run data" }).click();
    const input = page.locator("#runInputInspector");
    await input.getByText("items", { exact: false }).first().waitFor();
    assert.match(await input.textContent(), /seed/);
    assert.doesNotMatch(await input.textContent(), /900719925474099/);
    assert.equal(await input.locator("textarea, input").count(), 0);
    await page.getByRole("tab", { name: "Collections" }).click();

    await add.click();
    const concurrent = '{"key":"c","n":3}';
    await draft.fill(concurrent);
    jobs.open = false;
    await submit.click();
    await status.getByText("Collection closed by another caller").waitFor();
    await jobSection.getByText("Closed", { exact: true }).waitFor();
    assert.equal(await draft.inputValue(), concurrent);
    assert.equal(await draft.getAttribute("readonly"), "");
    assert.equal(await submit.count(), 0);
    assert.deepEqual(await accepted.locator("strong").allTextContents(), [
      "seed",
      "b",
    ]);

    canAppend = false;
    await openRunPage();
    await extraSection.getByText("Open", { exact: true }).waitFor();
    assert.equal(
      await page.getByRole("button", { name: "Add item" }).count(),
      0,
    );
    assert.equal(
      await page.getByRole("button", { name: "Close collection" }).count(),
      0,
    );
    canAppend = true;
    await page.evaluate("refreshRunCollections('run-a')");
    await extraSection
      .getByText("Accepted work will finish", { exact: false })
      .waitFor();
    await extraSection
      .getByRole("button", { name: "Close collection" })
      .click();
    await extraSection.getByText("Closed", { exact: true }).waitFor();
    assert.deepEqual(posts.at(-1), [`${collections}/extra/close`, null]);
    declared = false;
    await page.reload();
    await page.getByRole("heading", { name: "Workflow run" }).waitFor();
    await executionTab.press("End");
    assert.equal(
      await page.getByRole("tab", { name: "Collections" }).count(),
      0,
    );
    assert.equal(
      await page
        .getByRole("tab", { name: "Run data" })
        .getAttribute("aria-selected"),
      "true",
    );
  } finally {
    await browser.close();
  }
});
