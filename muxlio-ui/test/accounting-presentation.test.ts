import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import { test as regression } from "node:test";
regression("accounting presentation regression", async () => {
  const esc = (s) =>
    String(s ?? "").replace(
      /[&<>"']/g,
      (c) =>
        ({
          "&": "&amp;",
          "<": "&lt;",
          ">": "&gt;",
          '"': "&quot;",
          "'": "&#39;",
        })[c],
    );
  const api = vm.runInNewContext(
    readFileSync(new URL("./.generated/format.js", import.meta.url), "utf8") +
      "\n" +
      readFileSync(
        new URL("./.generated/usage-summary.js", import.meta.url),
        "utf8",
      ) +
      ";({buildUsagePresentation,buildWorkbenchUsageMarkup,buildUsageGridMarkup,buildAttemptAccountingMarkup})",
    { esc, stateClass: (s) => s },
  );
  const amount = (value, complete = true) => ({
    collectedSubtotal: value,
    estimatedFallbackSubtotal: null,
    knownSubtotal: value,
    effectiveTotal: complete ? value : null,
    completeness: complete ? "complete" : "partial",
    attempts: {
      complete: 1,
      partial: 0,
      unknown: 0,
      pending: 0,
      inapplicable: 0,
    },
  });
  const summary = {
    tokens: amount("9223372036854775807", false),
    tokenDimensions: { input: amount("9007199254740993") },
    credits: [
      {
        provider: "kiro",
        unit: "credit",
        value: amount("0.18065595854063016"),
      },
    ],
    costs: [
      { currency: "USD", basis: "api-equivalent", value: amount("0.0062") },
      { currency: "USD", basis: "plan-debit", value: amount("0.001") },
    ],
    unattributed: { credits: { unknownAttempts: 1, pendingAttempts: 0 } },
  };
  const p = api.buildUsagePresentation(summary, { attemptCount: 2 });
  const html = api.buildWorkbenchUsageMarkup(p);
  assert.match(html, /9223372036\.9B tokens/);
  assert.match(html, /incomplete/);
  assert.doesNotMatch(
    html,
    /<details|<summary|Known subtotal|coverage|Estimated fallback/,
  );
  assert.match(
    api.buildWorkbenchUsageMarkup(
      api.buildUsagePresentation(
        { tokens: amount("9809008") },
        { attemptCount: 1 },
      ),
    ),
    /9\.8M tokens/,
  );
  const unavailableGrid = api.buildUsageGridMarkup({
    tokens: amount("9809008"),
  });
  assert.match(unavailableGrid, /<h3>Tokens<\/h3>/);
  assert.match(unavailableGrid, /title="9809008">9\.8M<\/dd>/);
  assert.doesNotMatch(unavailableGrid, /Credits and cost unavailable/);
  assert.doesNotMatch(
    unavailableGrid,
    /<h3>Credits|<h3>Cost|Known subtotal|Complete total|<dt>Coverage/,
  );
  assert.doesNotMatch(unavailableGrid, /USD|kiro/);
  const grid = api.buildUsageGridMarkup(summary);
  assert.match(grid, /0\.18065595854063016/);
  assert.match(grid, /API-equivalent estimate/);
  assert.match(grid, /Plan debit/);
  assert.doesNotMatch(grid, /0\.0072|total spend/i);
  assert.equal(
    api.buildUsagePresentation({ tokens: amount("0") }, { attemptCount: 1 })
      .primary[0].value,
    "0",
  );
  assert.equal(
    api.buildUsagePresentation({}, { attemptCount: 1 }).primary.length,
    0,
  );
  const reference = { ...amount("3.42", false), methods: ["provider-quote", "credit-replacement"], attempts: {complete: 8, partial: 0, unknown: 2, pending: 0, inapplicable: 0} };
  const referenceGrid = api.buildUsageGridMarkup({ referenceValue: reference });
  assert.match(referenceGrid, /Estimated reference value \(USD\)/);
  assert.match(referenceGrid, /<dt>Subtotal<\/dt>/);
  assert.match(referenceGrid, /8 of 10 attempts priced · incomplete/);
  assert.match(referenceGrid, /Provider quote · Credit replacement rate/);
  assert.doesNotMatch(referenceGrid, /Actual account spend|Valued attempts|unpriced/);
  const referencePresentation = api.buildUsagePresentation({referenceValue: reference, tokens: amount("1000")}, {attemptCount:10});
  assert.match(api.buildWorkbenchUsageMarkup(referencePresentation), /3\.42/);
  const absentReference = api.buildUsageGridMarkup({referenceValue: {...reference, knownSubtotal: null, effectiveTotal: null}});
  assert.doesNotMatch(absentReference, /Estimated reference value|Known subtotal|Valued attempts|Actual account spend|Credits and cost unavailable/);
  assert.match(api.buildUsageGridMarkup({referenceValue: {...amount("0"), methods: ["provider-quote"]}}), /<dt>Value<\/dt><dd>0<\/dd>/);
  const unsafe = api.buildAttemptAccountingMarkup({
    entries: [
      {
        id: "call",
        model: "<script>",
        tokens: {
          total: {
            collected: { value: "0", source: "provider", fidelity: "metered" },
          },
        },
      },
    ],
  });
  assert.match(unsafe, /&lt;script&gt;/);
  assert.doesNotMatch(unsafe, /<script>/);
  const estimateSummary = {
    ...summary,
    costs: [
      {
        currency: "USD",
        basis: "api-equivalent",
        value: {
          ...amount("0.02"),
          evidence: { collected: ["provider-estimate"] },
        },
      },
    ],
  };
  assert.match(
    api.buildUsageGridMarkup(estimateSummary),
    /Collected \(provider estimate\)/,
  );
  const alternative = api.buildAttemptAccountingMarkup({
    entries: [
      {
        id: "call",
        costs: [
          {
            currency: "USD",
            basis: "api-equivalent",
            amount: {
              collected: {
                value: "0.02",
                source: "provider",
                fidelity: "provider-estimate",
              },
              estimated: {
                value: "0.03",
                source: "muxlio-pricing",
                fidelity: "derived-exact",
                method: "catalog-v1:rule",
                version: "1",
                catalogId: "sha256:pin",
              },
            },
          },
        ],
      },
    ],
  });
  assert.match(alternative, /collected \(provider estimate\)/);
  assert.match(alternative, /alternative calculation/);
  assert.match(alternative, /sha256:pin/);
  const models = api.buildAttemptAccountingMarkup({
    models: [
      { id: "auto<script>", role: "selected", source: 'config"unsafe' },
      { id: "model-1", role: "served", source: "provider-usage" },
    ],
  });
  const providerEvidence = api.buildAttemptAccountingMarkup({
    sessions: [{id:'session<script>', provider:'fixture', source:'provider-init', version:'1.2.3'}],
    entries: [{id:'event',costs:[{currency:'USD',basis:'provider-metered',amount:{collected:{value:'0',source:'ledger',fidelity:'metered',attributes:{state:'provisional<script>'}}}}]}]
  });
  assert.match(providerEvidence,/session&lt;script&gt;/);
  assert.match(providerEvidence,/state=provisional&lt;script&gt;/);
  assert.doesNotMatch(providerEvidence,/<script>/);
  assert.match(api.buildUsageGridMarkup({referenceValue:{...amount('0',false),methods:['provider-metered']}}),/Provider-metered usage/);
  assert.match(models, /selected model/);
  assert.match(models, /served model/);
  assert.match(models, /auto&lt;script&gt;/);
  assert.doesNotMatch(models, /<script>/);
  console.log("Canonical accounting presentation checks passed");
});
