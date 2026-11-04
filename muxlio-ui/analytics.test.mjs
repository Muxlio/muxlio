import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

const analyticsSource = readFileSync(new URL('./js/analytics.js', import.meta.url), 'utf8');
const indexSource = readFileSync(new URL('./index.html', import.meta.url), 'utf8');

// Build a minimal execution context that satisfies analytics.js dependencies.
function makeCtx(overrides = {}) {
  return {
    api: async () => { throw new Error('api not wired in test context'); },
    esc: s => String(s ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])),
    document: { getElementById: () => null },
    ...overrides,
  };
}

// Helper to extract buildAnalyticsModel and rendering helpers from source.
function getHelpers(ctx) {
  return vm.runInNewContext(
    `${analyticsSource}; ({buildAnalyticsModel, fmtTok, barChartMarkup, sparklineMarkup, tokenSparklineMarkup, renderAnalytics, loadAnalyticsData, loadAnalytics})`,
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

  // Page structure checks
  assert.match(content, /summary-grid/, 'summary grid rendered');
  assert.match(content, /Total runs/, 'total runs label present');
  assert.match(content, /Run volume/, 'run volume section present');
  assert.match(content, /Outcomes/, 'outcomes section present');
  assert.match(content, /Token usage by agent/, 'token usage section present');

  // State counts in summary
  assert.match(content, /summary-value green">1</, 'succeeded count shown');
  assert.match(content, /summary-value red">1</, 'failed count shown');

  // Success rate: 1 succeeded / (1 succeeded + 1 failed) = 50%
  assert.match(content, /50%/, 'success rate calculated correctly');

  // Token data for executorA executor
  assert.match(content, /executorA/, 'executorA executor in table');
  assert.match(content, /2\.0K/, 'input token count formatted');

  // Incomplete data note should appear (r2 has null usage)
  assert.match(content, /incomplete token data|provider did not report/, 'incomplete token data note present');

  // Run link to r1 should be included
  assert.match(content, /href="#run\/r1"/, 'link to contributing run r1 present');
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
  assert.match(content, /Total runs/, 'summary grid still rendered for empty state');
  assert.match(content, /<div class="summary-label">Total runs<\/div><div class="summary-value">0/, 'zero total runs shown');
  assert.match(content, /No runs in the last 30 days/, 'empty sparkline message shown');
  assert.match(content, /Token usage by agent/, 'token section present even with no data');
}
console.log('renderAnalytics empty-state checks passed');

// Analytics is discoverable and explains missing data when no usage exists.
{
  assert.match(indexSource, /href="#analytics" id="analyticsNav"/, 'analytics navigation link is present');
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
  assert.match(output.value, /no attempt records/);
  assert.match(output.value, /no provider-reported token usage/);
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
  assert.match(output.innerHTML, /200 ms \(n=2\)/);
  assert.match(output.innerHTML, /4.0 s \(n=1\)/);
  assert.match(output.innerHTML, /0 ms \(n=1\)/);
  assert.match(output.innerHTML, /1 missing timing; 0 in progress/);
  assert.match(output.innerHTML, /Unavailable/);
  assert.match(output.innerHTML, /href="#run\/mixed%2Frun"/);
  assert.doesNotMatch(output.innerHTML, /NaN/);
}
console.log('mixed executor timing, legacy and partial usage checks passed');

// Exercise script order: boot must not replace the actual Analytics loader.
{
  const boot = readFileSync(new URL('./js/boot.js', import.meta.url), 'utf8');
  const output = {innerHTML:''};
  let resolveRuns;
  const pending = new Promise(resolve => { resolveRuns = resolve; });
  const ctx = vm.createContext(makeCtx({
    api: async path => path === '/v1/runs' ? pending : {attempts:[]},
    document: {getElementById: () => output, querySelectorAll: () => []},
    window: {addEventListener(){}}, navigate(){},
    loadOverview(){}, loadRuns(){}, loadWorkers(){}, loadOlder(){},
  }));
  const sourceByPath = new Map([['/js/analytics.js', analyticsSource], ['/js/boot.js', boot]]);
  for (const match of indexSource.matchAll(/<script src="([^"]+)"/g)) {
    if (sourceByPath.has(match[1])) vm.runInContext(sourceByPath.get(match[1]), ctx);
  }
  const loading = ctx.loadAnalytics();
  assert.match(output.innerHTML, /Loading analytics/);
  resolveRuns({runs:[{id:'r',state:'succeeded'}]});
  await loading;
  assert.match(output.innerHTML, /Total runs/);
}
console.log('Analytics bootstrap and loading checks passed');

{
  const output = {innerHTML:''};
  const {loadAnalyticsData, loadAnalytics} = getHelpers(makeCtx({
    document: {getElementById: () => output},
    api: async path => {
      if (path === '/v1/runs') return {runs:[{id:'good',state:'succeeded'},{id:'bad',state:'failed'}]};
      if (path.includes('/bad/')) throw new Error('service <offline>');
      return {attempts:[{executor:'executorZ',state:'succeeded',executionDurationMs:10}]};
    },
  }));
  const model = await loadAnalyticsData();
  assert.equal(model.runs.length, 2);
  assert.equal(model.attemptErrors.size, 1);
  assert.equal(model.runsWithNoAttempts, 0, 'failed fetch is not an empty attempt history');
  await loadAnalytics();
  assert.match(output.innerHTML, /statistics are partial/);
  assert.match(output.innerHTML, /service &lt;offline&gt;/);
  assert.match(output.innerHTML, /executorZ/);
  assert.match(output.innerHTML, /href="#run\/bad"/);

  for (const api of [
    async () => {throw new Error('runs unavailable');},
    async () => ({}),
    async path => path === '/v1/runs' ? {runs:[{id:'bad'}]} : {},
  ]) {
    const helpers = getHelpers(makeCtx({api,document:{getElementById: () => output}}));
    await helpers.loadAnalytics();
    assert.match(output.innerHTML, /role="alert"/);
    assert.doesNotMatch(output.innerHTML, /Loading analytics/);
  }
}
console.log('Analytics fetch failure checks passed');

// The coordinator serializes empty Go slices as null.
{
  const {loadAnalyticsData} = getHelpers(makeCtx({api:async () => ({runs:null})}));
  assert.equal((await loadAnalyticsData()).runs.length, 0);
  const helpers = getHelpers(makeCtx({api:async path => path === '/v1/runs' ? {runs:[{id:'legacy'}]} : {attempts:null}}));
  const model = await helpers.loadAnalyticsData();
  assert.equal(model.runsWithNoAttempts, 1);
  assert.equal(model.attemptErrors.size, 0);
}
console.log('Analytics API empty slice checks passed');

// Missing token fields cannot produce a numeric relative-volume bar.
{
  const output = {innerHTML:''};
  const {buildAnalyticsModel, renderAnalytics} = getHelpers(makeCtx({document:{getElementById:()=>output}}));
  const runs = [{id:'partial',state:'succeeded'}, {id:'zero',state:'succeeded'}];
  const model = buildAnalyticsModel(runs, new Map([
    ['partial', [{executor:'partialExecutor',state:'succeeded',usage:{input:100}}]],
    ['zero', [{executor:'zeroExecutor',state:'succeeded',usage:{input:0,output:0,cached:0}}]],
  ]));
  renderAnalytics(model);
  const row = name => output.innerHTML.match(new RegExp(`<tr>\\s*<td[^>]*><strong>${name}</strong>[\\s\\S]*?</tr>`, 'g')).at(-1);
  assert.match(row('partialExecutor'), /<td>Unavailable<\/td>/);
  assert.doesNotMatch(row('partialExecutor'), /abc-bar/);
  assert.match(row('zeroExecutor'), /width:0\.0%/);
}
console.log('Partial token relative-volume checks passed');

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

// Revisiting Analytics can overlap loads; old successes and failures must not
// overwrite the latest request's data or loading state.
for (const staleFailure of [false, true]) {
  const output = {innerHTML:''};
  const pending = [];
  const {loadAnalytics} = getHelpers(makeCtx({
    document:{getElementById:()=>output},
    api:()=>new Promise((resolve,reject)=>pending.push({resolve,reject})),
  }));
  const oldLoad = loadAnalytics();
  const newLoad = loadAnalytics();
  pending[1].resolve({runs:[]});
  await newLoad;
  const latestHTML = output.innerHTML;
  if (staleFailure) pending[0].reject(new Error('stale failure'));
  else pending[0].resolve({runs:[{id:'stale',state:'succeeded'}]});
  // The stale successful load still finishes its attempt request.
  if (!staleFailure) {
    await new Promise(resolve=>setImmediate(resolve));
    pending[2].resolve({attempts:[]});
  }
  await oldLoad;
  assert.equal(output.innerHTML, latestHTML);
}
console.log('Analytics overlapping-load checks passed');
