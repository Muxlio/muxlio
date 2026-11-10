import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

// ── helpers ───────────────────────────────────────────────────────────────
const wbSource = readFileSync(new URL("./js/format.js", import.meta.url), "utf8") + "\n" + readFileSync(new URL('./js/workbench.js', import.meta.url), 'utf8');
const inspSource = readFileSync(new URL('./js/inspector.js', import.meta.url), 'utf8');

function makeCtx(runAttempts = []) {
  return {
    runAttempts,
    document: {getElementById: () => null, addEventListener() {}},
    selectedActivation: null,
    activationDisplayNumbers: new Map(),
    activationData: [],
    activationTranscripts: new Map(),
    current: 'run-x',
    wbCurrentRunState: 'succeeded',
    esc: s => String(s ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])),
    duration: (a, b) => '1.0 s',
    date: x => x ? new Date(x).toLocaleString() : '—',
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
  const { sumRunTokenUsage, sumExecutionTime } = vm.runInNewContext(`${wbSource}; ({sumRunTokenUsage,sumExecutionTime})`, ctx);

  // No attempts → null
  assert.strictEqual(sumRunTokenUsage([]), null, 'empty attempts → null');
  assert.strictEqual(sumExecutionTime([{executionDurationMs:100},{executionDurationMs:250}]), 350, 'parallel attempts add');
  assert.strictEqual(sumExecutionTime([{executionDurationMs:0}]), 0, 'measured zero is retained');
  assert.strictEqual(sumExecutionTime([{startedAt:'legacy'}]), null, 'legacy timing remains unavailable');
  assert.strictEqual(sumRunTokenUsage(null), null, 'null attempts → null');

  // All attempts have null usage → null (provider unavailable)
  assert.strictEqual(
    sumRunTokenUsage([
      { id: 'a', usage: null },
      { id: 'b', usage: null },
    ]),
    null,
    'all-null usage → null (unavailable, not zero)',
  );

  // A missing attempt makes the run total unavailable because its counts
  // cannot be included in a complete sum.
  const mixed = sumRunTokenUsage([
    { id: 'a', usage: { input: 100, output: 50, cached: 20 } },
    { id: 'b', usage: null },                                    // retry with no report
    { id: 'c', usage: { input: 200, output: 80, cached: 10 } }, // second agent
  ]);
  assert.ok(mixed !== null, 'at least one reported usage → non-null result');
  assert.strictEqual(mixed.input, null, 'input total unavailable with an unreported attempt');
  assert.strictEqual(mixed.output, null, 'output total unavailable with an unreported attempt');
  assert.strictEqual(mixed.cached, null, 'cached total unavailable with an unreported attempt');

  const partial = sumRunTokenUsage([{ id: 'p', usage: { input: 9, output: null, cached: null } }]);
  assert.strictEqual(partial.input, 9);
  assert.strictEqual(partial.output, null);
  assert.strictEqual(partial.cached, null);

  // Reported zero is distinct from null (zero usage, e.g. cached-only call)
  const zero = sumRunTokenUsage([{ id: 'z', usage: { input: 0, output: 0, cached: 0 } }]);
  assert.ok(zero !== null, 'reported-zero usage → non-null (distinguishable from unavailable)');
  assert.strictEqual(zero.input, 0);
}
console.log('sumRunTokenUsage checks passed');

// ── buildWbStats token stat display ──────────────────────────────────────
{
  const run = { state: 'succeeded', createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() };
  const acts = [{ state: 'succeeded' }, { state: 'succeeded' }];

  // Case 1: no attempts at all — token rows show '—' (older run, no records)
  const ctxNone = makeCtx([]);
  const { buildWbStats: buildWbStatsNone } = vm.runInNewContext(`${wbSource}; ({buildWbStats})`, ctxNone);
  const htmlNone = buildWbStatsNone(run, acts);
  // Each token total has its own labelled stat in the current workbench.
  const tokenValues = html => Object.fromEntries([...html.matchAll(
    /<div class="stat"><div class="stat-val">([^<]*)<\/div><div class="stat-label">(Input|Output|Cached) tokens<\/div><\/div>/g,
  )].map(([,value,label]) => [label,value]));
  assert.deepEqual(tokenValues(htmlNone), {Input:'—',Output:'—',Cached:'—'},
    'all three labelled token stats show an em-dash when there are no attempts');

  // Case 2: attempts present but all have null usage → show "Unavailable"
  const ctxUnavail = makeCtx([
    { activationId: 'act-1', retryNumber: 0, state: 'succeeded', usage: null },
    { activationId: 'act-2', retryNumber: 0, state: 'succeeded', usage: null },
  ]);
  const { buildWbStats: buildWbStatsUnavail } = vm.runInNewContext(`${wbSource}; ({buildWbStats})`, ctxUnavail);
  const htmlUnavail = buildWbStatsUnavail(run, acts);
  assert.deepEqual(tokenValues(htmlUnavail), {Input:'Unavailable',Output:'Unavailable',Cached:'Unavailable'},
    'all token stats must show unavailable rather than zero for missing usage');

  // Case 3: reported usage — values shown with correct totals
  const ctxReported = makeCtx([
    { activationId: 'act-1', retryNumber: 0, state: 'failed',    usage: { input: 500, output: 200, cached: 100 } },
    { activationId: 'act-1', retryNumber: 1, state: 'succeeded', usage: { input: 300, output: 150, cached:  50 } }, // retry
    { activationId: 'act-2', retryNumber: 0, state: 'succeeded', usage: { input: 200, output: 100, cached:   0 } },
  ]);
  const { buildWbStats: buildWbStatsRep } = vm.runInNewContext(`${wbSource}; ({buildWbStats})`, ctxReported);
  const htmlRep = buildWbStatsRep(run, acts);
  assert.deepEqual(tokenValues(htmlRep), {Input:'1.0K',Output:'450',Cached:'150'},
    'all three totals include retries and retain their labels');
  const zeroStats = vm.runInNewContext(`${wbSource}; buildWbStats`, makeCtx([
    {state:'succeeded',usage:{input:0,output:0,cached:0}},
  ]))(run,acts);
  assert.deepEqual(tokenValues(zeroStats), {Input:'0',Output:'0',Cached:'0'},
    'reported zero is distinct from unavailable in the rendered stats');

}
console.log('buildWbStats token stat checks passed');

// ── buildAttemptUsageMarkup per-activation inspector section ─────────────
{
  // Build minimal inspector context with the workbench source too (it provides
  // stateClass and checkIcon which inspector.js uses).
  function makeInspCtx(runAttempts, selectedAct) {
    return makeCtx(runAttempts);
  }

  // No attempts → empty string (section suppressed for older runs)
  const ctxNoAtt = makeInspCtx([]);
  const { buildAttemptUsageMarkup: buNoAtt } = vm.runInNewContext(
    `${wbSource}; ${inspSource}; ({buildAttemptUsageMarkup})`,
    ctxNoAtt,
  );
  const act0 = { id: 'act-old', nodeId: 'n', state: 'succeeded' };
  assert.strictEqual(buNoAtt(act0), '', 'no attempts → empty (older run graceful)');

  // Single attempt with reported usage
  const ctxWith = makeInspCtx([
    { activationId: 'act-1', retryNumber: 0, state: 'succeeded', usage: { input: 1200, output: 800, cached: 300 } },
  ]);
  const { buildAttemptUsageMarkup: buWith } = vm.runInNewContext(
    `${wbSource}; ${inspSource}; ({buildAttemptUsageMarkup})`,
    ctxWith,
  );
  const act1 = { id: 'act-1', nodeId: 'n', state: 'succeeded' };
  const html1 = buWith(act1);
  assert.match(html1, /Token usage/, 'section heading present');
  assert.match(html1, /<tr><td>1<\/td>/, 'first attempt labelled correctly');
  assert.match(html1, /1\.2K/, 'input formatted with K suffix');
  assert.match(html1, /800/, 'output shown');
  assert.match(html1, /300/, 'cached shown');

  // Retry: second attempt labelled sequentially (no redundant "Retry N (attempt N+1)" text)
  const ctxRetry = makeInspCtx([
    { activationId: 'act-2', retryNumber: 0, state: 'failed',    usage: { input: 500, output: 200, cached: 0 } },
    { activationId: 'act-2', retryNumber: 1, state: 'succeeded', usage: null },
  ]);
  const { buildAttemptUsageMarkup: buRetry } = vm.runInNewContext(
    `${wbSource}; ${inspSource}; ({buildAttemptUsageMarkup})`,
    ctxRetry,
  );
  const act2 = { id: 'act-2', nodeId: 'n', state: 'succeeded' };
  const htmlRetry = buRetry(act2);
  assert.match(htmlRetry, /<tr><td>1<\/td>/, 'first attempt label');
  assert.match(htmlRetry, /<tr><td>2<\/td>/, 'retry shown as Attempt 2, not "Retry 1 (attempt 2)"');
  assert.doesNotMatch(htmlRetry, /Retry \d/, 'no redundant Retry N label');
  assert.match(htmlRetry, /<tr><td>2<\/td><td colspan="3"[^>]*>—<\/td><\/tr>/, 'null usage attempt has one unavailable cell spanning all token columns');
  assert.match(htmlRetry, /500/, 'first attempt usage shown');
  // The "Unavailable" row must not contain a numeric cell — it uses colspan.
  assert.doesNotMatch(htmlRetry, /<tr><td>2<\/td><td class="num">/, 'unavailable row must not render as numeric cells');

  // All attempts have null usage → section exists but all rows say "Unavailable"
  const ctxAllNull = makeInspCtx([
    { activationId: 'act-3', retryNumber: 0, state: 'succeeded', usage: null },
  ]);
  const { buildAttemptUsageMarkup: buAllNull } = vm.runInNewContext(
    `${wbSource}; ${inspSource}; ({buildAttemptUsageMarkup})`,
    ctxAllNull,
  );
  const htmlAllNull = buAllNull({ id: 'act-3', nodeId: 'n', state: 'succeeded' });
  assert.match(htmlAllNull, /Token usage/, 'section still rendered when all unavailable');
  assert.match(htmlAllNull, /<td colspan="3"[^>]*>—<\/td>/, 'row marks all three token values unavailable');
  // Must NOT include a numeric usage table with zeros
  assert.doesNotMatch(htmlAllNull, /<td class="num">0<\/td>/, 'unavailable must not render as zero');

  const ctxPartial = makeInspCtx([
    { activationId: 'act-partial', retryNumber: 0, state: 'succeeded', usage: { input: 0, output: null, cached: null } },
  ]);
  const { buildAttemptUsageMarkup: buPartial } = vm.runInNewContext(
    `${wbSource}; ${inspSource}; ({buildAttemptUsageMarkup})`,
    ctxPartial,
  );
  const htmlPartial = buPartial({ id: 'act-partial', nodeId: 'n', state: 'succeeded' });
  assert.match(htmlPartial, /<td class="num">0<\/td>/, 'reported zero input stays zero');
  assert.doesNotMatch(htmlPartial, /<td class="num">0<\/td><td class="num">0<\/td><td class="num">0<\/td>/,
    'missing output and cached must not render as zero');
  assert.match(htmlPartial, /<td class="num">—<\/td>/g);

  for (const usage of [null, { input: 0, output: 0, cached: 0 }]) {
    const context = makeInspCtx([
      { activationId: 'agent', executor: 'custom/agent', retryNumber: 1, state: 'succeeded', usage },
    ]);
    const render = vm.runInNewContext(`${wbSource}; ${inspSource}; buildAttemptUsageMarkup`, context);
    const html = render({ id: 'agent' });
    if (usage === null) {
      assert.match(html, /<td colspan="3"[^>]*>—<\/td>/);
      assert.doesNotMatch(html, /<td class="num">0<\/td>/);
    } else {
      assert.doesNotMatch(html, /Unavailable/);
      assert.equal((html.match(/<td class="num">0<\/td>/g) || []).length, 3,
        'actual reported zero remains distinct from missing usage');
    }
  }
}
console.log('buildAttemptUsageMarkup checks passed');

// Compact values remain readable at million/billion boundaries.
{
 const format=vm.runInNewContext(`${wbSource};fmtTokens`,makeCtx());
 for(const [value,expected] of [[0,'0'],[999,'999'],[1000,'1.0K'],[1250000,'1.3M'],[999999,'1.0M'],[1500000000,'1.5B']])assert.equal(format(value),expected);
}
