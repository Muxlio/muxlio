/* ── analytics ─────────────────────────────────── */
// Cross-run operational insights: run volume, outcomes, timing, and
// provider-reported usage (tokens, credits, costs). Aggregated client-side
// from runs + attempts using the shared UsageSummary semantics.

async function loadAnalyticsDataLegacy(filters, range, attemptErrors = new Map(), signal) {
  const runsData = await api('/v1/runs', { signal });
  if (runsData.runs !== null && !Array.isArray(runsData.runs)) throw new Error('Invalid runs response');
  const attemptsByRun = new Map();
  let runs = (runsData.runs || []).slice().sort((a, b) => new Date(a.createdAt) - new Date(b.createdAt));
  const includeRange = analyticsRangeThroughNow(range);
  runs = runs.filter(r => analyticsRunInRange(r, includeRange));
  const concurrency = 12;
  for (let start = 0; start < runs.length; start += concurrency) await Promise.all(runs.slice(start, start + concurrency).map(async run => {
    try {
      const d = await api(`/v1/runs/${encodeURIComponent(run.id)}/attempts?details=false`, { signal });
      if (d.attempts !== null && !Array.isArray(d.attempts)) throw new Error('Invalid attempts response');
      attemptsByRun.set(run.id, d.attempts || []);
    } catch (err) {
      attemptErrors.set(run.id, err.message || 'Request failed');
    }
  }));
  const applied = analyticsApplyFiltersToModelInputs(runs, attemptsByRun, filters, includeRange);
  return buildAnalyticsModel(applied.runs, applied.attemptsByRun, attemptErrors, {
    range, filters, totalRuns: applied.runs.length, hasMore: false, legacy: true,
  });
}

async function loadAnalyticsApiPages(filters, range, signal) {
  const runs = [];
  const attemptsByRun = new Map();
  let page = 1;
  let totalRuns = 0;
  let populations;
  let stateCounts;
  let pagesFetched = 0;
  let offset = 0;
  let hasMore = false;

  for (;;) {
    const data = await api(`/v1/analytics?${analyticsFiltersQueryString({ ...filters, page }, range)}`, { signal });
    if (!Array.isArray(data.runs)) throw new Error('Invalid analytics runs response');
    pagesFetched++;
    for (const run of data.runs) runs.push(run);
    for (const [id, list] of Object.entries(data.attemptsByRun || {})) {
      attemptsByRun.set(id, Array.isArray(list) ? list : []);
    }
    totalRuns = data.totalRuns ?? runs.length;
    populations = data.populations ?? populations;
    stateCounts = data.stateCounts ?? stateCounts;
    offset = data.offset ?? offset;
    hasMore = !!data.hasMore && runs.length < totalRuns;
    // Avoid looping forever if a changing/inconsistent backend reports more
    // rows but returns an empty page. Preserve that partial scope in the UI.
    if (!hasMore || data.runs.length === 0) break;
    page++;
  }
  runs.sort((a, b) => new Date(a.createdAt) - new Date(b.createdAt));
  return {
    runs,
    attemptsByRun,
    totalRuns,
    populations,
    stateCounts,
    pagesFetched,
    offset,
    hasMore,
  };
}

async function loadAnalyticsData(filters = analyticsFiltersFromURL(), signal) {
  const range = analyticsResolveRange(filters);
  const attemptErrors = new Map();
  let snapshot;
  try {
    snapshot = await loadAnalyticsApiPages(filters, range, signal);
  } catch (err) {
    if (/HTTP 404|unavailable/i.test(err?.message || '')) {
      return loadAnalyticsDataLegacy(filters, range, attemptErrors, signal);
    }
    throw err;
  }
  const { runs, attemptsByRun, totalRuns, populations, stateCounts, pagesFetched, offset, hasMore } = snapshot;
  const model = buildAnalyticsModel(runs, attemptsByRun, attemptErrors, {
    range,
    filters: { ...filters, page: 1 },
    totalRuns,
    hasMore,
    populations,
    stateCounts,
    offset,
    pagesFetched,
  });
  if (totalRuns >= 3) {
    const prior = analyticsPriorRange(range);
    const priorFilters = { ...filters, page: 1 };
    const priorQs = analyticsFiltersQueryString(priorFilters, prior);
    try {
      const priorData = await api(`/v1/analytics?${priorQs}`, { signal });
      const priorTotal = priorData.totalRuns ?? (priorData.runs || []).length;
      if (priorTotal > 0) {
        model.comparison = {
          priorRunCount: priorTotal,
          currentRunCount: totalRuns,
          priorLabel: prior.label,
        };
      }
    } catch {
      // comparison is optional
    }
  }
  return model;
}

function buildAnalyticsModel(runs, attemptsByRun, attemptErrors = new Map(), ctx = {}) {
  const filters = ctx.filters || analyticsDefaultFilters();
  const range = ctx.range || analyticsResolveRange(filters);
  const tz = filters.tz || 'UTC';
  const stateCounts = analyticsStateCountsFromContext(ctx, runs);

  const dayBucketTemplate = () => analyticsDayBuckets(range, tz);

  const buckets = dayBucketTemplate().map(b => ({ ...b, total: 0, succeeded: 0, failed: 0 }));
  const bucketByKey = new Map(buckets.map(b => [b.dateKey, b]));
  for (const r of runs) {
    if (!r.createdAt) continue;
    const timestamp = Date.parse(r.createdAt);
    if (!Number.isFinite(timestamp)) continue;
    const b = analyticsBucketForTimestamp(timestamp, range, tz) || bucketByKey.get(analyticsBucketKey(new Date(timestamp), range, tz));
    if (!b) continue;
    const row = bucketByKey.get(b.dateKey) || b;
    row.total++;
    if (r.state === 'succeeded') row.succeeded++;
    if (r.state === 'failed') row.failed++;
  }

  const executorStats = new Map();
  const executorUsage = new Map();
  const flatAttempts = [];
  let runsWithNoAttempts = 0;
  let runsWithUnavailableUsage = 0;
  const bucketProvenance = { finishedAt: 0, startedAt: 0, runCreatedAt: 0 };

  const tokenBuckets = dayBucketTemplate().map(b => ({
    ...b,
    input: null,
    output: null,
    cached: null,
    hasData: false,
  }));
  const tokenBucketByDate = new Map(tokenBuckets.map(b => [b.dateKey, b]));

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
      flatAttempts.push({ attempt: a, run: r });
      const execKey = a.executor || 'unknown';

      let stats = executorStats.get(execKey);
      if (!stats) {
        stats = { attempts: 0, succeeded: 0, failed: 0, durationSumMs: 0, durationSamples: 0, elapsedSumMs: 0, elapsedSamples: 0, running: 0, missingTiming: 0, usageSamples: 0, missingUsage: 0, runIds: new Set() };
        executorStats.set(execKey, stats);
      }
      stats.attempts++;
      stats.runIds.add(r.id);
      if (a.state === 'succeeded') stats.succeeded++;
      if (a.state === 'failed') stats.failed++;

      const usage = analyticsUsage(a.usage);
      const bucketTime = analyticsAttemptBucketTime(a, r);
      if (bucketTime) bucketProvenance[bucketTime.provenance] = (bucketProvenance[bucketTime.provenance] || 0) + 1;

      if (!analyticsAttemptPending(a.state)) {
        if (usage) {
          if (bucketTime) {
            const b = analyticsBucketForTimestamp(bucketTime.ms, range, tz);
            const key = b?.dateKey || analyticsBucketKey(new Date(bucketTime.ms), range, tz);
            const tb = tokenBucketByDate.get(key);
            if (tb) {
              for (const field of ['input', 'output', 'cached']) {
                if (usage[field] != null) tb[field] = (tb[field] ?? 0) + usage[field];
              }
              tb.hasData = true;
            }
          }
        }
      }

      if (a.state === 'running') {
        stats.running++;
        continue;
      }
      if (a.state === 'queued') continue;
      hasCompletedAttempt = true;

      let taskDuration = null;
      if (analyticsNumber(a.executionDurationMs) != null) {
        stats.durationSumMs += a.executionDurationMs;
        stats.durationSamples++;
        taskDuration = a.executionDurationMs;
      } else {
        const start = Date.parse(a.startedAt), finish = Date.parse(a.finishedAt);
        if (Number.isFinite(start) && Number.isFinite(finish) && start > 0 && finish >= start) {
          stats.elapsedSumMs += finish - start;
          stats.elapsedSamples++;
          taskDuration = finish - start;
        } else {
          stats.missingTiming++;
        }
      }
      if (taskDuration != null) {
        stats.taskTimeTotal = (stats.taskTimeTotal || 0) + taskDuration;
        stats.taskTimeCount = (stats.taskTimeCount || 0) + 1;
        stats.taskTimeMin = Math.min(stats.taskTimeMin ?? Infinity, taskDuration);
        stats.taskTimeMax = Math.max(stats.taskTimeMax ?? 0, taskDuration);
      }

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
    if (hasCompletedAttempt && incompleteUsage) runsWithUnavailableUsage++;
  }

  const attemptRows = flatAttempts.map(({ attempt }) => attempt);
  const usageSummary = analyticsSummarizeUsage(attemptRows);
  const measures = analyticsMeasureCatalog(usageSummary);
  const measureRollups = buildAnalyticsMeasureRollups(flatAttempts, measures, dayBucketTemplate(), range, tz);
  const retryLatency = buildAnalyticsRetryLatency(flatAttempts, runs);

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
    flatAttempts,
    attemptCount: attemptRows.length,
    usageSummary,
    measures,
    defaultMeasure: measures[0]?.id || null,
    measureRollups,
    bucketProvenance,
    filters,
    range,
    totalRuns: ctx.totalRuns ?? runs.length,
    hasMore: !!ctx.hasMore,
    populations: ctx.populations,
    comparison: ctx.comparison,
    legacy: !!ctx.legacy,
    retryLatency,
    pagesFetched: ctx.pagesFetched ?? 1,
  };
}

function buildAnalyticsMeasureRollups(flatAttempts, measures, dayTemplate, range, tz = 'UTC') {
  const rollups = new Map();
  for (const measure of measures) {
    const buckets = dayTemplate.map(b => ({ ...b, total: null, reporting: 0, hasData: false }));
    const bucketByDate = new Map(buckets.map(b => [b.dateKey, b]));
    const byExecutor = new Map();
    const byWorkflow = new Map();
    const evidence = [];
    let subtotal = null;
    let reporting = 0;

    for (const { attempt, run } of flatAttempts) {
      if (analyticsAttemptPending(attempt.state)) continue;
      const value = analyticsMeasureValueForAttempt(attempt, measure);
      const row = {
        runId: run.id,
        attemptId: attempt.id || '',
        executor: attempt.executor || 'unknown',
        workflow: run.workflowName || 'Unnamed workflow',
        value,
        bucketProvenance: null,
      };
      if (value != null) {
        reporting++;
        subtotal = (subtotal ?? 0) + value;
        const execKey = row.executor;
        const wfKey = row.workflow;
        if (!byExecutor.has(execKey)) byExecutor.set(execKey, { label: execKey, value: 0, runIds: new Set() });
        if (!byWorkflow.has(wfKey)) byWorkflow.set(wfKey, { label: wfKey, value: 0, runIds: new Set() });
        const ex = byExecutor.get(execKey);
        ex.value += value;
        ex.runIds.add(run.id);
        const wf = byWorkflow.get(wfKey);
        wf.value += value;
        wf.runIds.add(run.id);
        const bucketTime = analyticsAttemptBucketTime(attempt, run);
        if (bucketTime) {
          row.bucketProvenance = bucketTime.provenance;
          const bk = analyticsBucketForTimestamp(bucketTime.ms, range, tz);
          const key = bk?.dateKey || analyticsBucketKey(new Date(bucketTime.ms), range, tz);
          const b = bucketByDate.get(key);
          if (b) {
            b.total = (b.total ?? 0) + value;
            b.reporting++;
            b.hasData = true;
          }
        }
      }
      evidence.push(row);
    }

    rollups.set(measure.id, {
      measure,
      buckets,
      byExecutor: [...byExecutor.values()].sort((a, b) => b.value - a.value),
      byWorkflow: [...byWorkflow.values()].sort((a, b) => b.value - a.value),
      evidence,
      subtotal,
      reporting,
    });
  }
  return rollups;
}

function analyticsNumber(value) {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null;
}

function analyticsUsage(usage) {
  if (usage == null) return null;
  const values = Object.fromEntries(['input', 'output', 'cached'].map(field => {
    const reported = analyticsUsageFieldReported(usage, field);
    return [field, reported === undefined ? null : reported];
  }));
  return Object.values(values).some(value => value != null) ? values : null;
}

function analyticsTokenVolume(input, output, cached) {
  const inN = analyticsNumber(input);
  const outN = analyticsNumber(output);
  const cachedN = analyticsNumber(cached);
  if (inN == null || outN == null || (cachedN != null && cachedN > inN)) return null;
  return {
    volume: inN + outN,
    nonCachedInput: cachedN == null ? null : inN - cachedN,
    cached: cachedN,
    output: outN,
  };
}

function barChartMarkup(items, opts = {}) {
  if (!items.length) return '<p style="color:var(--muted);font-size:12px">No data.</p>';
  const maxVal = opts.maxValue ?? Math.max(...items.map(it => it.value), 1);
  const colorVar = opts.colorVar || '--teal';
  const drillKind = opts.drillKind || '';
  return `<div class="analytics-bar-chart" role="list">${items.map(it => {
    const pct = maxVal > 0 ? Math.max(0, Math.min(100, (it.value / maxVal) * 100)) : 0;
    const label = it.label;
    const val = opts.fmt ? opts.fmt(it.value) : String(it.value);
    const drillKey = it.drillKey || it.label;
    const drill = drillKind ? ` data-analytics-drill="${esc(`${drillKind}:${drillKey}`)}"` : '';
    const tag = drillKind ? 'button' : 'div';
    const btnType = drillKind ? ' type="button"' : '';
    return `<${tag} class="abc-row${drillKind ? ' abc-drill' : ''}"${btnType}${drill} role="listitem">
      <div class="abc-label" title="${esc(label)}">${esc(label)}</div>
      <div class="abc-track">
        <div class="abc-bar" style="width:${pct.toFixed(1)}%;background:var(${esc(colorVar)})"></div>
      </div>
      <div class="abc-val">${esc(val)}</div>
    </${tag}>`;
  }).join('')}</div>`;
}

function sparklineMarkup(buckets, opts = {}) {
  const maxTotal = Math.max(...buckets.map(b => b.total), 1);
  const hasAny = buckets.some(b => b.total > 0);
  const emptyMsg = opts.emptyLabel || 'No runs in the selected window.';
  if (!hasAny) return `<p style="color:var(--muted);font-size:12px">${esc(emptyMsg)}</p>`;

  const drillKind = opts.drillKind || '';
  const bars = buckets.map(b => {
    const totalH = maxTotal > 0 ? (b.total / maxTotal) * 56 : 0;
    const succH = maxTotal > 0 ? (b.succeeded / maxTotal) * 56 : 0;
    const failH = maxTotal > 0 ? (b.failed / maxTotal) * 56 : 0;
    const pendH = Math.max(0, totalH - succH - failH);
    const tip = `${b.label}: ${b.total} runs (${b.succeeded} succeeded, ${b.failed} failed)`;
    const drill = drillKind ? ` data-analytics-drill="${esc(`${drillKind}:${b.dateKey}`)}"` : '';
  const tag = drillKind ? 'button' : 'div';
  const btnType = drillKind ? ' type="button"' : '';
    return `<${tag} class="spark-col${drillKind ? ' spark-drill' : ''}"${btnType}${drill} title="${esc(tip)}" aria-label="${esc(tip)}">
      <div class="spark-bar-wrap" style="height:${Math.max(1, Math.round(totalH))}px">
        ${failH > 0 ? `<div class="spark-seg failed" style="height:${Math.round(failH)}px"></div>` : ''}
        ${succH > 0 ? `<div class="spark-seg succeeded" style="height:${Math.round(succH)}px"></div>` : ''}
        ${pendH > 0 ? `<div class="spark-seg pending" style="height:${Math.round(pendH)}px"></div>` : ''}
      </div>
    </${tag}>`;
  }).join('');

  const labels = buckets
    .filter((_, i) => i === 0 || i === Math.floor(buckets.length / 2) || i === buckets.length - 1)
    .map(b => `<span>${esc(b.label)}</span>`).join('');

  return `<div class="sparkline" role="img" aria-label="${esc(opts.ariaLabel || 'Run volume')}">
    <div class="spark-bars">${bars}</div>
    <div class="spark-axis">${labels}</div>
  </div>`;
}

function tokenSparklineMarkup(tokenBuckets, opts = {}) {
  const volumes = tokenBuckets.map(b => analyticsTokenVolume(b.input, b.output, b.cached)?.volume ?? 0);
  const maxTotal = Math.max(...volumes, 1);
  const hasAny = tokenBuckets.some(b => b.hasData);
  if (!hasAny) return '';

  const bars = tokenBuckets.map(b => {
    const parts = analyticsTokenVolume(b.input, b.output, b.cached);
    const total = parts?.volume ?? 0;
    const totalH = maxTotal > 0 ? (total / maxTotal) * 56 : 0;
    const inH = total > 0 && parts ? (parts.nonCachedInput / total) * totalH : 0;
    const outH = total > 0 && parts ? (parts.output / total) * totalH : 0;
    const cacH = total > 0 && parts ? (parts.cached / total) * totalH : 0;
    const tip = `${b.label}: in=${fmtTok(b.input)} out=${fmtTok(b.output)} cached=${fmtTok(b.cached)}`;
    const drill = opts.drillKind ? ` data-analytics-drill="${esc(`${opts.drillKind}:${b.dateKey}`)}"` : '';
    const tag = opts.drillKind ? 'button' : 'div';
    const btnType = opts.drillKind ? ' type="button"' : '';
    return `<${tag} class="spark-col${opts.drillKind ? ' spark-drill' : ''}"${btnType}${drill} title="${esc(tip)}" aria-label="${esc(tip)}">
      <div class="spark-bar-wrap" style="height:${Math.max(1, Math.round(totalH))}px">
        ${parts?.cached == null && total > 0 ? `<div class="spark-seg tok-total" style="height:${Math.round(totalH)}px"></div>` : ''}
        ${parts?.cached != null && cacH > 0 ? `<div class="spark-seg tok-cached" style="height:${Math.round(cacH)}px"></div>` : ''}
        ${parts?.cached != null && outH > 0 ? `<div class="spark-seg tok-out" style="height:${Math.round(outH)}px"></div>` : ''}
        ${parts?.cached != null && inH > 0 ? `<div class="spark-seg tok-in"  style="height:${Math.round(inH)}px"></div>` : ''}
      </div>
    </${tag}>`;
  }).join('');

  const labels = tokenBuckets
    .filter((_, i) => i === 0 || i === Math.floor(tokenBuckets.length / 2) || i === tokenBuckets.length - 1)
    .map(b => `<span>${esc(b.label)}</span>`).join('');

  return `<div class="sparkline" role="img" aria-label="${esc(opts.ariaLabel || 'Token usage')}">
    <div class="spark-bars">${bars}</div>
    <div class="spark-axis">${labels}</div>
  </div>`;
}

function usageMeasureSparklineMarkup(rollup, opts = {}) {
  if (!rollup) return '<div class="analytics-chart-empty" aria-label="No reported data">—</div>';
  const buckets = rollup.buckets;
  const maxTotal = Math.max(...buckets.map(b => b.total ?? 0), 1);
  const hasAny = buckets.some(b => b.hasData);
  if (!hasAny) return '<div class="analytics-chart-empty" aria-label="No reported data">—</div>';
  const fmt = v => analyticsFormatMeasureValue(rollup.measure, v);
  const bars = buckets.map(b => {
    const total = b.total ?? 0;
    const totalH = maxTotal > 0 ? (total / maxTotal) * 56 : 0;
    const tip = `${b.label}: ${fmt(total)} · ${b.reporting} attempts`;
    const drill = opts.drillKind ? ` data-analytics-drill="${esc(`${opts.drillKind}:${b.dateKey}`)}"` : '';
    const tag = opts.drillKind ? 'button' : 'div';
    const btnType = opts.drillKind ? ' type="button"' : '';
    return `<${tag} class="spark-col${opts.drillKind ? ' spark-drill' : ''}"${btnType}${drill} title="${esc(tip)}" aria-label="${esc(tip)}">
      <div class="spark-bar-wrap" style="height:${Math.max(1, Math.round(totalH))}px">
        ${total > 0 ? `<div class="spark-seg tok-in" style="height:${Math.round(totalH)}px"></div>` : ''}
      </div>
    </${tag}>`;
  }).join('');
  const labels = buckets
    .filter((_, i) => i === 0 || i === Math.floor(buckets.length / 2) || i === buckets.length - 1)
    .map(b => `<span>${esc(b.label)}</span>`).join('');
  return `<div class="sparkline" role="img" aria-label="${esc(rollup.measure.label)} trend">
    <div class="spark-bars">${bars}</div>
    <div class="spark-axis">${labels}</div>
  </div>`;
}

function analyticsAttemptLink(runId, attempt) {
  const act = attempt?.activationId;
  const label = attempt?.nodeId || (act ? act.split(':').at(-1) : 'Attempt');
  if (act) {
    return `<a class="analytics-run-link" href="/transcripts/${encodeURIComponent(runId)}/${encodeURIComponent(act)}">${esc(label)}</a>`;
  }
  return `<a class="analytics-run-link" href="/runs/${encodeURIComponent(runId)}">${esc(label)}</a>`;
}

function analyticsFilterBarMarkup(model) {
  const f = model.filters || analyticsDefaultFilters();
  const range = model.range || analyticsResolveRange(f);
  const labels = analyticsActiveFilterLabels(f, range);
  const stateOptions = ANALYTICS_RUN_STATES.map(st =>
    `<option value="${esc(st)}"${f.states?.includes(st) ? ' selected' : ''}>${esc(st)}</option>`
  ).join('');
  const rangeOptions = ['24h', '7d', '30d', '90d', 'custom'].map(p =>
    `<option value="${esc(p)}"${f.preset === p ? ' selected' : ''}>${esc({'24h':'Last 24 hours','7d':'Last 7 days','30d':'Last 30 days','90d':'Last 90 days',custom:'Custom range'}[p])}</option>`
  ).join('');
  const chips = labels.map(l => `<span class="analytics-filter-chip">${esc(l.text)}</span>`).join('');
  const compare = model.comparison;
  const compareNote = compare && compare.priorRunCount > 0
    ? `<p class="subtle analytics-compare">${esc(compare.priorLabel)}: ${compare.priorRunCount} runs vs ${compare.currentRunCount} in current window (identical filters).</p>`
    : '';
  const total = model.totalRuns ?? model.runs.length;
  const loaded = model.runs.length;
  const scopeNote = model.hasMore
    ? `Charts include ${loaded} of ${total} matching runs on this load; usage and cost totals below reflect only loaded attempts until more runs are retrieved.`
    : (model.pagesFetched > 1
      ? `Charts aggregate all ${total} matching runs (${model.pagesFetched} API requests). Run charts use createdAt · usage buckets use attempt time.`
      : `${total} runs in window · run charts use createdAt · usage buckets use attempt time.`);
  const paging = model.hasMore
    ? `<div class="analytics-pager"><p class="subtle" role="status">${esc(scopeNote)}</p><button type="button" class="btn ghost" data-analytics-page="${(f.page || 1) + 1}">Load more runs</button></div>`
    : `<div class="analytics-pager"><p class="subtle" role="status">${esc(scopeNote)}</p></div>`;
  return `<section class="analytics-panel analytics-filters" aria-label="Analytics filters">
    <form id="analyticsFiltersForm" class="analytics-filter-form">
      <div class="analytics-filter-row">
        <label>Range <select name="preset" aria-label="Time range preset">${rangeOptions}</select></label>
        <label class="analytics-custom-range"${f.preset === 'custom' ? '' : ' hidden'}>From <input name="from" type="datetime-local" value="${esc(analyticsDateTimeLocalValue(f.from))}"></label>
        <label class="analytics-custom-range"${f.preset === 'custom' ? '' : ' hidden'}>To <input name="to" type="datetime-local" value="${esc(analyticsDateTimeLocalValue(f.to))}"></label>
        <details class="analytics-advanced"><summary>More filters</summary><div class="analytics-advanced-fields"><label>State <select name="state" multiple size="3" aria-label="Run states">${stateOptions}</select></label>

        <label>Executor <input name="executor" value="${esc(f.executor)}" placeholder="executor filter"></label>
        <label>Label timezone <select name="tz" aria-label="Chart label timezone"><option value="UTC"${f.tz === 'UTC' ? ' selected' : ''}>UTC</option><option value="${esc(Intl.DateTimeFormat().resolvedOptions().timeZone)}"${f.tz !== 'UTC' ? ' selected' : ''}>Local</option></select></label>
        </div></details><label>Workflow <input name="workflow" value="${esc(f.workflow)}" placeholder="All workflows"></label><button type="submit" class="btn">Apply filters</button>
        <button type="button" class="btn ghost" id="analyticsResetFilters">Reset</button>
      </div>
    </form>
    <div class="analytics-filter-chips" aria-live="polite">${chips}</div>
    ${compareNote}
    ${paging}
  </section>`;
}

function analyticsDrillRecords(model, drill, measureId) {
  if (!drill) return [];
  const [kind, key] = drill.split(/:(.+)/);
  const tz = model.filters?.tz || 'UTC';
  const range = model.range;
  if (kind === 'runVolume') {
    return model.runs.filter(r => {
      const ms = Date.parse(r.createdAt);
      const b = analyticsBucketForTimestamp(ms, range, tz);
      return b && b.dateKey === key;
    }).map(r => ({ kind: 'run', run: r }));
  }
  if (kind === 'tokenVolume') {
    const out = [];
    for (const { attempt, run } of model.flatAttempts) {
      const bucketTime = analyticsAttemptBucketTime(attempt, run);
      if (!bucketTime) continue;
      const b = analyticsBucketForTimestamp(bucketTime.ms, range, tz);
      if (b?.dateKey !== key) continue;
      out.push({ kind: 'attempt', run, attempt });
    }
    return out;
  }
  if (kind === 'measureVolume' && measureId) {
    const rollup = model.measureRollups?.get(measureId);
    return (rollup?.evidence || []).filter(row => {
      const matchRow = [...model.flatAttempts].find(({ attempt, run }) =>
        run.id === row.runId && (attempt.id || '') === row.attemptId);
      if (!matchRow) return false;
      const bt = analyticsAttemptBucketTime(matchRow.attempt, matchRow.run);
      if (!bt) return false;
      const b = analyticsBucketForTimestamp(bt.ms, range, tz);
      return b?.dateKey === key && row.value != null;
    }).map(row => {
      const match = [...model.flatAttempts].find(({ attempt, run }) => run.id === row.runId && (attempt.id || '') === row.attemptId);
      return { kind: 'attempt', run: match.run, attempt: match.attempt, value: row.value, measureId };
    });
  }
  if (kind === 'executor') {
    return model.flatAttempts.filter(({ attempt }) => (attempt.executor || 'unknown') === key)
      .map(({ attempt, run }) => ({ kind: 'attempt', run, attempt }));
  }
  if (kind === 'workflow') {
    return model.flatAttempts.filter(({ run }) => (run.workflowName || 'Unnamed workflow') === key)
      .map(({ attempt, run }) => ({ kind: 'attempt', run, attempt }));
  }
  if (kind === 'latencyWorkflow') {
    return model.flatAttempts.filter(({ run }) => (run.workflowName || 'Unnamed workflow') === key)
      .map(({ attempt, run }) => ({ kind: 'attempt', run, attempt }));
  }
  if (kind === 'latencyExecutor') {
    return model.flatAttempts.filter(({ attempt }) => (attempt.executor || 'unknown') === key)
      .map(({ attempt, run }) => ({ kind: 'attempt', run, attempt }));
  }
  return [];
}

function analyticsDrilldownMarkup(model, drill, measureId) {
  if (!drill) return '';
  const records = analyticsDrillRecords(model, drill, measureId);
  const [kind, key] = drill.split(/:(.+)/);
  const kindLabels={runVolume:'Runs started',tokenVolume:'Token usage',measureVolume:'Usage',executor:'Executor',workflow:'Workflow',latencyWorkflow:'Workflow timing',latencyExecutor:'Executor timing'};
  const title = `${kindLabels[kind]||'Selected records'} · ${key||''}`;
  const measure = measureId ? model.measureRollups?.get(measureId)?.measure : null;
  const evidence=model.measureRollups?.get(measureId)?.evidence||[];
  const rows = records.slice(0, 100).map(rec => {
    if(rec.kind==='attempt' && rec.value==null){
      const match=evidence.find(row=>row.runId===rec.run.id && row.attemptId===(rec.attempt.id||''));
      if(match?.value!=null)rec={...rec,value:match.value};
    }
    if (rec.kind === 'run') {
      return `<tr><td>Run</td><td>${esc(rec.run.workflowName || 'Unnamed workflow')}</td><td><a class="analytics-run-link" href="/runs/${encodeURIComponent(rec.run.id)}">Open run ↗</a></td><td>—</td><td>${esc(rec.run.state)}</td><td class="subtle">createdAt</td></tr>`;
    }
    return `<tr><td>Attempt</td><td>${esc(rec.run.workflowName || 'Unnamed workflow')}</td><td><a class="analytics-run-link" href="/runs/${encodeURIComponent(rec.run.id)}">Open run ↗</a></td><td>${analyticsAttemptLink(rec.run.id, rec.attempt)}</td><td>${esc(rec.attempt.executor || 'unknown')} · ${esc(rec.attempt.state)}</td><td class="num">${measure && rec.value != null ? esc(analyticsFormatMeasureValue(measure, rec.value)) : '—'}</td></tr>`;
  }).join('');
  return `<dialog class="analytics-panel analytics-drilldown" id="analyticsDrilldown" aria-labelledby="analyticsDrillTitle">
    <div class="analytics-drill-head"><div><p class="analytics-eyebrow">Selected chart records</p><h2 id="analyticsDrillTitle">${esc(title)}</h2></div><button type="button" class="btn ghost" id="analyticsCloseDrill">Close</button></div>
    <p class="subtle">${records.length} ${records.length===1?'record':'records'} in the current filters${records.length > 100 ? ' (first 100 shown)' : ''}</p>
    <div class="tablebox"><table><thead><tr><th>Type</th><th>Workflow</th><th>Run</th><th>Attempt</th><th>Executor / state</th><th>${esc(measure?.label||'Usage')}</th></tr></thead><tbody>${rows || '<tr><td colspan="6">No matching records for this selection.</td></tr>'}</tbody></table></div>
  </dialog>`;
}

function analyticsWireInteractiveUI(container, model, options = {}) {
  const applyFilters = (filters, local=false) => {
    const hash = analyticsFiltersToURL(filters);
    if(local){
      window.history.pushState(null,'',hash);
      model.filters={...filters};
      renderAnalytics(model,{measureId:filters.measureId,drill:filters.drill});
      return;
    }
    if (currentRoute() !== hash) navigateTo(hash);
    else loadAnalytics(filters);
  };
  const form = container.querySelector('#analyticsFiltersForm');
  if (form) {
    form.elements.preset.onchange = () => {
      container.querySelectorAll('.analytics-custom-range').forEach(el => { el.hidden = form.elements.preset.value !== 'custom'; });
    };
    form.onsubmit = event => {
      event.preventDefault();
      const states = [...form.elements.state.selectedOptions].map(o => o.value);
      const filters = {
        ...analyticsDefaultFilters(),
        preset: form.elements.preset.value,
        states,
        workflow: form.elements.workflow.value.trim(),
        executor: form.elements.executor.value.trim(),
        tz: form.elements.tz.value,
        measureId: options.measureId || model.defaultMeasure,
        drill: options.drill || '',
        page: 1,
      };
      if (filters.preset === 'custom') {
        filters.from = form.elements.from.value ? new Date(form.elements.from.value).toISOString() : filters.from;
        filters.to = form.elements.to.value ? new Date(form.elements.to.value).toISOString() : filters.to;
      }
      applyFilters(filters);
    };
  }
  const reset = container.querySelector('#analyticsResetFilters');
  if (reset) reset.onclick = () => applyFilters(analyticsDefaultFilters());
  container.querySelectorAll('[data-analytics-page]').forEach(button => {
    button.onclick = () => applyFilters({ ...(model.filters || analyticsDefaultFilters()), page: Number(button.dataset.analyticsPage) || 1 });
  });
  container.querySelectorAll('[data-analytics-drill]').forEach(el => {
    el.onclick = () => {
      const drill = el.getAttribute('data-analytics-drill');
      const filters = { ...(model.filters || analyticsDefaultFilters()), drill, measureId: options.measureId || model.defaultMeasure };
      applyFilters(filters,true);
    };
  });
  const close = container.querySelector('#analyticsCloseDrill');
  if (close) close.onclick = () => {
    const filters = { ...(model.filters || analyticsDefaultFilters()), drill: '', measureId: options.measureId || model.defaultMeasure };
    const drill=options.drill;
    applyFilters(filters,true);
    const trigger=[...container.querySelectorAll('[data-analytics-drill]')].find(el=>el.getAttribute('data-analytics-drill')===drill);
    trigger?.focus();
  };
  const select = container.querySelector('#analyticsMeasureSelect');
  if (select) {
    select.onchange = () => {
      const filters = { ...(model.filters || analyticsDefaultFilters()), measureId: select.value, drill: options.drill || '' };
      applyFilters(filters,true);
      container.querySelector('#analyticsMeasureSelect')?.focus();
    };
  }
}

function analyticsUsageHeadMarkup(model) {
  return '';
}

function analyticsMeasureToolbarMarkup(model, selectedId) {
  if (!model.measures?.length) {
    return `<div class="analytics-panel subtle" role="status">No usage measures reported across loaded attempts.</div>`;
  }
  const id = selectedId || model.defaultMeasure;
  const options = model.measures.map(m =>
    `<option value="${esc(m.id)}"${m.id === id ? ' selected' : ''}>${esc(m.label)}</option>`
  ).join('');
  return `<div class="analytics-toolbar">
      <label class="analytics-measure-label">Measure
        <select id="analyticsMeasureSelect" class="analytics-measure-select" aria-label="Select usage measure">${options}</select>
      </label>
    </div>`;
}

function analyticsDetailsMarkup(model, measureId) {
  const rows = [...model.executorStats].map(([label, stats]) => ({ label, value: stats.attempts })).sort((a,b)=>b.value-a.value).slice(0,10);
  const durationRows = [...model.executorStats].filter(([,s])=>s.durationSamples).map(([label,s])=>({label,value:s.durationSumMs/s.durationSamples})).sort((a,b)=>b.value-a.value).slice(0,10);
  const duration = value => fmtDuration(value);
  const card = (title, items, opts) => `<section class="analytics-chart-card"><h2>${title}</h2>${items.length?barChartMarkup(items,opts):'<div class="analytics-chart-empty" aria-label="No reported data">—</div>'}</section>`;
  const usageRows = measureId && model.measureRollups?.get(measureId)?.byExecutor?.slice(0,10).map(row=>({label:row.label,value:row.value})) || [];
  const usageFmt = value => analyticsFormatMeasureValue(model.measureRollups.get(measureId).measure,value);
  return `<div class="analytics-chart-grid analytics-executor-charts">
    ${card('Attempts by executor',rows,{drillKind:'executor'})}
    ${card('Average worker time',durationRows,{drillKind:'executor',fmt:duration,colorVar:'--blue'})}
    ${card('Usage by executor',usageRows,{drillKind:'executor',fmt:usageFmt,colorVar:'--amber'})}
  </div>`;
}

function renderAnalytics(model, options = {}) {
  const { buckets, range, filters } = model;
  const container = document.getElementById('analyticsContent');
  if (!container) return;
  container._analyticsModel = model;

  const measureId = options.measureId || filters?.measureId || model.defaultMeasure;
  const drill = options.drill ?? filters?.drill ?? '';
  const preset = filters?.preset || '30d';
  const windowLabel = preset === '30d' ? '30 days' : (range?.label || 'selected window');
  const emptyRunLabel = preset === '30d' ? 'No runs in the last 30 days.' : 'No runs in the selected window.';
  const rollup = measureId ? model.measureRollups?.get(measureId) : null;
  const measureFmt = v => rollup ? analyticsFormatMeasureValue(rollup.measure, v) : fmtTok(v);

  const empty = '<div class="analytics-chart-empty" aria-label="No reported data">—</div>';
  const bars = (items, opts) => items.length ? barChartMarkup(items, opts) : empty;
  const card = (title, chart, wide = false) => `<section class="analytics-chart-card${wide ? ' analytics-chart-wide' : ''}"><h2>${title}</h2>${chart}</section>`;
  const legend = items => `<div class="spark-legend">${items.map(([label, cls]) => `<span class="spark-legend-item"><span class="spark-swatch ${cls}"></span>${label}</span>`).join('')}</div>`;

  const executorBreakdown = rollup ? rollup.byExecutor.slice(0, 10).map(row => ({ label: row.label, value: row.value })) : [];
  const workflowBreakdown = rollup ? rollup.byWorkflow.slice(0, 10).map(row => ({ label: row.label, value: row.value })) : [];

  const warning = model.attemptErrors?.size ? `<details class="analytics-data-status" role="status"><summary>Partial data · ${model.attemptErrors.size} runs</summary><ul>${[...model.attemptErrors].map(([id,error])=>`<li>Statistics are partial · <a href="/runs/${encodeURIComponent(id)}">${esc(id)}</a> · ${esc(error)}</li>`).join('')}</ul></details>` : '';
  const sparkOpts = { drillKind: 'runVolume', ariaLabel: `Run volume · ${windowLabel}`, emptyLabel: emptyRunLabel };
  const measureSparkOpts = { drillKind: 'measureVolume', ariaLabel: `${rollup?.measure?.label || 'Usage'} · ${windowLabel}` };

  container.innerHTML = analyticsFilterBarMarkup(model)
    + warning
    + (drill ? analyticsDrilldownMarkup(model, drill, measureId) : '')
    + (model.measures?.length ? `<section class="analytics-chart-card analytics-chart-wide analytics-usage-trends"><div class="analytics-usage-head"><h2>Usage</h2>${analyticsMeasureToolbarMarkup(model, measureId)}</div>
      ${usageMeasureSparklineMarkup(rollup, measureSparkOpts)}
      <div class="analytics-breakdown-grid">
        <div><h3 class="analytics-breakdown-title">By executor</h3>${bars(executorBreakdown, { fmt: measureFmt, drillKind: 'executor' })}</div>
        <div><h3 class="analytics-breakdown-title">By workflow</h3>${bars(workflowBreakdown, { fmt: measureFmt, drillKind: 'workflow' })}</div>
      </div></section>` : '')
    + `<div class="analytics-chart-grid analytics-primary-charts">`
    + card(`Run outcomes · ${esc(windowLabel)}`, sparklineMarkup(buckets, sparkOpts) + legend([['Succeeded', 'succeeded'], ['Failed', 'failed'], ['Other', 'pending']]))
    + '</div>'
    + analyticsDetailsMarkup(model, measureId)
    + analyticsRetryLatencyMarkup(model);

  if (typeof container.querySelector === 'function') {
    analyticsWireInteractiveUI(container, model, { measureId, drill });
    if (drill) {
      const panel=container.querySelector('#analyticsDrilldown');
      if(panel?.showModal){panel.showModal();panel.addEventListener('cancel',event=>{event.preventDefault();container.querySelector('#analyticsCloseDrill')?.click()});}
    }
  }
}

let analyticsRequest = 0;
let analyticsController = null;
async function loadAnalytics(filters) {
  analyticsController?.abort();
  analyticsController = new AbortController();
  const controller = analyticsController;
  const request = ++analyticsRequest;
  const container = document.getElementById('analyticsContent');
  if (container) container.innerHTML = '<div class="loading" role="status" aria-live="polite">Loading analytics…</div>';
  filters = filters || analyticsFiltersFromURL();
  try {
    const model = await loadAnalyticsData(filters, controller.signal);
    if (request !== analyticsRequest || controller.signal.aborted) return;
    const pageAligned = !model.hasMore && model.runs.length >= (model.totalRuns ?? model.runs.length);
    model.filters = {
      ...filters,
      page: pageAligned ? 1 : (filters.page || 1),
      measureId: filters.measureId || model.defaultMeasure,
    };
    renderAnalytics(model, { measureId: model.filters.measureId, drill: filters.drill });
    if (pageAligned && (filters.page || 1) > 1) {
      const hash = analyticsFiltersToURL(model.filters);
      if (currentRoute() !== hash) window.history.replaceState(null, '', hash);
    }
  } catch (err) {
    if (request !== analyticsRequest || controller.signal.aborted) return;
    if (container) container.innerHTML = `<div class="notice error" role="alert">Unable to load analytics: ${esc(err.message)}</div>`;
  }
}
