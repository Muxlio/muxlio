import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

const formatSource = readFileSync(new URL('./js/format.js', import.meta.url), 'utf8');
const usageSource = readFileSync(new URL('./js/usage-summary.js', import.meta.url), 'utf8');
const wbSource = readFileSync(new URL('./js/workbench.js', import.meta.url), 'utf8');
const jsonSource = readFileSync(new URL('./js/json.js', import.meta.url), 'utf8');
const inspSource = readFileSync(new URL('./js/inspector.js', import.meta.url), 'utf8');
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
    current: 'run-x',
    wbCurrentRunState: 'succeeded',
    esc: s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])),
    duration: (a, b) => '1.0 s',
    date: x => (x ? new Date(x).toLocaleString() : '—'),
    stateClass: s => s || '',
    replaceHTMLPreservingDetails: () => {},
    treeMarkup: () => '',
    transcriptSectionMarkup: () => '',
    activationHasTranscript: () => false,
    scheduleTranscriptPoll: () => {},
    pretty: x => JSON.stringify(x ?? null, null, 2),
  };
}

// ── sumRunTokenUsage ──────────────────────────────────────────────────────
{
  const ctx = makeCtx();
  const { sumRunTokenUsage, sumExecutionTime } = vm.runInNewContext(`${bundle}; ({sumRunTokenUsage,sumExecutionTime})`, ctx);

  assert.strictEqual(sumRunTokenUsage([]), null, 'empty attempts → null');
  assert.strictEqual(sumExecutionTime([{ executionDurationMs: 100 }, { executionDurationMs: 250 }]), 350, 'parallel attempts add');
  assert.strictEqual(sumExecutionTime([{ executionDurationMs: 0 }]), 0, 'measured zero is retained');
  assert.strictEqual(sumExecutionTime([{ startedAt: 'legacy' }]), null, 'legacy timing remains unavailable');
  assert.strictEqual(sumRunTokenUsage(null), null, 'null attempts → null');

  assert.strictEqual(
    sumRunTokenUsage([
      { id: 'a', usage: null },
      { id: 'b', usage: null },
    ]),
    null,
    'all-null usage → null (unavailable, not zero)',
  );

  const mixed = sumRunTokenUsage([
    { id: 'a', usage: { input: 100, output: 50, cached: 20 } },
    { id: 'b', usage: null },
    { id: 'c', usage: { input: 200, output: 80, cached: 10 } },
  ]);
  assert.ok(mixed !== null, 'at least one reported usage → non-null result');
  assert.strictEqual(mixed.input, 300);
  assert.strictEqual(mixed.output, 130);
  assert.strictEqual(mixed.cached, 30);

  const partial = sumRunTokenUsage([{ id: 'p', usage: { input: 9, output: null, cached: null } }]);
  assert.strictEqual(partial.input, 9);
  assert.strictEqual(partial.output, null);
  assert.strictEqual(partial.cached, null);

  const zero = sumRunTokenUsage([{ id: 'z', usage: { input: 0, output: 0, cached: 0 } }]);
  assert.ok(zero !== null, 'reported-zero usage → non-null');
  assert.strictEqual(zero.input, 0);
}
console.log('sumRunTokenUsage checks passed');

// ── buildUsagePresentation (shared model) ───────────────────────────────
{
  const { buildUsagePresentation } = vm.runInNewContext(`${bundle}; ({buildUsagePresentation})`, makeCtx());
  const creditsOnly = buildUsagePresentation({
    credits: [{ unit: 'cursor.fast', observed: 1.5, unknown: 0 }],
    costUsd: { unknown: 1 },
  }, { attemptCount: 2, runState: 'succeeded' });
  assert.ok(creditsOnly.creditsOnly, 'credits-only runs do not imply tokens');
  assert.equal(creditsOnly.primary.length, 1);
  assert.match(creditsOnly.primary[0].label, /Credits/);

  const mixed = buildUsagePresentation({
    tokens: { observed: 1200, unknown: 1, reporting: 2, pending: 0 },
    tokenDimensions: {
      input: { observed: 700, reporting: 2, unknown: 0 },
      output: { observed: 500, reporting: 2, unknown: 0 },
      cached: { observed: 0, reporting: 2, unknown: 0 },
      coverage: { attempts: 3, pending: 0, reporting: 2 },
    },
    costUsd: { observed: 0.12, estimated: 0.15, unknown: 0 },
  }, { attemptCount: 3, runState: 'succeeded' });
  assert.equal(mixed.primary.find(m => m.key === 'tokens')?.value, 1200);
  assert.ok(mixed.primary.some(m => m.key === 'cost-observed'));
  assert.ok(mixed.primary.some(m => m.key === 'cost-estimated'));
  assert.ok(mixed.details.some(d => d.label.includes('Cached subtotal')));
}
console.log('buildUsagePresentation checks passed');

// ── buildWbStats adaptive usage ──────────────────────────────────────────
{
  const run = { state: 'succeeded', createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() };
  const acts = [{ state: 'succeeded' }, { state: 'succeeded' }];

  const ctxNone = makeCtx([]);
  const { buildWbStats: buildNone } = vm.runInNewContext(`${bundle}; ({buildWbStats})`, ctxNone);
  const htmlNone = buildNone(run, acts);
  assert.doesNotMatch(htmlNone, /Input tokens/, 'no separate input token card');
  assert.match(htmlNone, /wb-usage/, 'compact usage region present');
  assert.match(htmlNone, /—/, 'idle usage when no attempts recorded');

  const ctxUnavail = makeCtx([
    { activationId: 'act-1', retryNumber: 0, state: 'succeeded', usage: null },
    { activationId: 'act-2', retryNumber: 0, state: 'succeeded', usage: null },
  ]);
  const { buildWbStats: buildUnavail } = vm.runInNewContext(`${bundle}; ({buildWbStats})`, ctxUnavail);
  const htmlUnavail = buildUnavail(run, acts);
  assert.match(htmlUnavail, /No usage reported/, 'calm empty state instead of three Unavailable tiles');
  assert.doesNotMatch(htmlUnavail, /<div class="stat"><div class="stat-val">Unavailable<\/div><div class="stat-label">Input tokens/, 'no prominent unavailable token columns');

  const ctxReported = makeCtx([
    { activationId: 'act-1', retryNumber: 0, state: 'failed', usage: { input: 500, output: 200, cached: 100 } },
    { activationId: 'act-1', retryNumber: 1, state: 'succeeded', usage: { input: 300, output: 150, cached: 50 } },
    { activationId: 'act-2', retryNumber: 0, state: 'succeeded', usage: { input: 200, output: 100, cached: 0 } },
  ]);
  const { buildWbStats: buildRep } = vm.runInNewContext(`${bundle}; ({buildWbStats})`, ctxReported);
  const htmlRep = buildRep(run, acts);
  assert.doesNotMatch(htmlRep, /wb-usage-primary|wb-usage-measure/, 'summary row is a single stat, not a measure stack');
  assert.match(htmlRep, /class="stat-val">1\.4K<\/div><div class="stat-label">Usage<\/div>/, 'headline value matches elapsed-time stat layout');
  assert.match(htmlRep, /Token subtotal/, 'token measure named in expandable breakdown');
  assert.match(htmlRep, /Input subtotal/, 'dimension breakdown is in expandable details');
  assert.match(htmlRep, /data-open-operations/, 'links to attempt-level usage');

  const zeroStats = vm.runInNewContext(`${bundle}; buildWbStats`, makeCtx([
    { state: 'succeeded', usage: { input: 0, output: 0, cached: 0 } },
  ]))(run, acts);
  assert.match(zeroStats, /class="stat-val">0<\/div><div class="stat-label">Usage<\/div>/, 'reported zero shown as headline stat');

  const partialRun = {
    state: 'succeeded',
    createdAt: run.createdAt,
    updatedAt: run.updatedAt,
    overview: {
      attempts: 2,
      usage: {
        tokens: { observed: 120, unknown: 1, reporting: 1 },
        tokenDimensions: {
          input: { observed: 100, reporting: 1, unknown: 1 },
          output: { observed: 20, reporting: 1, unknown: 1 },
          cached: { observed: 30, reporting: 1, unknown: 1 },
          coverage: { attempts: 2, pending: 0, reporting: 1 },
        },
      },
    },
  };
  const { buildWbStats: buildPartial } = vm.runInNewContext(`${bundle}; ({buildWbStats})`, makeCtx([
    { activationId: 'act-1', retryNumber: 0, state: 'succeeded', usage: { input: 100, output: 20, cached: 30 } },
    { activationId: 'act-2', retryNumber: 0, state: 'succeeded', usage: null },
  ]));
  const htmlPartial = buildPartial(partialRun, acts);
  assert.match(htmlPartial, /Token subtotal/);
  assert.match(htmlPartial, /Input subtotal.*1\/2 attempts/);

  const fullRun = { ...run, usage: partialRun.overview.usage };
  const { buildWbStats: buildFull } = vm.runInNewContext(`${bundle}; ({buildWbStats})`, makeCtx([]));
  const htmlFull = buildFull(fullRun, acts);
  assert.match(htmlFull, /120/, 'summary API token subtotal on initial render');

  const pendingRun = {
    state: 'running',
    createdAt: run.createdAt,
    updatedAt: run.updatedAt,
    usage: { tokenDimensions: { coverage: { attempts: 1, pending: 1, reporting: 0 } } },
  };
  const pendingHtml = buildFull(pendingRun, acts);
  assert.match(pendingHtml, /Usage pending/, 'in-flight attempts identified as pending');

  const creditRun = {
    state: 'succeeded',
    createdAt: run.createdAt,
    updatedAt: run.updatedAt,
    overview: {
      attempts: 1,
      usage: { credits: [{ unit: 'provider.units', observed: 2.5, unknown: 0 }] },
    },
  };
  const { buildWbStats: buildCredit } = vm.runInNewContext(`${bundle}; ({buildWbStats})`, makeCtx([]));
  const creditHtml = buildCredit(creditRun, acts);
  assert.match(creditHtml, /Credits · provider\.units/, 'credits displayed without token subtotal');
  assert.doesNotMatch(creditHtml, /Token subtotal/, 'no implied token total for credits-only');

  const costRun = {
    ...run,
    usage: {
      costUsd: { observed: 0.5, estimated: 0.55, unknown: 0 },
      tokens: { observed: 10, unknown: 0, reporting: 1 },
      tokenDimensions: {
        input: { observed: 6, reporting: 1, unknown: 0 },
        output: { observed: 4, reporting: 1, unknown: 0 },
        cached: { observed: 0, reporting: 1, unknown: 0 },
        coverage: { attempts: 1, pending: 0, reporting: 1 },
      },
    },
  };
  const costHtml = buildFull(costRun, acts);
  assert.match(costHtml, /class="stat-val">10<\/div><div class="stat-label">Usage<\/div>/, 'tokens win headline when cost is also reported');
  assert.match(costHtml, /Cost · observed/, 'observed cost stays a distinct drawer measure');
  assert.match(costHtml, /Cost · estimated/, 'estimated cost stays a distinct drawer measure');
  assert.doesNotMatch(costHtml, /wb-usage-measure/, 'cost labels do not crowd the summary row');
  assert.match(costHtml, /wb-usage-details/, 'keyboard-expandable details element');
  assert.match(costHtml, /summary/, 'native summary control for accessibility');

  const manyCreditsRun = {
    ...run,
    usage: { credits: Array.from({ length: 12 }, (_, i) => ({ unit: `unit-${i}`, observed: i, unknown: 0 })) },
  };
  const manyCreditsHtml = buildFull(manyCreditsRun, acts);
  assert.match(manyCreditsHtml, /class="stat-val">0<\/div><div class="stat-label">Usage<\/div>/, 'first credit unit headlines the stat row');
  assert.match(manyCreditsHtml, /Show 5 more usage details/, 'additional credit units stay in expandable breakdown');
  assert.match(manyCreditsHtml, /unit-11/, 'last measure is rendered in the expandable overflow');
}
console.log('buildWbStats adaptive usage checks passed');

// ── buildAttemptUsageMarkup per-activation inspector section ─────────────
{
  function makeInspCtx(runAttempts) {
    return makeCtx(runAttempts);
  }

  const ctxNoAtt = makeInspCtx([]);
  const { buildAttemptUsageMarkup: buNoAtt } = vm.runInNewContext(
    `${bundle}; ${inspBundle}; ({buildAttemptUsageMarkup})`,
    ctxNoAtt,
  );
  assert.strictEqual(buNoAtt({ id: 'act-old', nodeId: 'n', state: 'succeeded' }), '', 'no attempts → empty');

  const ctxWith = makeInspCtx([
    { activationId: 'act-1', retryNumber: 0, state: 'succeeded', usage: { input: 1200, output: 800, cached: 300 } },
  ]);
  const { buildAttemptUsageMarkup: buWith } = vm.runInNewContext(
    `${bundle}; ${inspBundle}; ({buildAttemptUsageMarkup})`,
    ctxWith,
  );
  const html1 = buWith({ id: 'act-1', nodeId: 'n', state: 'succeeded' });
  assert.match(html1, /Token usage/);
  assert.match(html1, /1\.2K/);

  const ctxRetry = makeInspCtx([
    { activationId: 'act-2', retryNumber: 0, state: 'failed', usage: { input: 500, output: 200, cached: 0 } },
    { activationId: 'act-2', retryNumber: 1, state: 'succeeded', usage: null },
  ]);
  const { buildAttemptUsageMarkup: buRetry } = vm.runInNewContext(
    `${bundle}; ${inspBundle}; ({buildAttemptUsageMarkup})`,
    ctxRetry,
  );
  const htmlRetry = buRetry({ id: 'act-2', nodeId: 'n', state: 'succeeded' });
  assert.match(htmlRetry, /<tr><td>2<\/td><td colspan="3"[^>]*>—<\/td><\/tr>/);
  assert.doesNotMatch(htmlRetry, /Retry \d/, 'no redundant Retry N label');

  for (const usage of [null, { input: 0, output: 0, cached: 0 }]) {
    const context = makeInspCtx([
      { activationId: 'agent', executor: 'custom/agent', retryNumber: 1, state: 'succeeded', usage },
    ]);
    const render = vm.runInNewContext(`${bundle}; ${inspBundle}; buildAttemptUsageMarkup`, context);
    const html = render({ id: 'agent' });
    if (usage === null) {
      assert.match(html, /<td colspan="3"[^>]*>—<\/td>/);
      assert.doesNotMatch(html, /<td class="num">0<\/td>/);
    } else {
      assert.equal((html.match(/<td class="num">0<\/td>/g) || []).length, 3);
    }
  }
}
console.log('buildAttemptUsageMarkup checks passed');

{
  const format = vm.runInNewContext(`${bundle};fmtTokens`, makeCtx());
  for (const [value, expected] of [[0, '0'], [999, '999'], [1000, '1.0K'], [1250000, '1.3M'], [999999, '1.0M'], [1500000000, '1.5B']]) assert.equal(format(value), expected);
}
