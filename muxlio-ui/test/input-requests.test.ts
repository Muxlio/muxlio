import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import test from "node:test";

const fullSource = readFileSync(
  new URL("./.generated/inspector.js", import.meta.url),
  "utf8",
);
const source = fullSource.slice(
  fullSource.indexOf("function requestFields("),
  fullSource.indexOf("async function loadWorkers("),
);
const esc = (value) =>
  String(value ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
const { requestFields, readRequestResponse, refreshInputRequests } =
  vm.runInNewContext(
    `${source};({requestFields,readRequestResponse,refreshInputRequests})`,
    { esc },
  );

test("JSON Schema drives typed response fields and escapes untrusted text", () => {
  const html = requestFields({
    type: "object",
    required: ["approved"],
    properties: {
      approved: { type: "boolean" },
      reason: { type: "string", title: "<script>" },
      score: { type: "integer", minimum: 0, maximum: 5 },
      choice: { enum: ["yes", "no"] },
      extra: { type: "array" },
    },
  });
  assert.match(html, /<select[^>]*required/);
  assert.match(html, /&lt;script&gt;/);
  assert.match(html, /type="number" step="1" min="0" max="5"/);
  assert.match(html, /<textarea/);
  assert.doesNotMatch(html, /<script>/);
});

function field(path, value, json = false, required = true) {
  return {
    dataset: {
      responsePath: JSON.stringify(path),
      responseRequired: undefined as string | undefined,
    },
    value,
    required,
    hasAttribute: () => json,
  };
}
test("required strings accept empty values and object enums retain their choices", () => {
  const html = requestFields({ type: "string" });
  assert.match(html, /data-response-required="true"/);
  assert.doesNotMatch(html, /\srequired(?:\s|>)/);
  const empty = field(["reason"], "", false, false);
  empty.dataset.responseRequired = "true";
  assert.equal(
    JSON.stringify(readRequestResponse({ querySelectorAll: () => [empty] })),
    '{"reason":""}',
  );
  const root = field([], "", false, false);
  root.dataset.responseRequired = "true";
  assert.equal(readRequestResponse({ querySelectorAll: () => [root] }), "");
  const choices = requestFields({
    type: "object",
    properties: { approved: { type: "boolean" } },
    enum: [{ approved: true }, { approved: false }],
  });
  assert.match(choices, /<select/);
  assert.doesNotMatch(choices, /<fieldset/);
  assert.match(choices, /&quot;approved&quot;:true/);
  assert.equal(
    JSON.stringify(
      readRequestResponse({
        querySelectorAll: () => [field([], '{"approved":false}', true)],
      }),
    ),
    '{"approved":false}',
  );
});
test("responses preserve false, zero, scalar JSON and optional omissions", () => {
  const read = (fields) =>
    JSON.parse(
      JSON.stringify(readRequestResponse({ querySelectorAll: () => fields })),
    );
  assert.deepEqual(
    read([
      field(["approved"], "false", true),
      field(["score"], "0", true),
      field(["reason"], "", false, false),
    ]),
    { approved: false, score: 0 },
  );
  assert.equal(read([field([], "false", true)]), false);
  assert.equal(read([field([], "null", true)]), null);
  assert.throws(() => read([field([], "{bad", true)]));
  const response = read([field(["__proto__", "polluted"], "true", true)]);
  assert.equal(
    (Object.prototype as Record<string, unknown>).polluted,
    undefined,
  );
  assert.equal(response.__proto__.polluted, true);
});

test("multiple same-name requests retain their own IDs and preserve in-progress forms", async () => {
  const panel = {},
    container = { innerHTML: "", querySelectorAll: () => [] };
  let calls = 0;
  const requests = ["first", "second"].map((requestId) => ({
    requestId,
    name: "Review",
    activationId: `act-${requestId}`,
    state: "pending",
    schema: { type: "boolean" },
  }));
  const context = {
    esc,
    current: "r",
    $: (selector) => (selector === "#inputRequestsPanel" ? panel : container),
    api: async () => {
      calls++;
      return { requests };
    },
  };
  const refresh = vm.runInNewContext(`${source};refreshInputRequests`, context);
  await refresh("r");
  assert.match(container.innerHTML, /data-input-request="first"/);
  assert.match(container.innerHTML, /data-input-request="second"/);
  container.innerHTML = "edited form";
  await refresh("r");
  assert.equal(container.innerHTML, "edited form");
  assert.equal(calls, 2);
});

test("response delivery targets its request and reuses the key after a network failure", async () => {
  const status = { textContent: "" },
    button = { disabled: false },
    panel = {},
    deliveries = [];
  const form: Record<string, any> = {
    dataset: { inputRequest: "request-2" },
    querySelector: (selector) => (selector === "button" ? button : status),
    querySelectorAll: () => [field([], "false", true)],
  };
  const container = { querySelectorAll: () => [form] };
  const context = {
    esc,
    current: "r",
    $: (selector) => (selector === "#inputRequestsPanel" ? panel : container),
    crypto: { randomUUID: () => "delivery" },
    api: async (path, options) => {
      if (options) {
        deliveries.push({ path, options });
        throw Error("Connection interrupted");
      }
      return {
        requests: [
          {
            requestId: "request-2",
            activationId: "act",
            name: "Review",
            state: "pending",
            schema: { type: "boolean" },
          },
        ],
      };
    },
  };
  const refresh = vm.runInNewContext(`${source};refreshInputRequests`, context);
  await refresh("r");
  for (let i = 0; i < 2; i++) await form.onsubmit({ preventDefault() {} });
  assert.equal(status.textContent, "Connection interrupted");
  assert.equal(button.disabled, false);
  assert.equal(deliveries.length, 2);
  for (const { path, options } of deliveries) {
    assert.equal(path, "/v1/runs/r/input-requests/request-2");
    assert.equal(options.headers["Idempotency-Key"], "delivery");
    assert.equal(options.body, "false");
  }
});

test("updating another request preserves an outstanding form and its delivery key", async () => {
  let forms = [],
    requests = ["first", "second"].map((requestId) => ({
      requestId,
      state: "pending",
    }));
  let keys = 0;
  const container = {
    querySelectorAll: () => forms,
    set innerHTML(value) {
      forms = requests
        .filter((req) => req.state === "pending")
        .map((req) => ({
          dataset: { inputRequest: req.requestId },
          replaceWith(existing) {
            forms[forms.indexOf(this)] = existing;
          },
        }));
    },
  };
  const context = {
    esc,
    current: "r",
    $: (selector) => (selector === "#inputRequestsPanel" ? {} : container),
    crypto: { randomUUID: () => String(++keys) },
    api: async () => ({ requests }),
  };
  const refresh = vm.runInNewContext(`${source};refreshInputRequests`, context);
  await refresh("r");
  const original = forms[1],
    handler = original.onsubmit;
  original.editedValue = "unsent response";
  requests = [{ requestId: "first", state: "fulfilled" }, requests[1]];
  await refresh("r");
  assert.equal(forms.length, 1);
  assert.equal(forms[0], original);
  assert.equal(forms[0].onsubmit, handler);
  assert.equal(forms[0].editedValue, "unsent response");
  assert.equal(keys, 2);
});
