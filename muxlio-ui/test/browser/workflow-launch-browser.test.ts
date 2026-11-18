import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import { test } from "node:test";
import { createUIServer } from "../../src/server.ts";
import { port } from "../helpers.ts";

test("launches a registered workflow through the dashboard and proxy", async (t) => {
  const { chromium } = await import(
    process.env.MUXLIO_BROWSER_MODULE || "playwright"
  );
  const browser = await chromium.launch({
    channel: process.env.MUXLIO_BROWSER_CHANNEL || undefined,
  });
  t.after(() => browser.close());

  const fixtureToken = "workflow-launch-fixture";
  process.env.MUXLIO_TOKEN = fixtureToken;
  const unsafe = '"><img src=x onerror=window.workflowInjected=true>';
  const digest = "sha256:" + "a".repeat(64);
  const legacyDigest = "sha256:" + "b".repeat(64);
  const versions = [
    { digest, name: unsafe, createdAt: "2026-01-02" },
    { digest: legacyDigest, name: "Second workflow", createdAt: "2026-01-01" },
  ];
  const compiled = `{ "digest": "${digest}", "inputSchema": {"maximum":9007199254740993} }`;
  const input = '{"n":9007199254740993,"fraction":0.1234567890123456789}';
  const paths = {
    versions: "/v1/workflow-versions",
    catalog: "/v1/workspaces",
    check: "/v1/runs/preflight",
    runs: "/v1/runs",
  };
  const calls: { url: URL; method: string; raw: string; key?: string }[] = [];
  let catalog: "multiple" | "legacy" | "denied" | "empty" = "multiple";
  let versionStatus = 200;
  let submissionStatus = 403;
  let submissionResult: { runId: string } | { id: string } = {
    runId: "confirmed-run",
  };
  let releaseSubmission: (() => void) | undefined;
  let coordinatorSawSubmission: () => void;
  const submissionStarted = new Promise<void>((resolve) => {
    coordinatorSawSubmission = resolve;
  });

  const coordinator = createServer(async (req, res) => {
    let raw = "";
    for await (const chunk of req) raw += chunk;
    const url = new URL(req.url!, "http://localhost");
    const path = url.pathname;
    calls.push({
      url,
      method: req.method!,
      raw,
      key: req.headers["idempotency-key"] as string | undefined,
    });
    const send = (data: unknown, status = 200) => {
      res.writeHead(status, { "content-type": "application/json" });
      res.end(JSON.stringify(data));
    };
    if (req.headers.authorization !== `Bearer ${fixtureToken}`)
      return send({}, 401);
    if (path === paths.versions) return send({ workflowVersions: versions });
    if (path.startsWith(paths.versions + "/")) {
      if (versionStatus !== 200)
        return send({ message: "Missing version" }, versionStatus);
      res.writeHead(200, { "content-type": "application/json" });
      return res.end(compiled);
    }
    if (path === paths.catalog) {
      if (catalog === "denied")
        return send({ message: "Private workspace identity" }, 403);
      if (catalog === "empty") return send({ workspaces: [], hasMore: false });
      if (catalog === "legacy")
        return send({
          workspaces: [{ workspaceId: "default", status: "configured" }],
          hasMore: false,
        });
      if (url.searchParams.get("offset") === "0")
        return send({
          workspaces: [{ workspaceId: "offline", status: "owner_offline" }],
          hasMore: true,
          nextOffset: 1,
        });
      return send({
        workspaces: [
          {
            workspaceId: unsafe,
            status: "advertised",
            path: "/private/fixture checkout",
          },
        ],
        hasMore: false,
      });
    }
    if (path === paths.check) {
      const { workspaceId, input } = JSON.parse(raw);
      const offline = workspaceId === "offline";
      const wrongInput = input.n === undefined;
      return send({
        feasible: !offline && !wrongInput,
        workersChecked: offline ? 0 : 1,
        workspaceError: offline ? `No available owning worker ${unsafe}` : "",
        inputError: wrongInput ? "input.n is required" : "",
        unverified: ["remote_quota_and_rate_limits"],
      });
    }
    if (path === paths.runs && req.method === "POST") {
      if (submissionStatus === 202)
        await new Promise<void>((resolve) => {
          releaseSubmission = resolve;
          coordinatorSawSubmission();
        });
      return send(submissionResult, submissionStatus);
    }
    if (path === paths.runs) return send({ runs: [], total: 0, states: {} });
    if (path === paths.runs + "/confirmed-run")
      return send({
        id: "confirmed-run",
        workflowName: "Confirmed fixture",
        state: "succeeded",
        planDigest: digest,
        overview: { activations: 0, completed: 0 },
      });
    if (path.endsWith("/events")) return send({ events: [] });
    if (path.endsWith("/outputs")) return send({ outputs: [] });
    if (path.endsWith("/attempts")) return send({ attempts: [] });
    return send({});
  });
  coordinator.listen(0, "127.0.0.1");
  await once(coordinator, "listening");
  t.after(() => coordinator.close());

  const ui = createUIServer({
    coordinatorURL: new URL(`http://127.0.0.1:${port(coordinator)}`),
  });
  ui.listen(0, "127.0.0.1");
  await once(ui, "listening");
  t.after(() => ui.close());

  const page = await browser.newPage();
  page.setDefaultTimeout(5000);
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const base = `http://127.0.0.1:${port(ui)}`;
  const dialog = page.getByRole("dialog", { name: "Run workflow" });
  const header = page.getByRole("button", { name: "Run workflow" });
  const row = page.locator(`[data-wf-run="${legacyDigest}"]`);
  const version = dialog.getByLabel("Workflow version");
  const workspace = dialog.getByLabel("Authorized workspace");
  const editor = dialog.getByLabel("Run input (JSON)");
  const preflight = dialog.getByRole("button", {
    name: "Preflight",
    exact: true,
  });
  const start = dialog.locator("#runWorkflowSubmitBtn");
  const posts = () =>
    calls.filter(
      ({ method, url }) => method === "POST" && url.pathname === paths.runs,
    );

  await page.goto(base + "/workflows");
  await row.focus();
  await page.keyboard.press("Enter");
  await dialog.waitFor();
  await workspace
    .locator("option")
    .filter({ hasText: "advertised" })
    .waitFor({ state: "attached" });
  assert.equal(await version.inputValue(), legacyDigest);
  assert.match(await version.locator("option").first().textContent(), /<img/);
  assert.equal(
    await version.evaluate((select) => document.activeElement === select),
    true,
  );
  assert.equal(await workspace.inputValue(), "");
  assert.doesNotMatch(await dialog.innerText(), /private\/fixture checkout/);
  assert.equal(await preflight.isDisabled(), true);
  assert.equal(await start.isDisabled(), true);
  for (let i = 0; i < 9; i++) {
    await page.keyboard.press("Tab");
    assert.equal(
      await header.evaluate((button) => document.activeElement === button),
      false,
    );
  }
  await page.keyboard.press("Escape");
  await dialog.waitFor({ state: "hidden" });
  assert.equal(
    await row.evaluate((button) => document.activeElement === button),
    true,
  );

  await header.click();
  await workspace
    .locator("option")
    .filter({ hasText: "advertised" })
    .waitFor({ state: "attached" });
  await workspace.selectOption("offline");
  assert.equal(await editor.inputValue(), "{}");
  for (const invalid of [" ", "{", "01", "1e"]) {
    await editor.fill(invalid);
    assert.equal(await editor.getAttribute("aria-invalid"), "true");
    assert.equal(await preflight.isDisabled(), true);
    assert.equal(await start.isDisabled(), true);
  }
  assert.equal(
    calls.filter(({ url }) => url.pathname === paths.check).length,
    0,
  );
  await editor.fill("{}");
  await preflight.click();
  await dialog.getByText("input.n is required").waitFor();
  await dialog
    .getByText("No available owning worker", { exact: false })
    .waitFor();
  assert.equal(await start.isDisabled(), true);
  assert.equal(posts().length, 0);
  assert.equal(await dialog.locator("img").count(), 0);

  await workspace.selectOption(unsafe);
  await editor.fill(input);
  await preflight.click();
  await dialog.getByText("Unverified caveats").waitFor();
  const check = calls.filter(({ url }) => url.pathname === paths.check).at(-1)!;
  assert.equal(
    check.raw,
    `{"workspaceId":${JSON.stringify(unsafe)},"plan":${compiled},"input":${input}}`,
  );
  assert.equal(await start.isEnabled(), true);
  await editor.fill("null");
  assert.equal(await start.isDisabled(), true);
  await editor.fill(input);
  await preflight.click();
  await start.click();
  await dialog
    .getByText("This credential cannot start the selected workflow.")
    .waitFor();
  assert.equal(new URL(page.url()).pathname, "/workflows");

  submissionStatus = 409;
  await start.click();
  await dialog
    .getByText("The submission conflicts", { exact: false })
    .waitFor();
  assert.equal(await start.isDisabled(), true);

  submissionStatus = 410;
  await preflight.click();
  await start.click();
  await dialog
    .getByText("This workflow version is no longer registered", {
      exact: false,
    })
    .waitFor();
  assert.equal(
    await version.locator(`option[value="${digest}"]`).isDisabled(),
    true,
  );
  await page.keyboard.press("Escape");

  catalog = "denied";
  await header.click();
  await dialog
    .getByText("This credential cannot read the workspace catalog.")
    .waitFor();
  assert.equal(await workspace.isDisabled(), true);
  assert.equal(await preflight.isDisabled(), true);
  assert.doesNotMatch(await dialog.innerText(), /Private workspace identity/);
  await page.keyboard.press("Escape");

  catalog = "empty";
  await header.click();
  await workspace
    .locator("option")
    .filter({ hasText: "No authorized workspaces" })
    .waitFor({ state: "attached" });
  assert.equal(await start.isDisabled(), true);
  await page.keyboard.press("Escape");

  catalog = "legacy";
  await header.click();
  await workspace
    .locator('option[value="default"]')
    .waitFor({ state: "attached" });
  assert.equal(await workspace.inputValue(), "default");
  versionStatus = 404;
  await preflight.click();
  await dialog
    .getByText("This workflow version is no longer registered", {
      exact: false,
    })
    .waitFor();
  versionStatus = 200;
  await version.selectOption(legacyDigest);
  await editor.fill(input);
  await preflight.click();

  submissionStatus = 202;
  submissionResult = { id: "unconfirmed" };
  const before = posts().length;
  await start.click();
  await submissionStarted;
  await dialog.getByRole("button", { name: "Starting…" }).waitFor();
  await start.evaluate((button: HTMLButtonElement) => button.click());
  await page.keyboard.press("Escape");
  assert.equal(await dialog.isVisible(), true);
  assert.equal(
    await dialog.getByRole("button", { name: "Close" }).isDisabled(),
    true,
  );
  assert.equal(posts().length, before + 1);
  assert.ok(releaseSubmission);
  releaseSubmission();
  await dialog
    .getByText("The coordinator returned no run ID", { exact: false })
    .waitFor();
  assert.equal(new URL(page.url()).pathname, "/workflows");
  await page.keyboard.press("Escape");
  await header.click();
  await workspace
    .locator('option[value="default"]')
    .waitFor({ state: "attached" });
  await version.selectOption(legacyDigest);
  await editor.fill(input);
  await preflight.click();

  submissionStatus = 201;
  submissionResult = { runId: "confirmed-run" };
  await start.click();
  await page.waitForURL("**/runs/confirmed-run");
  await page.getByText("Confirmed fixture", { exact: true }).first().waitFor();
  const [ambiguous, retry] = posts().slice(-2);
  assert.ok(ambiguous.key);
  assert.equal(ambiguous.key, retry.key);
  assert.equal(
    retry.raw,
    `{"planDigest":"${legacyDigest}","workspaceId":"default","input":${input}}`,
  );
  assert.notEqual(posts()[0].key, retry.key);
  assert.equal(
    calls.filter(
      ({ method, url }) =>
        method === "GET" && url.pathname === paths.runs + "/confirmed-run",
    ).length,
    1,
  );
  assert.equal(await page.evaluate(() => "workflowInjected" in window), false);
  const discovery = page.waitForRequest((request) => {
    const url = new URL(request.url());
    return (
      url.pathname === paths.runs &&
      url.searchParams.get("workspaceId") === "default"
    );
  });
  await page.goto(base + "/runs?workspaceId=default");
  await discovery;
  assert.equal(await page.locator("#workspaceFilter").inputValue(), "default");
  assert.deepEqual(errors, []);
});
