/* ── analytics ─────────────────────────────────── */
// Cross-run operational insights: run volume, outcomes, and provider-reported
// token usage. Aggregated client-side from the existing runs + attempts APIs.

// Fetch all runs and their execution attempts, then compute aggregated stats.
// Returns a structured object; callers render it via renderAnalytics().
async function loadAnalyticsData() {
  const runsData = await api('/v1/runs');
  if (runsData.runs !== null && !Array.isArray(runsData.runs)) throw new Error('Invalid runs response');
  const runs = (runsData.runs || []).slice().sort((a, b) =>
    new Date(a.createdAt) - new Date(b.createdAt)
  );

  // Fetch all attempts so all-time totals describe the same run set. Keep the
  // fan-out bounded to avoid overwhelming the coordinator on large histories.
  const targets = runs;
  const attemptsByRun = new Map();
  const attemptErrors = new Map();
  const concurrency = 12;
  for (let start = 0; start < targets.length; start += concurrency) await Promise.all(targets.slice(start, start + concurrency).map(async run => {
    try {
      const d = await api(`/v1/runs/${encodeURIComponent(run.id)}/attempts`);
      if (d.attempts !== null && !Array.isArray(d.attempts)) throw new Error('Invalid attempts response');
      attemptsByRun.set(run.id, d.attempts || []);
    } catch (err) {
      attemptErrors.set(run.id, err.message || 'Request failed');
    }
  }));

  return buildAnalyticsModel(runs, attemptsByRun, attemptErrors);
}

// Build the analytics model from runs + attempt maps.
// Exported (module-style) only for testing; workspace.js uses loadAnalyticsData.
function buildAnalyticsModel(runs, attemptsByRun, attemptErrors = new Map()) {
  const states = ['created', 'running', 'waiting', 'paused', 'succeeded', 'failed', 'cancelled'];

  // ── run volume by state ────────────────────────
  const stateCounts = Object.fromEntries(states.map(s => [s, 0]));
  for (const r of runs) {
    if (stateCounts[r.state] !== undefined) stateCounts[r.state]++;
  }

  // ── daily run counts (last 30 days) ───────────
  // Bucket by calendar day using the run's createdAt.
  const now = new Date();
  const DAY_MS = 86400000;
  const BUCKET_COUNT = 30;
  const buckets = Array.from({ length: BUCKET_COUNT }, (_, i) => {
    const d = new Date(now - (BUCKET_COUNT - 1 - i) * DAY_MS);
    return {
      label: d.toLocaleDateString([], { month: 'short', day: 'numeric', timeZone: 'UTC' }),
      dateKey: d.toISOString().slice(0, 10),
      total: 0,
      succeeded: 0,
      failed: 0,
    };
  });
  const bucketByDate = new Map(buckets.map(b => [b.dateKey, b]));
  for (const r of runs) {
    if (!r.createdAt) continue;
    const timestamp = Date.parse(r.createdAt);
    if (!Number.isFinite(timestamp)) continue;
    const key = new Date(timestamp).toISOString().slice(0, 10);
    const b = bucketByDate.get(key);
    if (!b) continue;
    b.total++;
    if (r.state === 'succeeded') b.succeeded++;
    if (r.state === 'failed') b.failed++;
  }

  // ── per-executor attempt counts, timing, and token usage ─────────────
  // Group only by the shared ExecutionAttempt executor identifier.
  // executorStats tracks attempt counts and worker execution timing.
  // executorUsage tracks provider-reported token totals (may be absent).
  // These are kept separate: missing token records must not hide timing/attempt data.
  //
  // Timing note: executionDurationMs is the worker-measured execution duration
  // (excludes coordinator dispatch overhead and queue time). startedAt/finishedAt
  // are coordinator-recorded wall times (queue-inclusive). We prefer
  // executionDurationMs where available and only fall back to the coordinator
  // elapsed difference when no worker duration was recorded — clearly labeled.
  const executorStats = new Map(); // executor → {attempts, succeeded, failed, durationSumMs, durationSamples, elapsedSumMs, elapsedSamples, runIds}
  const executorUsage = new Map(); // executor → {input, output, cached, runIds: Set}
  let runsWithNoAttempts = 0;
  let runsWithUnavailableUsage = 0;

  for (const r of runs) {
    if (attemptErrors.has(r.id)) continue;
    const attempts = attemptsByRun.get(r.id) || [];
    if (!attempts.length) {
      runsWithNoAttempts++;
      continue;
    }
    let incompleteUsage = false;
    let hasCompletedAttempt = false;
    for (const a of attempts) {
      const execKey = a.executor || 'unknown';

      // ── attempt count and timing ───────────────
      let stats = executorStats.get(execKey);
      if (!stats) {
        stats = { attempts: 0, succeeded: 0, failed: 0, durationSumMs: 0, durationSamples: 0, elapsedSumMs: 0, elapsedSamples: 0, running: 0, missingTiming: 0, usageSamples: 0, missingUsage: 0, runIds: new Set() };
        executorStats.set(execKey, stats);
      }
      stats.attempts++;
      stats.runIds.add(r.id);
      if (a.state === 'succeeded') stats.succeeded++;
      if (a.state === 'failed')    stats.failed++;

      if (a.state === 'running') {
        stats.running++;
        continue;
      }
      if(a.state==='queued')continue;
      hasCompletedAttempt = true;
      let taskDuration=null;
      // Keep worker measurements and coordinator elapsed samples separate.
      if (analyticsNumber(a.executionDurationMs) != null) {
        stats.durationSumMs += a.executionDurationMs;
        stats.durationSamples++;
        taskDuration=a.executionDurationMs;
      } else {
        const start = Date.parse(a.startedAt), finish = Date.parse(a.finishedAt);
        if (Number.isFinite(start) && Number.isFinite(finish) && start > 0 && finish >= start) {
          stats.elapsedSumMs += finish - start;
          stats.elapsedSamples++;
          taskDuration=finish-start;
        } else {
          stats.missingTiming++;
        }
      }
      if(taskDuration!=null){
        stats.taskTimeTotal=(stats.taskTimeTotal||0)+taskDuration;
        stats.taskTimeCount=(stats.taskTimeCount||0)+1;
        stats.taskTimeMin=Math.min(stats.taskTimeMin??Infinity,taskDuration);
        stats.taskTimeMax=Math.max(stats.taskTimeMax??0,taskDuration);
      }

      // ── token usage ───────────────────────────
      const usage = analyticsUsage(a.usage);
      if (!usage) { stats.missingUsage++; incompleteUsage = true; continue; }
      stats.usageSamples++;
      if (Object.values(usage).some(value => value == null)) { stats.missingUsage++; incompleteUsage = true; }
      let entry = executorUsage.get(execKey);
      if (!entry) {
        entry = { input: null, output: null, cached: null, runIds: new Set() };
        executorUsage.set(execKey, entry);
      }
      for (const field of ['input', 'output', 'cached']) {
        if (usage[field] != null) entry[field] = (entry[field] ?? 0) + usage[field];
      }
      entry.runIds.add(r.id);
    }
    if (hasCompletedAttempt && incompleteUsage) {
      runsWithUnavailableUsage++;
    }
  }

  // ── token usage over time (last 30 days) ──────
  // Aggregate reported tokens per day bucket.
  const tokenBuckets = Array.from({ length: BUCKET_COUNT }, (_, i) => {
    const d = new Date(now - (BUCKET_COUNT - 1 - i) * DAY_MS);
    return {
      label: d.toLocaleDateString([], { month: 'short', day: 'numeric', timeZone: 'UTC' }),
      dateKey: d.toISOString().slice(0, 10),
      input: null,
      output: null,
      cached: null,
      hasData: false,
    };
  });
  const tokenBucketByDate = new Map(tokenBuckets.map(b => [b.dateKey, b]));
  for (const r of runs) {
    if (!r.createdAt) continue;
    const timestamp = Date.parse(r.createdAt);
    if (!Number.isFinite(timestamp)) continue;
    const key = new Date(timestamp).toISOString().slice(0, 10);
    const b = tokenBucketByDate.get(key);
    if (!b) continue;
    if (attemptErrors.has(r.id)) continue;
    const attempts = attemptsByRun.get(r.id) || [];
    for (const a of attempts) {
      const usage = analyticsUsage(a.usage);
      if (a.state === 'running' || !usage) continue;
      for (const field of ['input', 'output', 'cached']) {
        if (usage[field] != null) b[field] = (b[field] ?? 0) + usage[field];
      }
      b.hasData = true;
    }
  }

  // ── totals ─────────────────────────────────────
  let totalInput = null, totalOutput = null, totalCached = null, anyTokenData = false;
  for (const [, e] of executorUsage) {
    if (e.input != null) totalInput = (totalInput ?? 0) + e.input;
    if (e.output != null) totalOutput = (totalOutput ?? 0) + e.output;
    if (e.cached != null) totalCached = (totalCached ?? 0) + e.cached;
    anyTokenData = true;
  }

  return {
    runs,
    attemptsByRun,
    attemptErrors,
    stateCounts,
    buckets,
    tokenBuckets,
    executorStats,
    executorUsage,
    runsWithNoAttempts,
    runsWithUnavailableUsage,
    totals: anyTokenData ? { input: totalInput, output: totalOutput, cached: totalCached } : null,
  };
}

// Reject missing or invalid measurements without turning them into zero.
function analyticsNumber(value) {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null;
}
function analyticsUsage(usage) {
  if (usage == null) return null;
  const values = Object.fromEntries(['input', 'output', 'cached'].map(field => [field, analyticsNumber(usage[field])]));
  return Object.values(values).some(value => value != null) ? values : null;
}

// Render a simple CSS bar chart from an array of {label, value} items.
// maxValue is the scale reference; bars are percentage widths.
function barChartMarkup(items, opts = {}) {
  if (!items.length) return '<p style="color:var(--muted);font-size:12px">No data.</p>';
  const maxVal = opts.maxValue ?? Math.max(...items.map(it => it.value), 1);
  const colorVar = opts.colorVar || '--teal';
  return `<div class="analytics-bar-chart" role="list">${items.map(it => {
    const pct = maxVal > 0 ? Math.max(0, Math.min(100, (it.value / maxVal) * 100)) : 0;
    const label = it.label;
    const val = opts.fmt ? opts.fmt(it.value) : String(it.value);
    return `<div class="abc-row" role="listitem">
      <div class="abc-label" title="${esc(label)}">${esc(label)}</div>
      <div class="abc-track">
        <div class="abc-bar" style="width:${pct.toFixed(1)}%;background:var(${esc(colorVar)})"></div>
      </div>
      <div class="abc-val">${esc(val)}</div>
    </div>`;
  }).join('')}</div>`;
}

// Render a stacked sparkline from daily bucket data.
// Each bucket has total/succeeded/failed.
function sparklineMarkup(buckets) {
  const maxTotal = Math.max(...buckets.map(b => b.total), 1);
  const hasAny = buckets.some(b => b.total > 0);
  if (!hasAny) return '<p style="color:var(--muted);font-size:12px">No runs in the last 30 days.</p>';

  const bars = buckets.map(b => {
    const totalH = maxTotal > 0 ? (b.total / maxTotal) * 56 : 0;
    const succH = maxTotal > 0 ? (b.succeeded / maxTotal) * 56 : 0;
    const failH = maxTotal > 0 ? (b.failed / maxTotal) * 56 : 0;
    const pendH = Math.max(0, totalH - succH - failH);
    const tip = `${b.label}: ${b.total} runs (${b.succeeded} succeeded, ${b.failed} failed)`;
    return `<div class="spark-col" title="${esc(tip)}" aria-label="${esc(tip)}">
      <div class="spark-bar-wrap" style="height:${Math.max(1,Math.round(totalH))}px">
        ${failH > 0 ? `<div class="spark-seg failed" style="height:${Math.round(failH)}px"></div>` : ''}
        ${succH > 0 ? `<div class="spark-seg succeeded" style="height:${Math.round(succH)}px"></div>` : ''}
        ${pendH > 0 ? `<div class="spark-seg pending" style="height:${Math.round(pendH)}px"></div>` : ''}
      </div>
    </div>`;
  }).join('');

  const labels = buckets
    .filter((_, i) => i === 0 || i === Math.floor(buckets.length / 2) || i === buckets.length - 1)
    .map(b => `<span>${esc(b.label)}</span>`).join('');

  return `<div class="sparkline" role="img" aria-label="Run volume over last 30 days">
    <div class="spark-bars">${bars}</div>
    <div class="spark-axis">${labels}</div>
  </div>`;
}

// Render the token-usage-over-time section as stacked bars per day.
function tokenSparklineMarkup(tokenBuckets) {
  const maxTotal = Math.max(...tokenBuckets.map(b => b.input + b.output + b.cached), 1);
  const hasAny = tokenBuckets.some(b => b.hasData);
  if (!hasAny) return '';

  const bars = tokenBuckets.map(b => {
    const total = b.input + b.output + b.cached;
    const totalH = maxTotal > 0 ? (total / maxTotal) * 56 : 0;
    const inH  = total > 0 ? (b.input  / total) * totalH : 0;
    const outH = total > 0 ? (b.output / total) * totalH : 0;
    const cacH = total > 0 ? (b.cached / total) * totalH : 0;
    const tip = `${b.label}: in=${fmtTok(b.input)} out=${fmtTok(b.output)} cached=${fmtTok(b.cached)}`;
    return `<div class="spark-col" title="${esc(tip)}" aria-label="${esc(tip)}">
      <div class="spark-bar-wrap" style="height:${Math.max(1,Math.round(totalH))}px">
        ${cacH > 0 ? `<div class="spark-seg tok-cached" style="height:${Math.round(cacH)}px"></div>` : ''}
        ${outH > 0 ? `<div class="spark-seg tok-out" style="height:${Math.round(outH)}px"></div>` : ''}
        ${inH  > 0 ? `<div class="spark-seg tok-in"  style="height:${Math.round(inH)}px"></div>` : ''}
      </div>
    </div>`;
  }).join('');

  const labels = tokenBuckets
    .filter((_, i) => i === 0 || i === Math.floor(tokenBuckets.length / 2) || i === tokenBuckets.length - 1)
    .map(b => `<span>${esc(b.label)}</span>`).join('');

  return `<div class="sparkline" role="img" aria-label="Token usage over last 30 days">
    <div class="spark-bars">${bars}</div>
    <div class="spark-axis">${labels}</div>
  </div>`;
}

// Keep provenance and unavailable measurements inspectable alongside the charts.
function analyticsDetailsMarkup(model) {
  const runLinks=ids=>[...ids].map(id=>`<a href="#run/${encodeURIComponent(id)}">${esc(id)}</a>`).join(', ');
  const notes=[];
  if(model.runsWithNoAttempts)notes.push(`${model.runsWithNoAttempts} runs have no attempt records.`);
  if(model.runsWithUnavailableUsage)notes.push(`${model.runsWithUnavailableUsage} runs have incomplete token data: no provider-reported token usage for some measurements.`);
  for(const [id,error] of model.attemptErrors||[])notes.push(`Run ${runLinks([id])}: ${esc(error)}; statistics are partial.`);
  const sample=(total,count)=>count?`${esc(fmtDuration(total/count))} (n=${count})`:'Unavailable';
  const rows=[...model.executorStats].map(([name,stats])=>{
    const usage=model.executorUsage.get(name);
    const values=['input','output','cached'].map(field=>`<td class="num">${esc(fmtTok(usage?.[field]))}</td>`).join('');
    const complete=usage&&['input','output','cached'].every(field=>usage[field]!=null);
    const total=complete?usage.input+usage.output+usage.cached:null;
    return {name,stats,values,total};
  });
  const maxTokens=Math.max(1,...rows.map(row=>row.total??0));
  return `<details class="analytics-details"><summary>Data details</summary>
    ${notes.length?`<div role="status">${notes.map(note=>`<p>${note}</p>`).join('')}</div>`:''}
    <div class="tablebox"><table><thead><tr><th>Agent</th><th>Worker time</th><th>Coordinator elapsed</th><th>Timing availability</th><th>Input tokens</th><th>Output tokens</th><th>Cached tokens</th><th>Relative token volume</th><th>Runs</th></tr></thead><tbody>
    ${rows.map(({name,stats,values,total})=>`<tr><td><strong>${esc(name)}</strong></td><td>${sample(stats.durationSumMs,stats.durationSamples)}</td><td>${sample(stats.elapsedSumMs,stats.elapsedSamples)}</td><td>${stats.missingTiming} missing timing; ${stats.running} in progress</td>${values}<td>${total==null?'Unavailable':`<span class="abc-bar" style="display:block;width:${(100*total/maxTokens).toFixed(1)}%">${esc(fmtTok(total))}</span>`}</td><td>${runLinks(stats.runIds)}</td></tr>`).join('')}
    </tbody></table></div></details>`;
}

// Render the full analytics page from the model.
function renderAnalytics(model) {
  const {runs,buckets,tokenBuckets,executorStats,executorUsage}=model;
  const container=document.getElementById('analyticsContent');
  if(!container)return;
  const workflows=new Map();
  for(const run of runs){
    if(!['succeeded','failed','cancelled'].includes(run.state))continue;
    const ms=Date.parse(run.updatedAt)-Date.parse(run.createdAt);
    if(!Number.isFinite(ms)||ms<0)continue;
    const name=run.workflowName||'Unnamed workflow';
    if(!workflows.has(name))workflows.set(name,[]);
    workflows.get(name).push(ms);
  }
  const workflowTime=[...workflows].map(([label,values])=>{
    values.sort((a,b)=>a-b);
    const mid=Math.floor(values.length/2);
    return {label,value:values.length%2?values[mid]:(values[mid-1]+values[mid])/2};
  }).sort((a,b)=>b.value-a.value).slice(0,10);
  const agentTiming=[...(executorStats||[])].map(([label,s])=>({label,
    low:s.taskTimeMin??null,high:s.taskTimeMax??null,
    average:s.taskTimeCount?s.taskTimeTotal/s.taskTimeCount:null,
    count:s.taskTimeCount||0,fallback:s.elapsedSamples||0,
  })).sort((a,b)=>(b.average??-1)-(a.average??-1));
  const failureRates=[...(executorStats||[])].map(([label,s])=>({label,value:s.failed+s.succeeded?100*s.failed/(s.failed+s.succeeded):null}))
    .filter(x=>x.value!=null).sort((a,b)=>b.value-a.value).slice(0,10);
  const empty='<div class="analytics-chart-empty" aria-label="No reported data">—</div>';
  const bars=(items,opts)=>items.length?barChartMarkup(items,opts):empty;
  const card=(title,chart,wide=false)=>`<section class="analytics-chart-card${wide?' analytics-chart-wide':''}"><h2>${title}</h2>${chart}</section>`;
  const legend=(items)=>`<div class="spark-legend">${items.map(([label,cls])=>`<span class="spark-legend-item"><span class="spark-swatch ${cls}"></span>${label}</span>`).join('')}</div>`;
  const rawMax=Math.max(1,...agentTiming.map(a=>a.high??0));
  const magnitude=10**Math.floor(Math.log10(rawMax));
  const maxTiming=([1,2,5,10].find(n=>n*magnitude>=rawMax)||10)*magnitude;
  const timingChart=agentTiming.length?`<div class="adapter-timing-chart">${agentTiming.map(agent=>{
    const tooltip=`${agent.count} completed attempts; ${agent.fallback} use timestamp elapsed time, including queue and dispatch delays`;
    return `<div class="adapter-timing-group"><div class="adapter-timing-identity"><strong>${esc(agent.label)}</strong><span title="Completed attempts">n=${agent.count}</span></div><div class="adapter-timing-bars" title="${esc(tooltip)}">${[['low','Lowest'],['average','Average'],['high','Highest']].map(([field,label])=>{
      const value=agent[field];
      return `<div class="adapter-timing-series timing-${field}"><span class="adapter-series-label">${label}</span><div class="adapter-series-track"><span class="adapter-series-bar${value===0?' timing-zero':''}" style="width:${value==null?0:100*value/maxTiming}%"></span></div><span class="adapter-series-value">${value==null?'—':esc(fmtDuration(value))}</span></div>`;
    }).join('')}</div></div>`;
  }).join('')}<div class="adapter-timing-axis"><span></span><div><span>0</span><span>${esc(fmtDuration(maxTiming/2))}</span><span>${esc(fmtDuration(maxTiming))}</span></div></div></div>`:empty;
  const warning=model.attemptErrors?.size?`<div class="analytics-data-status" role="status" title="Some attempt records could not be loaded">Partial data · ${model.attemptErrors.size} runs</div>`:'';
  container.innerHTML=warning+`<div class="analytics-chart-grid">`+
    card('Task time by adapter',timingChart,true)+
    card('Run outcomes · 30 days',sparklineMarkup(buckets)+legend([['Succeeded','succeeded'],['Failed','failed'],['Other','pending']]),true)+
    card('Median workflow duration',bars(workflowTime,{fmt:fmtDuration}))+
    card('Agent failure rate',bars(failureRates,{fmt:value=>`${Math.round(value)}%`,maxValue:100,colorVar:'--red'}))+
    card('Daily tokens · 30 days',tokenSparklineMarkup(tokenBuckets)+legend([['Input','tok-input'],['Output','tok-output'],['Cached','tok-cached']]),true)+
    '</div>'+analyticsDetailsMarkup(model);
}

let analyticsRequest = 0;
async function loadAnalytics() {
  const request = ++analyticsRequest;
  const container = document.getElementById('analyticsContent');
  if (container) container.innerHTML = '<div class="loading">Loading analytics…</div>';
  try {
    const model = await loadAnalyticsData();
    if (request !== analyticsRequest) return;
    renderAnalytics(model);
  } catch (err) {
    if (request !== analyticsRequest) return;
    if (container) container.innerHTML = `<div class="notice error" role="alert">Unable to load analytics: ${esc(err.message)}</div>`;
  }
}
