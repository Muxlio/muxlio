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
    `${analyticsSource}; ({buildAnalyticsModel, fmtTok, barChartMarkup, sparklineMarkup, renderAnalytics, loadAnalyticsData, loadAnalytics, analyticsAttemptBucketTime, analyticsMeasureCatalog, analyticsFiltersFromURL, analyticsDefaultFilters, analyticsResolveRange, analyticsRangeThroughNow, analyticsRunInRange, analyticsDayBuckets, analyticsBucketForTimestamp, analyticsDateTimeLocalValue, buildAnalyticsRetryLatency, analyticsAttemptRecoveredFailure, analyticsAttemptTiming, analyticsPercentile, analyticsDrillRecords, analyticsMeasureExact, analyticsPrecisionWarning, analyticsMeasureValueForAttempt})`,
    ctx,
  );
}

const complete=value=>({collectedSubtotal:value,estimatedFallbackSubtotal:null,knownSubtotal:value,effectiveTotal:value,completeness:'complete',attempts:{complete:1,partial:0,unknown:0,pending:0,inapplicable:0}});
const summary=(value='9007199254740993')=>({tokens:complete(value),tokenDimensions:{input:complete(value)},credits:[{provider:'kiro',unit:'credit',value:complete('0.18065595854063016')}],costs:[{currency:'USD',basis:'api-equivalent',value:complete('0.0062')},{currency:'USD',basis:'plan-debit',value:complete('0.001')}],unattributed:{credits:{unknownAttempts:0,pendingAttempts:0},costs:{unknownAttempts:0,pendingAttempts:0}}});
const run={id:'run-1',workflowName:'<workflow>',state:'succeeded',createdAt:'2026-01-01T00:00:00Z',updatedAt:'2026-01-02T00:00:00Z'};
const attempt={id:'attempt-1',activationId:'act',executor:'<executor>',state:'succeeded',retryNumber:1,startedAt:'2026-01-01T23:00:00Z',finishedAt:'2026-01-02T00:00:00Z',executionDurationMs:1000,accountingSummary:summary()};
const data={accounting:summary(),accountingByAttempt:{'attempt-1':summary()},accountingByDay:{'2026-01-02':summary()},accountingByExecutor:{'<executor>':summary()},accountingByWorkflow:{'<workflow>':summary()}};
const h=getHelpers(makeCtx());
const formatMeasure=vm.runInNewContext(`${analyticsSource};analyticsFormatMeasureValue`,makeCtx());
for(const kind of ['tokens','dimension']){
 for(const [value,expected] of [['1500','1.5K'],['9809008','9.8M'],['2500000000','2.5B'],['0','0'],[null,'Unavailable']])assert.equal(formatMeasure({kind},value),expected);
}
assert.equal(formatMeasure({kind:'credit'},'0.18065595854063016'),'0.18065595854063016');
assert.equal(formatMeasure({kind:'cost'},'0.0062'),'0.0062');
const filters={...h.analyticsDefaultFilters(),preset:'custom',from:'2026-01-01T00:00:00Z',to:'2026-01-03T00:00:00Z',tz:'UTC'};
const range=h.analyticsResolveRange(filters);
const model=h.buildAnalyticsModel([run],new Map([[run.id,[attempt]]]),new Map(),{filters,range,accountingData:data});
assert.equal(model.usageSummary.tokens.knownSubtotal,'9007199254740993');
assert.equal(model.executorStats.get('<executor>').durationSumMs,1000);
assert.equal(model.executorStats.get('<executor>').usageSamples,1);
assert.equal(model.measureRollups.get('tokens').subtotal,'9007199254740993');
assert.equal(model.measureRollups.get('tokens').buckets.find(b=>b.dateKey==='2026-01-02').exactTotal,'9007199254740993');
assert.equal(model.measureRollups.get('tokens').buckets.find(b=>b.dateKey==='2026-01-01').total,null);
assert.equal(model.measureRollups.get('tokens').byExecutor[0].exactValue,'9007199254740993');
assert.equal(model.measureRollups.get('credit:kiro:credit').subtotal,'0.18065595854063016');
assert.equal(model.measureRollups.get('cost:USD:api-equivalent').subtotal,'0.0062');
assert.equal(model.measureRollups.get('cost:USD:plan-debit').subtotal,'0.001');
assert.equal(model.measureRollups.get('tokens').precisionWarning,true);
assert.equal(h.analyticsMeasureExact(summary('0'),{kind:'tokens'}),'0');
assert.equal(h.analyticsMeasureExact({}, {kind:'tokens'}),null);
assert.equal(h.analyticsMeasureValueForAttempt({usage:{input:999,output:1}},{kind:'tokens'}),null,'legacy fields cannot reconstruct accounting');
assert.equal(h.analyticsAttemptBucketTime({startedAt:attempt.startedAt},run),null,'daily consumption requires completion date');
assert.equal(h.analyticsAttemptBucketTime(attempt,run).provenance,'finishedAt');

// Without server accounting the operational model remains usable, with no
// locally manufactured consumption totals.
const absent=h.buildAnalyticsModel([run],new Map([[run.id,[{...attempt,accountingSummary:undefined}]]]),new Map(),{filters,range});
assert.equal(absent.measures.length,0);
assert.equal(absent.executorStats.get('<executor>').attempts,1);
assert.equal(absent.executorStats.get('<executor>').missingUsage,1);
const empty=h.buildAnalyticsModel([],new Map(),new Map(),{filters,range});
assert.equal(empty.attemptCount,0);assert.equal(empty.measures.length,0);

const container={innerHTML:'',querySelectorAll:()=>[],querySelector:()=>null};
const ui=getHelpers(makeCtx({document:{getElementById:id=>id==='analyticsContent'?container:null}}));
ui.renderAnalytics(model);
assert.match(container.innerHTML,/9007199\.3B/);
assert.doesNotMatch(container.innerHTML,/>9007199254740992</,'breakdown labels retain exact values, not chart coordinates');
assert.match(container.innerHTML,/approximate numbers/);
assert.match(container.innerHTML,/&lt;executor&gt;/);
assert.doesNotMatch(container.innerHTML,/<executor>|<workflow>/);
ui.renderAnalytics(empty);assert.match(container.innerHTML,/No runs/);

// Each load fetches one requested page. Totals, records, and outcomes share
// that page's scope, even when the API also supplies global match counts.
let requests=0;
const loader=getHelpers(makeCtx({api:async path=>{
 assert.match(path,/^\/v1\/analytics\?/);requests++;
 const second=new URLSearchParams(path.split('?')[1]).get('offset')==='200';
 return {...data,accounting:summary(second?'999':'9007199254740993'),runs:[{...run,id:second?'run-2':'run-1',state:second?'failed':'succeeded'}],attemptsByRun:{[second?'run-2':'run-1']:[{...attempt,accountingSummary:undefined}]},totalRuns:201,hasMore:!second,offset:second?200:0,stateCounts:{succeeded:200,failed:1}};
}}));
const loaded=await loader.loadAnalyticsData(filters);
// The optional prior-window comparison is one additional bounded request.
assert.equal(requests,2);assert.equal(loaded.runs.length,1);assert.equal(loaded.usageSummary.tokens.knownSubtotal,'9007199254740993');
assert.equal(loaded.flatAttempts[0].attempt.accountingSummary.tokens.knownSubtotal,'9007199254740993');
assert.equal(loaded.stateCounts.succeeded,1);
assert.equal(loaded.hasMore,true);
const second=await loader.loadAnalyticsData({...filters,page:2});
assert.equal(requests,4);assert.equal(second.runs.length,1);assert.equal(second.offset,200);assert.equal(second.filters.page,2);
assert.equal(second.usageSummary.tokens.knownSubtotal,'999');assert.equal(second.stateCounts.failed,1);assert.equal(second.stateCounts.succeeded,0);
ui.renderAnalytics(second);assert.match(container.innerHTML,/Runs 201–201 of 201/);assert.match(container.innerHTML,/>Previous</);assert.doesNotMatch(container.innerHTML,/>Next</);
ui.renderAnalytics(loaded);assert.match(container.innerHTML,/>Next</);assert.doesNotMatch(container.innerHTML,/>Previous</);
const failed=getHelpers(makeCtx({api:async()=>{throw new Error('HTTP 404')}}));
await assert.rejects(()=>failed.loadAnalyticsData(filters),/HTTP 404/,'incompatible APIs fail without a legacy accounting path');
assert.equal(h.analyticsPercentile([1,2,3,4],0.5),2);
// An older response (including an error) must never overwrite a newer page.
for (const staleFailure of [false, true]) {
  const output={innerHTML:''}, pending=[], signals=[];
  const concurrent=getHelpers(makeCtx({
    document:{getElementById:()=>output},
    api:(_path,opts)=>{signals.push(opts.signal);return new Promise((resolve,reject)=>pending.push({resolve,reject}));},
  }));
  const older=concurrent.loadAnalytics(filters);
  const newer=concurrent.loadAnalytics(filters);
  assert.equal(signals[0].aborted,true);
  pending[1].resolve({runs:[],attemptsByRun:{},totalRuns:0});
  await newer;
  const latest=output.innerHTML;
  assert.match(latest,/No runs/);
  if(staleFailure) pending[0].reject(new Error('stale failure'));
  else pending[0].resolve({runs:[run],attemptsByRun:{[run.id]:[attempt]},totalRuns:1});
  await older;
  assert.equal(output.innerHTML,latest);
}
const failureOutput={innerHTML:''};
await getHelpers(makeCtx({document:{getElementById:()=>failureOutput},api:async()=>{throw new Error('<untrusted>');}})).loadAnalytics(filters);
assert.match(failureOutput.innerHTML,/role="alert"/);
assert.match(failureOutput.innerHTML,/&lt;untrusted&gt;/);
assert.doesNotMatch(failureOutput.innerHTML,/<untrusted>/);

const selected=h.analyticsFiltersFromURL('/analytics?range=7d&state=failed&workflow=demo&tz=UTC');
assert.equal(selected.preset,'7d');
assert.equal(selected.states.join(','),'failed');
assert.equal(selected.workflow,'demo');
const week=h.analyticsResolveRange(selected,new Date('2026-06-10T12:00:00Z'));
assert.equal(week.from.toISOString(),'2026-06-03T12:00:00.000Z');
assert.equal(h.analyticsDayBuckets(week,'UTC').length,8,'partial edge days retain their own buckets');
assert.equal(h.analyticsBucketForTimestamp(Date.parse('2026-06-09T23:59:00Z'),week,'UTC').dateKey,'2026-06-09');
const drillModel={...model,range:{...range,bucketMs:86400000}};
assert.equal(h.analyticsDrillRecords(drillModel,'tokenVolume:2026-01-02').length,1);
assert.equal(h.analyticsDrillRecords(drillModel,'tokenVolume:2026-01-01').length,0,'consumption drilldown uses completion, not start time');

const timed={startedAt:'2026-01-01T00:00:00Z',finishedAt:'2026-01-01T00:01:00Z',queuedAt:'2026-01-01T00:00:00Z',workerStartedAt:'2026-01-01T00:00:08Z',executionDurationMs:50};
const timing=h.analyticsAttemptTiming(timed);
assert.equal(timing.workerMs,50);assert.equal(timing.coordinatorMs,60000);assert.equal(timing.queueMs,8000);
assert.equal(h.analyticsAttemptTiming({executionDurationMs:0}).workerMs,0);
assert.equal(h.analyticsAttemptTiming({startedAt:timed.startedAt,finishedAt:timed.finishedAt}).queueMs,null,'coordinator start is not a queue timestamp');
assert.equal(h.analyticsAttemptTiming({startedAt:'invalid',finishedAt:timed.finishedAt}).coordinatorMs,null);
const retryAttempts=[
  {...timed,id:'failed',activationId:'node',state:'failed',retryNumber:0,executor:'generic'},
  {...timed,id:'recovered',activationId:'node',state:'succeeded',retryNumber:1,executionDurationMs:100,executor:'generic'},
  {id:'queued',activationId:'node',state:'queued',retryNumber:2,executor:'generic'},
];
const retry=h.buildAnalyticsRetryLatency(retryAttempts.map(attempt=>({attempt,run})),[run]);
assert.equal(retry.runOutcomes.succeededAfterRetry,1);
assert.equal(retry.runOutcomes.recoveredFailureAttempts,1);
assert.equal(retry.runOutcomes.finalFailureAttempts,0);
assert.equal(retry.retryOverhead.extraAttempts,1);
assert.equal(retry.retryOverhead.workerMs,50);
assert.equal(retry.retryOverhead.coordinatorMs,60000);
assert.equal(retry.byExecutor[0].worker.n,2,'queued attempts do not enter latency distributions');
assert.equal(retry.byExecutor[0].worker.median,50);
assert.equal(retry.byExecutor[0].worker.p95,100);
assert.equal(retry.failedRetriedUsage.unknown,2,'missing consumption is not zero');
console.log('Canonical analytics grouping, precision, paging, timing, and failure checks passed');
