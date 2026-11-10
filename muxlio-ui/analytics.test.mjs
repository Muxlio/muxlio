import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

const analyticsSource = [
  './js/format.js',
  './js/usage-summary.js',
  './js/analytics-accounting.js',
  './js/analytics-filters.js',
  './js/analytics-retry-latency.js',
  './js/analytics.js',
].map(p => readFileSync(new URL(p, import.meta.url), 'utf8')).join('\n');
const indexSource = readFileSync(new URL('./index.html', import.meta.url), 'utf8');

// Build a minimal execution context that satisfies analytics.js dependencies.
function makeCtx(overrides = {}) {
  return {
    api: async () => { throw new Error('api not wired in test context'); },
    esc: s => String(s ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])),
    document: { getElementById: () => null },
    URLSearchParams,
    AbortController,
    Intl,
    location: { pathname: '/analytics', search: '' }, currentRoute:()=>'/analytics',
    ...overrides,
  };
}

// Helper to extract buildAnalyticsModel and rendering helpers from source.
function getHelpers(ctx) {
  return vm.runInNewContext(
    `${analyticsSource}; ({buildAnalyticsModel, fmtTok, barChartMarkup, sparklineMarkup, tokenSparklineMarkup, analyticsTokenVolume, renderAnalytics, loadAnalyticsData, loadAnalytics, analyticsSummarizeUsage, analyticsAttemptBucketTime, analyticsMeasureCatalog, analyticsFiltersFromURL, analyticsDefaultFilters, analyticsResolveRange, analyticsRangeThroughNow, analyticsRunInRange, analyticsDayBuckets, analyticsBucketForTimestamp, analyticsDateTimeLocalValue, buildAnalyticsRetryLatency, analyticsAttemptRecoveredFailure, analyticsAttemptTiming, analyticsPercentile, analyticsDrillRecords})`,
    ctx,
  );
}

// ── fmtTok ──────────────────────────────────────────────────────────────
{
  const { fmtTok } = getHelpers(makeCtx());
  assert.strictEqual(fmtTok(0), '0');
  assert.strictEqual(fmtTok(999), '999');
  assert.strictEqual(fmtTok(1000), '1.0K');
  assert.strictEqual(fmtTok(1500), '1.5K');
  assert.strictEqual(fmtTok(1_000_000), '1.0M');
  assert.strictEqual(fmtTok(2_500_000), '2.5M');
}
console.log('fmtTok checks passed');

// Shared ISO filter state must round-trip through the viewer-local datetime input.
{
  const { analyticsDateTimeLocalValue } = getHelpers(makeCtx());
  const value = new Date(2026, 5, 2, 15, 7).toISOString();
  const expected = `${value ? new Date(value).getFullYear() : ''}-${String(new Date(value).getMonth() + 1).padStart(2, '0')}-${String(new Date(value).getDate()).padStart(2, '0')}T${String(new Date(value).getHours()).padStart(2, '0')}:${String(new Date(value).getMinutes()).padStart(2, '0')}`;
  assert.equal(analyticsDateTimeLocalValue(value), expected);
  assert.equal(analyticsDateTimeLocalValue('invalid'), '');
}
console.log('custom range local display checks passed');

// ── buildAnalyticsModel — empty state ──────────────────────────────────
{
  const { buildAnalyticsModel } = getHelpers(makeCtx());
  const model = buildAnalyticsModel([], new Map());
  assert.strictEqual(model.runs.length, 0);
  assert.strictEqual(model.stateCounts.succeeded, 0);
  assert.strictEqual(model.stateCounts.failed, 0);
  assert.strictEqual(model.totals, null, 'no runs → no token totals');
  assert.strictEqual(model.executorUsage.size, 0);
  assert.strictEqual(model.runsWithNoAttempts, 0);
}
console.log('buildAnalyticsModel empty checks passed');

// ── buildAnalyticsModel — run volume by state ──────────────────────────
{
  const { buildAnalyticsModel } = getHelpers(makeCtx());
  const now = new Date().toISOString();
  const runs = [
    { id: 'r1', state: 'succeeded', workflowName: 'w', createdAt: now, updatedAt: now },
    { id: 'r2', state: 'succeeded', workflowName: 'w', createdAt: now, updatedAt: now },
    { id: 'r3', state: 'failed',    workflowName: 'w', createdAt: now, updatedAt: now },
    { id: 'r4', state: 'running',   workflowName: 'w', createdAt: now, updatedAt: now },
    { id: 'r5', state: 'cancelled', workflowName: 'w', createdAt: now, updatedAt: now },
  ];
  const model = buildAnalyticsModel(runs, new Map(runs.map(r => [r.id, []])));
  assert.strictEqual(model.stateCounts.succeeded, 2);
  assert.strictEqual(model.stateCounts.failed, 1);
  assert.strictEqual(model.stateCounts.running, 1);
  assert.strictEqual(model.stateCounts.cancelled, 1);
  // 5 runs with empty attempts → all count as no-attempt runs
  assert.strictEqual(model.runsWithNoAttempts, 5);
}
console.log('buildAnalyticsModel state counts passed');

{
  const { buildAnalyticsModel } = getHelpers(makeCtx());
  const now = new Date().toISOString();
  const runs = [{ id: 'r1', state: 'succeeded', workflowName: 'w', createdAt: now }];
  const model = buildAnalyticsModel(runs, new Map([['r1', []]]), new Map(), {
    totalRuns: 250,
    stateCounts: { succeeded: 200, failed: 50 },
  });
  assert.equal(model.stateCounts.succeeded, 200, 'server stateCounts used when loaded runs are a proper subset');
  assert.equal(model.stateCounts.failed, 50);
}
console.log('buildAnalyticsModel server stateCounts passed');

// ── buildAnalyticsModel — token usage by executor ─────────────────────
{
  const { buildAnalyticsModel } = getHelpers(makeCtx());
  const now = new Date().toISOString();
  const runs = [
    { id: 'r1', state: 'succeeded', workflowName: 'w', createdAt: now, updatedAt: now },
    { id: 'r2', state: 'succeeded', workflowName: 'w', createdAt: now, updatedAt: now },
    { id: 'r3', state: 'failed',    workflowName: 'w', createdAt: now, updatedAt: now },
    { id: 'r-running', state: 'running', workflowName: 'w', createdAt: now, updatedAt: now },
  ];
  const attemptsByRun = new Map([
    ['r1', [
      { id: 'a1', activationId: 'act-1', executor: 'executorA', retryNumber: 0, state: 'succeeded',
        usage: { input: 1000, output: 400, cached: 100 } },
    ]],
    ['r2', [
      { id: 'a2', activationId: 'act-2', executor: 'executorA', retryNumber: 0, state: 'succeeded',
        usage: { input: 500, output: 200, cached: 0 } },
      { id: 'a3', activationId: 'act-3', executor: 'executorB', retryNumber: 0, state: 'succeeded',
        usage: { input: 300, output: 100, cached: 50 } },
    ]],
    ['r-running', [{ id: 'a-running', activationId: 'act-running', executor: 'executorA', retryNumber: 0, state: 'running',
      usage: { input: 9000, output: 9000, cached: 0 } }]],
    // r3 has attempt with null usage — provider did not report
    ['r3', [
      { id: 'a4', activationId: 'act-4', executor: 'executorA', retryNumber: 0, state: 'failed', usage: null },
    ]],
  ]);
  const model = buildAnalyticsModel(runs, attemptsByRun);

  // executorA should aggregate r1 + r2 attempts only (r3 had null usage)
  const executorA = model.executorUsage.get('executorA');
  assert.ok(executorA, 'executorA executor entry exists');
  assert.strictEqual(executorA.input, 1000 + 500, 'executorA input summed across runs');
  assert.strictEqual(executorA.output, 400 + 200, 'executorA output summed');
  assert.strictEqual(executorA.cached, 100 + 0, 'executorA cached summed');
  assert.ok(executorA.runIds.has('r1'), 'r1 contributed to executorA');
  assert.ok(executorA.runIds.has('r2'), 'r2 contributed to executorA');
  assert.ok(!executorA.runIds.has('r3'), 'r3 NOT counted for executorA (null usage)');

  const executorB = model.executorUsage.get('executorB');
  assert.ok(executorB, 'executorB executor entry exists');
  assert.strictEqual(executorB.input, 300);
  assert.strictEqual(executorB.output, 100);
  assert.strictEqual(executorB.cached, 50);

  // r3 has attempt but null usage → runsWithUnavailableUsage
  assert.strictEqual(model.runsWithUnavailableUsage, 1, 'one run with no reported usage');
  assert.strictEqual(model.runsWithNoAttempts, 0, 'no run completely lacks attempts');

  // Totals should include all reported usage
  assert.ok(model.totals !== null, 'totals non-null when usage reported');
  assert.strictEqual(model.totals.input, 1000 + 500 + 300);
  assert.strictEqual(model.totals.output, 400 + 200 + 100);
  assert.strictEqual(model.totals.cached, 100 + 0 + 50);
}
console.log('buildAnalyticsModel token usage by executor passed');

// ── null usage is distinct from zero usage ────────────────────────────
{
  const { buildAnalyticsModel } = getHelpers(makeCtx());
  const now = new Date().toISOString();
  const runs = [
    { id: 'r1', state: 'succeeded', workflowName: 'w', createdAt: now, updatedAt: now },
    { id: 'r2', state: 'succeeded', workflowName: 'w', createdAt: now, updatedAt: now },
  ];
  const attemptsByRun = new Map([
    // r1 has zero usage reported (e.g. cached-only call)
    ['r1', [{ id: 'a1', activationId: 'act-1', executor: 'agent', retryNumber: 0, state: 'succeeded',
              usage: { input: 0, output: 0, cached: 0 } }]],
    // r2 has null usage (provider did not report)
    ['r2', [{ id: 'a2', activationId: 'act-2', executor: 'agent', retryNumber: 0, state: 'succeeded',
              usage: null }]],
  ]);
  const model = buildAnalyticsModel(runs, attemptsByRun);
  // agent entry should exist (from r1 with zero usage)
  const agent = model.executorUsage.get('agent');
  assert.ok(agent, 'agent entry exists for run with zero-reported usage');
  assert.ok(agent.runIds.has('r1'), 'r1 counted (zero is not null)');
  assert.ok(!agent.runIds.has('r2'), 'r2 NOT counted for agent (null usage)');
  // totals should reflect r1 (zeros are valid, not missing)
  assert.ok(model.totals !== null, 'zero-reported usage still produces non-null totals');
  assert.strictEqual(model.runsWithUnavailableUsage, 1, 'r2 counted as unavailable');
  assert.strictEqual(model.runsWithNoAttempts, 0);
}
console.log('null vs zero usage distinction passed');

// ── buildAnalyticsModel — daily buckets ───────────────────────────────
{
  const { buildAnalyticsModel } = getHelpers(makeCtx());
  const todayKey = new Date().toISOString().slice(0, 10);
  const runs = [
    { id: 'r1', state: 'succeeded', workflowName: 'w', createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() },
    { id: 'r2', state: 'failed',    workflowName: 'w', createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() },
  ];
  const model = buildAnalyticsModel(runs, new Map(runs.map(r => [r.id, []])));
  // Buckets span 30 days; today's bucket should have totals
  const todayBucket = model.buckets.find(b => b.dateKey === todayKey);
  assert.ok(todayBucket, 'today bucket exists in 30-day range');
  assert.strictEqual(todayBucket.total, 2, 'today bucket has both runs');
  assert.strictEqual(todayBucket.succeeded, 1);
  assert.strictEqual(todayBucket.failed, 1);
}
console.log('daily bucket aggregation passed');

// ── renderAnalytics — HTML structure ──────────────────────────────────
{
  let content = '';
  const ctx = makeCtx({
    document: {
      getElementById(id) {
        if (id === 'analyticsContent') return {
          set innerHTML(v) { content = v; },
          get innerHTML() { return content; },
        };
        return null;
      },
    },
  });
  const { buildAnalyticsModel, renderAnalytics } = getHelpers(ctx);

  const now = new Date().toISOString();
  const runs = [
    { id: 'r1', state: 'succeeded', workflowName: 'my-workflow', createdAt: now, updatedAt: now },
    { id: 'r2', state: 'failed',    workflowName: 'my-workflow', createdAt: now, updatedAt: now },
  ];
  const attemptsByRun = new Map([
    ['r1', [{ id: 'a1', activationId: 'act-1', executor: 'executorA', retryNumber: 0, state: 'succeeded',
              usage: { input: 2000, output: 800, cached: 200 } }]],
    ['r2', [{ id: 'a2', activationId: 'act-2', executor: 'executorA', retryNumber: 0, state: 'failed',
              usage: null }]],
  ]);
  const model = buildAnalyticsModel(runs, attemptsByRun);
  renderAnalytics(model);

  // Charts carry the current page's totals, outcomes, timing and usage views.
  assert.match(content, /analytics-chart-grid/, 'chart grid rendered');
  assert.match(content, /Median worker time · executor/, 'executor latency chart present');
  assert.match(content, /Run outcomes · 30 days/, 'run outcomes section present');
  assert.match(content, /Usage by executor/, 'executor usage chart present');
  assert.match(content, /Median worker time · workflow/, 'workflow latency chart present');
  assert.match(content, /Run outcomes/, 'outcomes section present');
  assert.equal(model.runs.length,2, 'total runs retained');
  assert.equal(model.stateCounts.succeeded,1);
  assert.equal(model.stateCounts.failed,1);
  assert.match(content, /2 runs \(1 succeeded, 1 failed\)/, 'run volume chart exposes both state counts');
  assert.match(content, /<h3 class="analytics-breakdown-title">Run outcomes<\/h3>/, 'run outcome chart renders');
  assert.match(content, /executorA/, 'executor is identified in charts');
  assert.equal(model.executorUsage.get('executorA').input,2000);
  assert.equal(model.executorUsage.get('executorA').output,800);
  assert.equal(model.executorUsage.get('executorA').cached,200);
  assert.doesNotMatch(content, /incomplete token data|provider did not report/, 'text summary is omitted from the graph-led view');
  assert.match(content,/data-analytics-drill="executor:executorA"/, 'executor opens contributing attempts');
  renderAnalytics(model,{drill:'executor:executorA'});
  assert.match(content, /<dialog[^>]*aria-labelledby="analyticsDrillTitle"/);
  assert.match(content, /href="\/runs\/r1"/, 'link to contributing run present');
  assert.equal(model.runsWithUnavailableUsage,1, 'incomplete token records remain tracked');
  assert.equal(model.executorStats.get('executorA').missingUsage,1);
  assert.ok(model.executorUsage.get('executorA').runIds.has('r1'), 'contributing run identity retained');
  assert.ok(!content.includes('<script>'), 'no script injection possible');
}
console.log('renderAnalytics HTML structure checks passed');

// ── renderAnalytics — no runs ──────────────────────────────────────────
{
  let content = '';
  const ctx = makeCtx({
    document: {
      getElementById(id) {
        if (id === 'analyticsContent') return {
          set innerHTML(v) { content = v; },
          get innerHTML() { return content; },
        };
        return null;
      },
    },
  });
  const { buildAnalyticsModel, renderAnalytics } = getHelpers(ctx);
  const model = buildAnalyticsModel([], new Map());
  renderAnalytics(model);
  assert.match(content, /analytics-chart-grid/, 'chart grid rendered for empty state');
  assert.equal(model.runs.length,0, 'zero total runs');
  assert.doesNotMatch(content, /No attempts loaded yet/, 'empty analytics avoids the removed prose summary');
  assert.match(content, /No runs in the last 30 days/, 'empty sparkline message shown');
  assert.doesNotMatch(content, /No attempts loaded yet/, 'no-attempt state keeps the graph-led layout');
}
console.log('renderAnalytics empty-state checks passed');

// Analytics is discoverable and explains missing data when no usage exists.
{
  assert.match(indexSource, /href="\/analytics" id="analyticsNav"/, 'analytics navigation link is present');
  const { buildAnalyticsModel, renderAnalytics } = getHelpers(makeCtx());
  const now = new Date().toISOString();
  const runs = [
    { id: 'old', state: 'succeeded', createdAt: now },
    { id: 'unreported', state: 'failed', createdAt: now },
  ];
  const model = buildAnalyticsModel(runs, new Map([
    ['old', []],
    ['unreported', [{ executor: 'agent', usage: null }]],
  ]));
  assert.equal(model.runsWithNoAttempts, 1);
  assert.equal(model.runsWithUnavailableUsage, 1);
  const output = { value: '', set innerHTML(value) { this.value = value; } };
  renderAnalytics(model);
  const emptyModel = buildAnalyticsModel(runs, new Map([['old', []], ['unreported', [{ usage: null }]]]));
  vm.runInNewContext(`${analyticsSource}; renderAnalytics(model)`, {
    ...makeCtx({ document: { getElementById: () => output } }), model: emptyModel,
  });
  assert.doesNotMatch(output.value, /no attempt records|no provider-reported token usage/);
  assert.equal(emptyModel.runsWithNoAttempts,1, 'missing attempt history remains distinct');
  assert.equal(emptyModel.runsWithUnavailableUsage,1, 'unreported usage remains distinct');
  assert.equal(emptyModel.totals,null, 'missing usage cannot become numeric totals');
  assert.match(output.value, /aria-label="No reported data"/, 'missing measurements are labelled in the charts');
  assert.doesNotMatch(output.value, /spark-seg tok-/, 'no numeric token bars when usage is unavailable');
}
console.log('analytics navigation and incomplete usage checks passed');

// ── XSS: executor name and workflow name are escaped ──────────────────
{
  let content = '';
  const ctx = makeCtx({
    document: {
      getElementById(id) {
        if (id === 'analyticsContent') return {
          set innerHTML(v) { content = v; },
          get innerHTML() { return content; },
        };
        return null;
      },
    },
  });
  const { buildAnalyticsModel, renderAnalytics } = getHelpers(ctx);
  const now = new Date().toISOString();
  const runs = [
    { id: 'r-xss', state: 'succeeded', workflowName: '<script>xss</script>', createdAt: now, updatedAt: now },
  ];
  const attemptsByRun = new Map([
    ['r-xss', [{ id: 'ax', activationId: 'act-x', executor: '<img onerror=x src=x>', retryNumber: 0, state: 'succeeded',
                 usage: { input: 1, output: 1, cached: 0 } }]],
  ]);
  const model = buildAnalyticsModel(runs, attemptsByRun);
  renderAnalytics(model);
  assert.ok(!content.includes('<script>xss'), 'workflow name XSS escaped');
  assert.ok(!content.includes('<img onerror'), 'executor name XSS escaped');
  assert.match(content, /&lt;script&gt;xss/, 'workflow name HTML-escaped');
}
console.log('XSS escaping checks passed');

// Timing samples stay independent of token availability and coordinator elapsed.
{
  const output = { innerHTML: '' };
  const { buildAnalyticsModel, renderAnalytics } = getHelpers(makeCtx({ document: { getElementById: () => output } }));
  const runs = [{id:'mixed/run', state:'succeeded', createdAt:'invalid'}, {id:'legacy', state:'created'}];
  const model = buildAnalyticsModel(runs, new Map([
    ['mixed/run', [
      {executor:'executorA', state:'succeeded', executionDurationMs:100, usage:null},
      {executor:'executorA', state:'failed', executionDurationMs:300, usage:{input:0,output:0,cached:0}},
      {executor:'executorA', state:'succeeded', startedAt:'2026-01-01T00:00:00Z', finishedAt:'2026-01-01T00:00:04Z'},
      {executor:'executorA', state:'failed', executionDurationMs:-1, startedAt:'bad', finishedAt:'bad'},
      {executor:'executorB', state:'running'},
      {executor:'executorC', state:'succeeded', executionDurationMs:0, usage:{input:7}},
    ]],
    ['legacy', []],
  ]));
  const a = model.executorStats.get('executorA');
  assert.equal(a.attempts, 4);
  assert.equal(a.durationSumMs / a.durationSamples, 200);
  assert.equal(a.elapsedSumMs / a.elapsedSamples, 4000);
  assert.equal(a.missingTiming, 1);
  assert.equal(model.executorStats.get('executorB').attempts, 1);
  assert.equal(model.executorStats.get('executorB').running, 1);
  assert.equal(model.executorStats.get('executorC').durationSamples, 1);
  assert.equal(model.executorUsage.get('executorC').output, null);
  assert.equal(model.runsWithNoAttempts, 1);
  renderAnalytics(model);
  assert.match(output.innerHTML, /200 ms/);
  assert.equal(a.elapsedSumMs/a.elapsedSamples,4000);
  assert.match(output.innerHTML, /0 ms/);
  assert.equal(a.missingTiming,1);
  assert.match(output.innerHTML, /Unavailable/);
  renderAnalytics(model,{drill:'executor:executorA'});
  assert.match(output.innerHTML, /href="\/runs\/mixed%2Frun"/);
  assert.equal(a.taskTimeTotal,4400, 'chart timing includes worker and legacy samples');
  assert.equal(a.taskTimeCount,3);
  assert.equal(a.taskTimeTotal/a.taskTimeCount,4400/3);
  assert.equal(a.taskTimeMin,100);
  assert.equal(a.taskTimeMax,4000);
  assert.match(output.innerHTML, /0 ms/, 'reported zero duration shown');
  assert.match(output.innerHTML, /Unavailable/, 'missing timing remains unavailable');
  assert.ok(a.runIds.has('mixed/run'), 'contributing run identity retained');
  assert.doesNotMatch(output.innerHTML, /NaN/);
}
console.log('mixed executor timing, legacy and partial usage checks passed');

// boot must not replace the actual Analytics loader.
{
  const boot = readFileSync(new URL('./js/boot.js', import.meta.url), 'utf8');
  assert.doesNotMatch(boot, /function loadAnalytics\b/, 'boot must not redefine Analytics loader');
  assert.match(indexSource, /analytics-filters\.js/, 'filter module is wired from index');
}
{
  const output = { value: '', set innerHTML(v) { this.value = v; }, get innerHTML() { return this.value; } };
  let resolveRuns;
  const pending = new Promise(resolve => { resolveRuns = resolve; });
  const { loadAnalytics } = getHelpers(makeCtx({
    location: { pathname: '/analytics', search: '' }, currentRoute:()=>'/analytics',
    document: {
      getElementById(id) { return id === 'analyticsContent' ? output : null; },
      querySelector: () => null,
      querySelectorAll: () => [],
    },
    api: async path => {
      if (String(path).startsWith('/v1/analytics')) throw new Error('HTTP 404');
      if (path === '/v1/runs') return pending;
      return { attempts: [] };
    },
  }));
  const loading = loadAnalytics();
  assert.match(output.innerHTML, /Loading analytics/);
  resolveRuns({ runs: [{ id: 'r', state: 'succeeded', createdAt: new Date().toISOString() }] });
  await loading;
  assert.match(output.innerHTML, /Run outcomes · 30 days/);
}
console.log('Analytics bootstrap and loading checks passed');

{
  const output = {innerHTML:''};
  const {loadAnalyticsData, loadAnalytics} = getHelpers(makeCtx({
    document: {getElementById: () => output},
    api: async path => {
      if (String(path).startsWith('/v1/analytics')) throw new Error('HTTP 404');
      if (path === '/v1/runs') return { runs: [{ id: 'good', state: 'succeeded', createdAt: new Date(Date.now() - 60_000).toISOString() }, { id: 'bad', state: 'failed', createdAt: new Date(Date.now() - 60_000).toISOString() }] };
      if (path.includes('/bad/')) throw new Error('service <offline>');
      return { attempts: [{ executor: 'executorZ', state: 'succeeded', executionDurationMs: 10 }] };
    },
  }));
  const model = await loadAnalyticsData();
  assert.equal(model.runs.length, 2);
  assert.equal(model.attemptErrors.size, 1);
  assert.equal(model.runsWithNoAttempts, 0, 'failed fetch is not an empty attempt history');
  await loadAnalytics();
  assert.match(output.innerHTML, /class="analytics-data-status" role="status"><summary>Partial data · 1 runs/, 'partial fetch results are announced');
  assert.match(output.innerHTML, /Statistics are partial/);
  assert.match(output.innerHTML, /service &lt;offline&gt;/);
  assert.match(output.innerHTML, /href="\/runs\/bad"/);
  assert.equal(model.attemptErrors.get('bad'),'service <offline>', 'failed fetch reason retained');
  assert.match(output.innerHTML, /executorZ/);
  assert.ok(model.attemptErrors.has('bad'), 'failed run identity retained');

  for (const [api,role] of [
    [async () => {throw new Error('runs unavailable');},'alert'],
    [async () => ({}), 'alert'],
    [async path => {
      if (String(path).startsWith('/v1/analytics')) throw new Error('HTTP 404');
      if (path === '/v1/runs') return { runs: [{ id: 'bad', createdAt: new Date().toISOString() }] };
      if (path.includes('/attempts')) throw new Error('attempt unavailable');
      return {};
    }, 'status'],
  ]) {
    const helpers = getHelpers(makeCtx({api,document:{getElementById: () => output}}));
    await helpers.loadAnalytics();
    assert.match(output.innerHTML, new RegExp(`role="${role}"`), 'invalid runs fail the page; invalid attempts mark partial data');
    assert.doesNotMatch(output.innerHTML, /Loading analytics/);
  }
}
console.log('Analytics fetch failure checks passed');

// The coordinator serializes empty Go slices as null.
{
  const analyticsOrLegacy = async path => {
    if (String(path).startsWith('/v1/analytics')) throw new Error('HTTP 404');
    return ({ runs: null });
  };
  const {loadAnalyticsData} = getHelpers(makeCtx({api: analyticsOrLegacy}));
  assert.equal((await loadAnalyticsData()).runs.length, 0);
  const helpers = getHelpers(makeCtx({api: async path => {
    if (String(path).startsWith('/v1/analytics')) throw new Error('HTTP 404');
    return path === '/v1/runs' ? { runs: [{ id: 'legacy', createdAt: new Date().toISOString() }] } : { attempts: null };
  }}));
  const model = await helpers.loadAnalyticsData();
  assert.equal(model.runsWithNoAttempts, 1);
  assert.equal(model.attemptErrors.size, 0);

  const { analyticsResolveRange, analyticsRangeThroughNow, analyticsRunInRange } = getHelpers(makeCtx());
  const staleRange = analyticsResolveRange({ preset: '30d' }, new Date('2026-06-01T12:00:00.000Z'));
  const run = { id: 'edge', createdAt: '2026-06-01T12:00:00.050Z' };
  assert.equal(analyticsRunInRange(run, staleRange), false, 'stale upper bound can exclude just-created runs');
  const includeRange = analyticsRangeThroughNow(staleRange, new Date('2026-06-01T12:00:00.050Z'));
  assert.equal(analyticsRunInRange(run, includeRange), true, 'legacy load widens the include window through now');
}
console.log('Analytics API empty slice checks passed');

// Missing token fields cannot produce a numeric relative-volume bar.
{
  const output = {innerHTML:''};
  const {buildAnalyticsModel, renderAnalytics, analyticsTokenVolume} = getHelpers(makeCtx({document:{getElementById:()=>output}}));
  const runs = [{id:'partial',state:'succeeded'}, {id:'zero',state:'succeeded'}];
  const model = buildAnalyticsModel(runs, new Map([
    ['partial', [{executor:'partialExecutor',state:'succeeded',usage:{input:100}}]],
    ['zero', [{executor:'zeroExecutor',state:'succeeded',usage:{input:0,output:0,cached:0}}]],
  ]));
  renderAnalytics(model);
  assert.equal(analyticsTokenVolume(100,null,null),null,'partial token data has no inferred total');
  assert.deepEqual(JSON.parse(JSON.stringify(analyticsTokenVolume(0,0,0))),{volume:0,nonCachedInput:0,cached:0,output:0},'reported zeros remain zero');
  assert.match(output.innerHTML,/Attempts by executor/);
}
console.log('Partial token relative-volume checks passed');

// Missing token fields and reported zero retain distinct chart values.
{
  const {buildAnalyticsModel, tokenSparklineMarkup} = getHelpers(makeCtx());
  const now = new Date().toISOString();
  const renderUsage = usage => {
    const model = buildAnalyticsModel([{id:'run',state:'succeeded',createdAt:now}], new Map([
      ['run', [{executor:'generic',state:'succeeded',usage}]],
    ]));
    return tokenSparklineMarkup(model.tokenBuckets);
  };
  const partial = renderUsage({input:100});
  assert.match(partial, /in=100 out=Unavailable cached=Unavailable/);
  assert.doesNotMatch(partial, /spark-seg tok-(out|cached)/, 'unknown fields have no numeric bar');
  const zero = renderUsage({input:0,output:0,cached:0});
  assert.match(zero, /in=0 out=0 cached=0/, 'reported zeros stay visible in the chart labels');
  assert.doesNotMatch(zero, /spark-seg tok-/, 'zero values have zero bar height');
}
console.log('Partial token chart checks passed');

// Cached tokens are a subset of input; volume must not count them twice.
{
  const output = { innerHTML: '' };
  const { buildAnalyticsModel, renderAnalytics, tokenSparklineMarkup, analyticsTokenVolume } = getHelpers(makeCtx({ document: { getElementById: () => output } }));
  const usage = { input: 100, output: 20, cached: 30 };
  const runs = [{ id: 'run', state: 'succeeded', createdAt: new Date().toISOString() }];
  const model = buildAnalyticsModel(runs, new Map([
    ['run', [{ executor: 'generic', state: 'succeeded', usage }]],
  ]));
  renderAnalytics(model);
  assert.equal(analyticsTokenVolume(usage.input,usage.output,usage.cached).volume,120,'relative token volume is input+output, not input+output+cached');
  assert.match(output.innerHTML,/Usage by executor/);
  const spark = tokenSparklineMarkup(model.tokenBuckets);
  const bucket = model.tokenBuckets.find(b => b.hasData);
  assert.equal(bucket.input + bucket.output, 120, 'fixture billable volume');
  const heights = [...spark.matchAll(/style="height:(\d+)px/g)].map(m => Number(m[1]));
  const barHeight = heights[0];
  const segSum = heights.slice(1, 4).reduce((a, b) => a + b, 0);
  assert.ok(Math.abs(segSum - barHeight) <= 3, 'stacked segments match billable bar height without double-counting cached');
  assert.match(spark, /in=100 out=20 cached=30/);
}
console.log('Cached subset token volume checks passed');

// UTC labels must match UTC bucket keys even for viewers on a different day.
{
  class ViewerDate extends Date {
    constructor(...args) { super(...(args.length ? args : ['2026-11-04T00:30:00Z'])); }
    toLocaleDateString(locales, options) {
      return super.toLocaleDateString('en-US', {timeZone:'America/Los_Angeles', ...options});
    }
  }
  const {buildAnalyticsModel} = getHelpers(makeCtx({Date:ViewerDate}));
  const runs = [{id:'today',createdAt:'2026-11-04T00:15:00Z'}];
  const model = buildAnalyticsModel(runs, new Map([['today', [{usage:{input:0},state:'succeeded'}]]]));
  for (const buckets of [model.buckets, model.tokenBuckets]) {
    assert.equal(buckets.at(-1).dateKey, '2026-11-04');
    assert.equal(buckets.at(-1).label, 'Nov 4');
  }
  assert.equal(model.buckets.at(-1).total, 1);
  assert.equal(model.tokenBuckets.at(-1).input, 0);
  assert.equal(model.tokenBuckets.at(-1).output, null);
}
console.log('Analytics UTC date checks passed');

// Credits, costs, and measure breakdown use UsageSummary semantics.
{
  const { buildAnalyticsModel, renderAnalytics, analyticsSummarizeUsage } = getHelpers(makeCtx());
  const finished = '2026-06-01T12:00:00Z';
  const attempts = [
    { id: 'a1', executor: 'agent/a', state: 'succeeded', finishedAt: finished,
      usage: { input: 100, output: 40, cached: 10 },
      measurements: { costUsd: 0.2, credit: { unit: 'credit', amount: 0.5 } } },
    { id: 'a2', executor: 'agent/b', state: 'succeeded', finishedAt: finished,
      measurements: { estimatedCostUsd: 0.1, credit: { unit: 'point', amount: 2 } } },
    { id: 'a3', executor: 'agent/a', state: 'running' },
  ];
  const runs = [{ id: 'run-1', workflowName: 'wf', createdAt: '2020-01-01T00:00:00Z' }];
  const model = buildAnalyticsModel(runs, new Map([['run-1', attempts]]));
  assert.ok(model.measures.some(m => m.id === 'tokens'), 'token measure present');
  assert.ok(model.measures.some(m => m.id === 'cost-observed'), 'observed cost measure');
  assert.ok(model.measures.some(m => m.id === 'cost-estimated'), 'estimated cost measure');
  assert.ok(model.measures.some(m => m.id === 'credit:credit'), 'credit unit measure');
  const summary = analyticsSummarizeUsage(attempts);
  assert.equal(summary.tokens.observed, 140);
  assert.equal(summary.costUsd.observed, 0.2);
  assert.equal(summary.costUsd.estimated, 0.1);
  const credit = summary.credits.find(c => c.unit === 'credit');
  assert.equal(credit.observed, 0.5);
  assert.equal(model.bucketProvenance.finishedAt, 2);
  const rollup = model.measureRollups.get('cost-observed');
  assert.equal(rollup.subtotal, 0.2);
  const output = { innerHTML: '' };
  const { renderAnalytics: render } = getHelpers(makeCtx({ document: { getElementById: () => output } }));
  render(model);
  assert.match(output.innerHTML, />Usage</);
  assert.match(output.innerHTML, /Cost · observed/);
  assert.match(output.innerHTML, /analyticsMeasureSelect/);
  render(model,{drill:'executor:'+attempts[0].executor});
  assert.match(output.innerHTML, /href="\/runs\/run-1"/);
}
console.log('analytics credits and cost checks passed');

// Usage buckets prefer attempt finishedAt over run createdAt.
{
  const { buildAnalyticsModel } = getHelpers(makeCtx());
  const day = new Date().toISOString();
  const dayKey = day.slice(0, 10);
  const runs = [{ id: 'r', createdAt: '2020-01-01T00:00:00Z' }];
  const model = buildAnalyticsModel(runs, new Map([
    ['r', [{ state: 'succeeded', finishedAt: day, usage: { input: 5, output: 5, cached: 0 } }]],
  ]));
  const bucket = model.tokenBuckets.find(b => b.dateKey === dayKey);
  assert.ok(bucket?.hasData, 'token bucket uses attempt finishedAt');
  assert.equal(bucket.input, 5);
}
console.log('analytics attempt bucket time checks passed');

// Revisiting Analytics can overlap loads; old successes and failures must not
// overwrite the latest request's data or loading state.
for (const staleFailure of [false, true]) {
  const output = {innerHTML:''};
  const pending = [];
  const signals = [];
  const {loadAnalytics} = getHelpers(makeCtx({
    document:{getElementById:()=>output},
    api:(path, options)=>{ signals.push(options?.signal); return new Promise((resolve,reject)=>pending.push({resolve,reject})); },
  }));
  const oldLoad = loadAnalytics();
  const newLoad = loadAnalytics();
  assert.equal(signals[0].aborted, true, 'starting a newer load aborts the older request');
  pending[1].resolve({ runs: [], attemptsByRun: {} });
  await newLoad;
  const latestHTML = output.innerHTML;
  if (staleFailure) pending[0].reject(new Error('stale failure'));
  else pending[0].resolve({ runs: [{ id: 'stale', state: 'succeeded', createdAt: new Date().toISOString() }], attemptsByRun: { stale: [] } });
  await oldLoad;
  assert.equal(output.innerHTML, latestHTML);
}
console.log('Analytics overlapping-load checks passed');

{
  const { analyticsFiltersFromURL, analyticsDefaultFilters, analyticsResolveRange, analyticsDayBuckets, analyticsBucketForTimestamp, buildAnalyticsModel } = getHelpers(makeCtx());
  const filters = analyticsFiltersFromURL('/analytics?range=7d&state=failed&workflow=demo');
  assert.equal(filters.preset, '7d');
  assert.deepEqual(Array.from(filters.states), ['failed']);
  assert.equal(filters.workflow, 'demo');
  const range = analyticsResolveRange(filters, new Date('2026-06-10T12:00:00Z'));
  assert.equal(range.buckets, 7);
  const buckets = analyticsDayBuckets(range);
  assert.equal(buckets.length, 8, 'partial UTC calendar days receive edge buckets');
  const runTime = '2026-06-09T10:00:00Z';
  const model = buildAnalyticsModel(
    [{ id: 'x', state: 'failed', workflowName: 'demo', createdAt: runTime }],
    new Map([['x', []]]),
    new Map(),
    { filters, range },
  );
  assert.equal(model.runs.length, 1);
  const bucket = analyticsBucketForTimestamp(Date.parse(runTime), range);
  assert.ok(model.buckets.find(b => b.dateKey === bucket.dateKey)?.total, 'run lands in 7d bucket');
}
console.log('analytics filter URL and bucket boundary checks passed');

// Retry, latency distributions, and timing separation.
{
  const { buildAnalyticsModel, buildAnalyticsRetryLatency, analyticsAttemptRecoveredFailure, analyticsAttemptTiming, analyticsPercentile, analyticsDrillRecords, renderAnalytics } = getHelpers(makeCtx());
  assert.equal(analyticsPercentile([100, 200, 300], 0.95), 300);
  assert.equal(analyticsPercentile([], 0.5), null);
  const t = analyticsAttemptTiming({
    executionDurationMs: 50,
    startedAt: '2026-01-01T00:00:10Z',
    finishedAt: '2026-01-01T00:01:10Z',
    queuedAt: '2026-01-01T00:00:00Z',
    workerStartedAt: '2026-01-01T00:00:08Z',
  });
  assert.equal(t.workerMs, 50);
  assert.equal(t.coordinatorMs, 60000);
  assert.equal(t.queueMs, 8000);
  assert.equal(analyticsAttemptTiming({ queuedAt: '2026-01-01T00:00:00Z', startedAt: '2026-01-01T00:00:10Z' }).queueMs, null,
    'coordinator startedAt alone cannot establish actual worker queue delay');
  assert.equal(analyticsAttemptTiming({ executionDurationMs: 0 }).workerMs, 0);
  assert.equal(analyticsAttemptTiming({ startedAt: 'bad', finishedAt: 'bad' }).coordinatorMs, null);

  const runs = [
    { id: 'ok', state: 'succeeded', workflowName: 'wf-a', createdAt: '2026-06-01T00:00:00Z' },
    { id: 'retry', state: 'succeeded', workflowName: 'wf-a', createdAt: '2026-06-01T00:00:00Z' },
    { id: 'fail', state: 'failed', workflowName: 'wf-b', createdAt: '2026-06-01T00:00:00Z' },
    { id: 'cancel', state: 'cancelled', workflowName: 'wf-b', createdAt: '2026-06-01T00:00:00Z' },
  ];
  const attemptsByRun = new Map([
    ['ok', [{ id: 'a0', activationId: 'act', executor: 'exec-a', retryNumber: 0, state: 'succeeded', executionDurationMs: 100 }]],
    ['retry', [
      { id: 'a1', activationId: 'act', executor: 'exec-a', retryNumber: 0, state: 'failed', executionDurationMs: 200,
        usage: { input: 10, output: 5, cached: 0 }, measurements: { costUsd: 0.1 } },
      { id: 'a2', activationId: 'act', executor: 'exec-b', retryNumber: 1, state: 'succeeded', executionDurationMs: 400,
        startedAt: '2026-01-01T00:00:00Z', finishedAt: '2026-01-01T00:00:05Z' },
    ]],
    ['fail', [{ id: 'f1', activationId: 'act', executor: 'exec-a', retryNumber: 0, state: 'failed', executionDurationMs: 300 }]],
    ['cancel', [{ id: 'c1', activationId: 'act', executor: 'exec-a', retryNumber: 0, state: 'cancelled' }]],
  ]);
  const flat = [];
  for (const run of runs) {
    for (const attempt of attemptsByRun.get(run.id) || []) flat.push({ run, attempt });
  }
  const rl = buildAnalyticsRetryLatency(flat, runs);
  assert.equal(rl.runOutcomes.terminalRuns, 4);
  assert.equal(rl.runOutcomes.succeededNoRetry, 1);
  assert.equal(rl.runOutcomes.succeededAfterRetry, 1);
  assert.equal(rl.runOutcomes.finalFailed, 1);
  assert.equal(rl.runOutcomes.cancelledRuns, 1);
  assert.equal(rl.runOutcomes.cancelledAttempts, 1, 'cancelled attempts count on terminal cancelled runs');
  assert.equal(rl.runOutcomes.recoveredFailureAttempts, 1);
  assert.equal(rl.runOutcomes.finalFailureAttempts, 1);
  assert.equal(rl.retryOverhead.extraAttempts, 1);
  assert.equal(rl.retryOverhead.workerMs, 200);
  assert.equal(rl.failedRetriedUsage.reporting, 1);
  assert.ok(rl.contributors.some(c => c.attemptId === 'a2'), 'slow successful attempt appears in contributor list');
  assert.equal(rl.byWorkflow.find(w => w.label === 'wf-a').worker.n, 3);
  assert.equal(rl.byExecutor.find(e => e.label === 'exec-b').coordinator.median, 5000);
  const model = buildAnalyticsModel(runs, attemptsByRun);
  assert.ok(model.retryLatency);
  const output = { innerHTML: '' };
  const { renderAnalytics: renderRetry } = getHelpers(makeCtx({ document: { getElementById: () => output } }));
  renderRetry(model);
  assert.match(output.innerHTML, /Failure, retry, and latency/);
  assert.match(output.innerHTML, /Succeeded · retried/);
  assert.match(output.innerHTML, /data-analytics-drill="latencyWorkflow:wf-a"/);
  assert.match(output.innerHTML, /href="\/runs\/retry"/);

  const activation = [
    { id: 'f0', retryNumber: 0, state: 'failed' },
    { id: 'r1', retryNumber: 1, state: 'running' },
  ];
  assert.equal(
    analyticsAttemptRecoveredFailure(activation[0], activation),
    false,
    'failed attempt is not recovered while a later attempt is still running',
  );

  const wfRuns = [{ id: 'mix', state: 'succeeded', workflowName: 'wf-mix', createdAt: '2026-06-01T00:00:00Z' }];
  const wfAttempts = [
    { id: 'timed', activationId: 'a', executor: 'exec-x', retryNumber: 0, state: 'succeeded', executionDurationMs: 120 },
    { id: 'notimed', activationId: 'b', executor: 'exec-x', retryNumber: 0, state: 'cancelled' },
  ];
  const wfMap = new Map([['mix', wfAttempts]]);
  const wfModel = buildAnalyticsModel(wfRuns, wfMap);
  const latencyDrill = analyticsDrillRecords(wfModel, 'latencyWorkflow:wf-mix');
  assert.equal(latencyDrill.length, 1);
  assert.equal(latencyDrill[0].attempt.id, 'timed');
}
console.log('analytics retry latency checks passed');

assert.match(indexSource, /analytics-retry-latency\.js/, 'retry latency module wired from index');

// Selecting evidence stays local to the loaded dataset, provides context, and restores focus.
{
 let modalOpens=0,focused=0,cancelHandler;const urls=[];
 const close={click(){this.onclick()}},trigger={getAttribute:()=> 'executor:generic/echo',focus:()=>focused++};
 const dialog={showModal(){modalOpens++},addEventListener(type,fn){if(type==='cancel')cancelHandler=fn}};
 const output={innerHTML:'',querySelector(selector){return selector==='#analyticsCloseDrill'?close:selector==='#analyticsDrilldown'?dialog:null},querySelectorAll(selector){return selector==='[data-analytics-drill]'?[trigger]:[]}};
 const ctx=makeCtx({document:{getElementById:()=>output},window:{history:{pushState(_,__,url){urls.push(url)}}}});
 const {buildAnalyticsModel,renderAnalytics,analyticsDefaultFilters}=getHelpers(ctx);
 const time=new Date().toISOString();
 const model=buildAnalyticsModel([{id:'run-a',workflowName:'Build',state:'succeeded',createdAt:time}],new Map([['run-a',[{id:'attempt-a',nodeId:'implement',activationId:'run-a:implement',executor:'generic/echo',state:'succeeded',startedAt:time,finishedAt:time,usage:{input:100,output:20,cached:30}}]]]),new Map(),{filters:analyticsDefaultFilters()});
 renderAnalytics(model);trigger.onclick();
 assert.equal(modalOpens,1);assert.match(output.innerHTML,/Executor · generic\/echo/);assert.match(output.innerHTML,/>implement<\/a>/);
 assert.match(output.innerHTML,/Open run ↗/);assert.match(output.innerHTML,/>120<\/td>/);
 assert.match(urls.at(-1),/^\/analytics\?.*drill=/);assert.doesNotMatch(urls.at(-1),/#/);
 let prevented=false;cancelHandler({preventDefault(){prevented=true}});
 assert.ok(prevented);assert.equal(focused,1);assert.doesNotMatch(output.innerHTML,/<dialog/);assert.doesNotMatch(urls.at(-1),/drill=/);
}
console.log('Analytics evidence panel interactions passed');

{
  let requests = 0;
  const now = new Date().toISOString();
  const api = async path => {
    requests++;
    const url = new URL(path, 'http://local');
    const offset = Number(url.searchParams.get('offset') || 0);
    const total = 250;
    const slice = Array.from({ length: Math.min(200, total - offset) }, (_, i) => ({
      id: `r${offset + i}`,
      state: 'succeeded',
      workflowName: 'w',
      createdAt: now,
    }));
    return {
      runs: slice,
      attemptsByRun: Object.fromEntries(slice.map(r => [r.id, []])),
      totalRuns: total,
      hasMore: offset + slice.length < total,
      stateCounts: { succeeded: total },
    };
  };
  const { loadAnalyticsData, analyticsDefaultFilters } = getHelpers(makeCtx({ api }));
  const model = await loadAnalyticsData(analyticsDefaultFilters());
  assert.equal(requests, 3, 'fetches each analytics page plus the prior-period summary');
  assert.equal(model.runs.length, 250, 'charts aggregate the full filtered population');
  assert.equal(model.totalRuns, 250);
  assert.equal(model.pagesFetched, 2);
  assert.equal(model.buckets.reduce((sum, b) => sum + b.total, 0), 250, 'run volume buckets include all matching runs');
}
console.log('analytics pagination population checks passed');

{
  let requests = 0;
  const api = async path => {
    requests++;
    const url = new URL(path, 'http://local');
    const offset = Number(url.searchParams.get('offset') || 0);
    if (offset === 0) return { runs: [{ id: 'r1', state: 'succeeded', createdAt: new Date().toISOString() }], totalRuns: 3, hasMore: true };
    return { runs: [], totalRuns: 3, hasMore: true };
  };
  const { loadAnalyticsData, analyticsDefaultFilters } = getHelpers(makeCtx({ api }));
  const model = await loadAnalyticsData(analyticsDefaultFilters());
  assert.equal(requests, 3, 'stops fetching pages after no progress, then makes the optional comparison request');
  assert.equal(model.hasMore, true, 'reports partial loaded population when backend says rows remain');
  assert.equal(model.runs.length, 1);
}
console.log('analytics empty-page pagination guard passed');

{
  let calls = 0;
  const now = new Date().toISOString();
  const api = async path => {
    calls++;
    const url = new URL(path, 'http://local');
    if (calls >= 3) {
      return { runs: [], attemptsByRun: {}, totalRuns: 180, hasMore: false };
    }
    const offset = Number(url.searchParams.get('offset') || 0);
    const total = 260;
    const slice = Array.from({ length: Math.min(200, total - offset) }, (_, i) => ({
      id: `r${offset + i}`, state: 'succeeded', workflowName: 'w', createdAt: now,
    }));
    return { runs: slice, attemptsByRun: {}, totalRuns: total, hasMore: offset + slice.length < total };
  };
  const { loadAnalyticsData, analyticsDefaultFilters } = getHelpers(makeCtx({ api }));
  const model = await loadAnalyticsData(analyticsDefaultFilters());
  assert.equal(model.comparison.priorRunCount, 180, 'period comparison uses filtered totals, not page size');
  assert.equal(model.comparison.currentRunCount, 260);
}
console.log('analytics period comparison totals passed');

// Partial pagination must not claim full-population usage totals.
{
  const output = { innerHTML: '' };
  const { buildAnalyticsModel, renderAnalytics, analyticsDefaultFilters } = getHelpers(makeCtx({
    document: { getElementById: () => output },
  }));
  const now = new Date().toISOString();
  const attempts = [
    { id: 'a1', state: 'succeeded', finishedAt: now, usage: { input: 100, output: 50, cached: 0 }, measurements: { costUsd: 0.5 } },
  ];
  const model = buildAnalyticsModel(
    [{ id: 'r1', state: 'succeeded', workflowName: 'w', createdAt: now }],
    new Map([['r1', attempts]]),
    new Map(),
    { totalRuns: 100, hasMore: true, filters: analyticsDefaultFilters() },
  );
  renderAnalytics(model);
  assert.doesNotMatch(output.innerHTML, /full filtered population/, 'partial load must not imply full-population usage totals');
  assert.match(output.innerHTML, /only loaded attempts/, 'partial load explains scoped usage totals');
  const rollup = model.measureRollups.get('cost-observed');
  assert.equal(rollup.subtotal, 0.5, 'rollup reflects loaded attempts only');
}
console.log('analytics partial-scope usage honesty checks passed');
