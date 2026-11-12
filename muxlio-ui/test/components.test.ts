import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import test from "node:test";

// Exercise the same templates and rendering functions shipped in the browser.
const components = vm.runInNewContext(
  readFileSync(new URL("./.generated/components.js", import.meta.url), "utf8") +
    ";({renderComponent,componentBody,renderEmptyState,renderDataTable,renderMetricCard,renderPagination,renderAccountingCard,renderInputField,renderTextField,renderNotice,renderPlaceholder,renderStatusBadge,renderAttemptStatus,renderDefinitionRow,renderSmallContent,componentEscape,renderExecutionMetadata})",
);

test("component text and attribute values escape untrusted content exactly once", () => {
  const input = '<img src=x onerror="alert(1)"> & {{value}}';
  const empty = components.renderEmptyState(input, input);
  assert.ok(!empty.includes("<img"));
  assert.match(empty, /&lt;img/);
  assert.match(empty, /&amp; \{\{value\}\}/);
  assert.ok(!empty.includes("&amp;lt;"));
  const field = components.renderInputField(
    input,
    'name" onfocus="bad',
    "text",
    input,
    components.componentBody("required"),
  );
  assert.match(field, /name="name&quot; onfocus=&quot;bad"/);
  assert.ok(!field.includes('onfocus="bad'));
  const textarea = components.renderTextField(
    "Result",
    "result",
    "</textarea><script>bad</script>",
    components.componentBody(""),
  );
  assert.match(textarea, /&lt;\/textarea&gt;/);
  assert.ok(!textarea.includes("<script>"));
});

test("data tables compose trusted rows and escape headers and empty-state labels", () => {
  const table = components.renderDataTable(
    ["<State>"],
    [components.componentBody("<tr><td>Ready</td></tr>")],
  );
  assert.match(table, /<th scope="col">&lt;State&gt;<\/th>/);
  assert.match(table, /<tbody><tr><td>Ready<\/td><\/tr><\/tbody>/);
  assert.match(
    components.renderDataTable([], [], { emptyTitle: "<Empty>" }),
    /&lt;Empty&gt;/,
  );
});

test("metrics and accounting cards preserve reported zero and unavailable readings", () => {
  assert.match(
    components.renderMetricCard("Attempts", 0),
    /<strong>0<\/strong>/,
  );
  const card = components.renderAccountingCard(
    "Tokens",
    { label: "Collected", value: "0" },
    "Incomplete · total may be higher",
    [{ label: "Estimated fallback", value: "9223372036854775807" }],
  );
  assert.match(card, />0<\/dd>/);
  assert.match(
    components.renderAccountingCard(
      "Cost",
      { label: "Usage", value: "Unavailable" },
      "No usage reported",
    ),
    /Unavailable/,
  );
  assert.match(card, /9223372036854775807/);
  assert.match(card, /Incomplete · total may be higher/);
});

test("pagination renders delegated actions and disables unavailable directions", () => {
  const markup = components.renderPagination({
    label: "Runs pages",
    summary: "Page 1",
    previous: { attributes: { "data-page": 0 }, disabled: true },
    next: { attributes: { "data-page": 2 }, disabled: false },
  });
  assert.match(markup, /data-page="0"\s+disabled/);
  assert.match(markup, /data-page="2"\s*>Next/);
  assert.throws(
    () =>
      components.renderPagination({
        label: "Bad action",
        summary: "",
        previous: { attributes: { onclick: "bad()" }, disabled: false },
        next: { attributes: {}, disabled: false },
      }),
    /data attributes/,
  );
});

test("dialog templates compose fields while escaping titles and submit labels", () => {
  const dialog = components.renderComponent("form-dialog", {
    title: "<Review>",
    fields: components.componentBody('<input name="reason" required>'),
    submit: "<Save>",
  });
  assert.match(dialog, /<h2>&lt;Review&gt;<\/h2>/);
  assert.match(dialog, /<input name="reason" required>/);
  assert.match(dialog, /&lt;Save&gt;/);
  assert.match(dialog, /aria-live="polite"/);
  assert.throws(
    () => components.renderComponent("metric-card", { label: "Missing" }),
    /Missing metric-card component slot/,
  );
});

test("shared feedback and badges escape text once and preserve zero values", () => {
  const message = "<script> & done";
  const notice = components.renderNotice(message, { error: true });
  assert.match(notice, /role="alert"/);
  assert.match(notice, /&lt;script&gt; &amp; done/);
  assert.ok(!notice.includes("&amp;lt;"));
  assert.match(components.renderPlaceholder(message), /&lt;script&gt;/);
  assert.match(
    components.renderStatusBadge("running", "<Ready>"),
    /&lt;Ready&gt;/,
  );
  assert.match(components.renderAttemptStatus(0, "running", "Running"), /#0/);
  assert.match(components.renderDefinitionRow("Measured", 0), />0<\/dd>/);
  assert.match(components.renderSmallContent("<note>"), /&lt;note&gt;/);
  assert.match(
    components.renderSmallContent(
      components.componentBody('<a href="/runs">Runs</a>'),
    ),
    /<a href="\/runs">/,
  );
});

test("execution metadata shares safe run and transcript links across both views", () => {
  const data = {
    runId: "run/a",
    nodeId: "<task>",
    state: "running",
    stateTone: "running",
    executor: "generic/echo",
    attempt: { retryNumber: 0, tone: "running", label: "Running" },
  };
  const live = components.renderExecutionMetadata({
    ...data,
    workflowName: "<workflow>",
    transcriptActivationId: "node/a",
  });
  const transcript = components.renderExecutionMetadata({
    ...data,
    className: "transcript-meta-kv",
  });
  for (const html of [live, transcript]) {
    assert.match(html, /href="\/runs\/run%2Fa"/);
    assert.match(html, /&lt;task&gt;/);
    assert.match(html, /#0/);
    assert.match(html, /generic\/echo/);
  }
  assert.match(live, /&lt;workflow&gt;/);
  assert.match(live, /\/transcripts\/run%2Fa\/node%2Fa/);
  assert.ok(!transcript.includes("Open transcript page"));
});
