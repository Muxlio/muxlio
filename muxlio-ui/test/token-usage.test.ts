import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import { test as regression } from "node:test";
regression("token usage regression", async () => {
  const formatSource = readFileSync(
    new URL("./.generated/format.js", import.meta.url),
    "utf8",
  );
  const usageSource = readFileSync(
    new URL("./.generated/usage-summary.js", import.meta.url),
    "utf8",
  );
  const wbSource = readFileSync(
    new URL("./.generated/workbench.js", import.meta.url),
    "utf8",
  );
  const jsonSource = readFileSync(
    new URL("./.generated/json.js", import.meta.url),
    "utf8",
  );
  const inspSource = readFileSync(
    new URL("./.generated/inspector.js", import.meta.url),
    "utf8",
  );
  const bundle = `${formatSource}\n${usageSource}\n${wbSource}`;
  const inspBundle = `${jsonSource};const MuxlioJson=globalThis.MuxlioJson;\n${inspSource}`;

  function makeCtx(runAttempts = []) {
    return {
      runAttempts,
      document: { getElementById: () => null, addEventListener() {} },
      selectedActivation: null,
      activationDisplayNumbers: new Map(),
      activationData: [],
      activationTranscripts: new Map(),
      current: "run-x",
      wbCurrentRunState: "succeeded",
      esc: (s) =>
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
        ),
      duration: (a, b) => "1.0 s",
      date: (x) => (x ? new Date(x).toLocaleString() : "—"),
      stateClass: (s) => s || "",
      replaceHTMLPreservingDetails: () => {},
      treeMarkup: () => "",
      transcriptSectionMarkup: () => "",
      activationHasTranscript: () => false,
      scheduleTranscriptPoll: () => {},
      pretty: (x) => JSON.stringify(x ?? null, null, 2),
    };
  }

  {
    const { sumExecutionTime } = vm.runInNewContext(
      `${bundle};({sumExecutionTime})`,
      makeCtx(),
    );
    assert.equal(
      sumExecutionTime([
        { executionDurationMs: 100 },
        { executionDurationMs: 250 },
      ]),
      350,
    );
    assert.equal(sumExecutionTime([{ executionDurationMs: 0 }]), 0);
    assert.equal(sumExecutionTime([{ startedAt: "legacy" }]), null);
  }
  const summary = {
    tokens: {
      collectedSubtotal: "9007199254740993",
      estimatedFallbackSubtotal: null,
      knownSubtotal: "9007199254740993",
      effectiveTotal: null,
      completeness: "partial",
      attempts: { partial: 1 },
    },
    tokenDimensions: {},
    credits: [],
    costs: [],
  };
  {
    const ctx = makeCtx();
    const { buildWbStats } = vm.runInNewContext(
      `${bundle};({buildWbStats})`,
      ctx,
    );
    const html = buildWbStats(
      {
        state: "succeeded",
        createdAt: "2026-01-01",
        updatedAt: "2026-01-02",
        overview: {
          activations: 1,
          completed: 1,
          attempts: 1,
          accounting: summary,
        },
      },
      [],
    );
    assert.match(html, /9007199\.3B tokens/);
    assert.match(html, /Accounting/);
    assert.match(html, /incomplete/);
  }
  {
    const ctx = makeCtx([
      {
        activationId: "act",
        accounting: {
          revision: 1,
          final: true,
          entries: [
            {
              id: "call",
              tokens: {
                total: {
                  collected: {
                    value: "9007199254740993",
                    source: "fixture",
                    fidelity: "metered",
                  },
                },
              },
            },
          ],
        },
      },
    ]);
    const { buildAttemptUsageMarkup } = vm.runInNewContext(
      `${bundle};${inspBundle};({buildAttemptUsageMarkup})`,
      ctx,
    );
    assert.equal(buildAttemptUsageMarkup({ id: "absent" }), "");
    const html = buildAttemptUsageMarkup({ id: "act" });
    assert.match(html, /9007199254740993/);
    assert.match(html, /fixture/);
    assert.match(html, /collected/);
  }
  console.log("Canonical workbench and inspector accounting checks passed");
});
