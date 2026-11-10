/* ── analytics ─────────────────────────────────── */
// Cross-run operational insights: run volume, outcomes, timing, and
// provider-reported usage (tokens, credits, costs). Aggregated client-side
// from runs + attempts using the shared UsageSummary semantics.

async function loadAnalyticsDataLegacy(filters, range, attemptErrors = new Map(), signal) {
  const runsData = await api('/v1/runs', { signal });
  if (runsData.runs !== null && !Array.isArray(runsData.runs)) throw new Error('Invalid runs response');
  const attemptsByRun = new Map();
  let runs = (runsData.runs || []).slice().sort((a, b) => new Date(a.createdAt) - new Date(b.createdAt));
  runs = runs.filter(r => analyticsRunInRange(r, range));
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
  const applied = analyticsApplyFiltersToModelInputs(runs, attemptsByRun, filters, range);
  return buildAnalyticsModel(applied.runs, applied.attemptsByRun, attemptErrors, {
    range, filters, totalRuns: applied.runs.length, hasMore: false, legacy: true,
  });
}

async function loadAnalyticsData(filters = analyticsFiltersFromHash(), signal) {
  const range = analyticsResolveRange(filters);
  const attemptErrors = new Map();
  const qs = analyticsFiltersQueryString(filters, range);
  let data;
  try {
    data = await api(`/v1/analytics?${qs}`, { signal });
  } catch (err) {
    if (/HTTP 404|unavailable/i.test(err?.message || '')) {
      return loadAnalyticsDataLegacy(filters, range, attemptErrors, signal);
    }
    throw err;
  }
  if (!Array.isArray(data.runs)) throw new Error('Invalid analytics runs response');
  const runs = (data.runs || []).slice().sort((a, b) => new Date(a.createdAt) - new Date(b.createdAt));
  const attemptsByRun = new Map(Object.entries(data.attemptsByRun || {}).map(([id, list]) => [id, Array.isArray(list) ? list : []]));
  const model = buildAnalyticsModel(runs, attemptsByRun, attemptErrors, {
    range,
    filters,
    totalRuns: data.totalRuns ?? runs.length,
    hasMore: !!data.hasMore,
    populations: data.populations,
    offset: data.offset,
  });
  if (model.runs.length >= 3) {
    const prior = analyticsPriorRange(range);
    const priorFilters = { ...filters, page: 1 };
    const priorQs = analyticsFiltersQueryString(priorFilters, prior);
    try {
      const priorData = await api(`/v1/analytics?${priorQs}`, { signal });
      const priorRuns = priorData.runs || [];
      if (priorRuns.length) {
        model.comparison = {
          priorRunCount: priorRuns.length,
          currentRunCount: model.runs.length,
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
  const states = ['created', 'running', 'waiting', 'paused', 'succeeded', 'failed', 'cancelled'];
  const stateCounts = Object.fromEntries(states.map(s => [s, 0]));
  for (const r of runs) {
    if (stateCounts[r.state] !== undefined) stateCounts[r.state]++;
  }

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
  if (inN == null || outN == null) {
    if (inN != null && outN == null && cachedN == null) {
      return { volume: inN, nonCachedInput: inN, cached: 0, output: 0 };
    }
    if (inN == null && outN != null && cachedN == null) {
      return { volume: outN, nonCachedInput: 0, cached: 0, output: outN };
    }
    return null;
  }
  if (cachedN == null) {
    return { volume: inN + outN, nonCachedInput: inN, cached: 0, output: outN };
  }
  if (cachedN > inN) return null;
  return {
    volume: inN + outN,
    nonCachedInput: inN - cachedN,
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
        ${cacH > 0 ? `<div class="spark-seg tok-cached" style="height:${Math.round(cacH)}px"></div>` : ''}
        ${outH > 0 ? `<div class="spark-seg tok-out" style="height:${Math.round(outH)}px"></div>` : ''}
        ${inH > 0 ? `<div class="spark-seg tok-in"  style="height:${Math.round(inH)}px"></div>` : ''}
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
  const label = attempt?.id || act || '—';
  if (act) {
    return `<a class="analytics-run-link" href="#transcript/${encodeURIComponent(runId)}/${encodeURIComponent(act)}">${esc(label)}</a>`;
  }
  return `<a class="analytics-run-link" href="#run/${encodeURIComponent(runId)}">${esc(label)}</a>`;
}

function analyticsFilterBarMarkup(model) {
  const f = model.filters || analyticsDefaultFilters();
  const range = model.range || analyticsResolveRange(f);
  const labels = analyticsActiveFilterLabels(f, range);
  const stateOptions = ANALYTICS_RUN_STATES.map(st =>
    `<option value="${esc(st)}"${f.states?.includes(st) ? ' selected' : ''}>${esc(st)}</option>`
  ).join('');
  const rangeOptions = ['24h', '7d', '30d', '90d', 'custom'].map(p =>
    `<option value="${esc(p)}"${f.preset === p ? ' selected' : ''}>${esc(p)}</option>`
  ).join('');
  const chips = labels.map(l => `<span class="analytics-filter-chip">${esc(l.text)}</span>`).join('');
  const compare = model.comparison;
  const compareNote = compare && compare.priorRunCount > 0
    ? `<p class="subtle analytics-compare">${esc(compare.priorLabel)}: ${compare.priorRunCount} runs vs ${compare.currentRunCount} in current window (identical filters).</p>`
    : '';
  const paging = model.hasMore
    ? `<div class="analytics-pager"><p class="subtle" role="status">Page ${f.page || 1} · showing ${model.runs.length} of ${model.totalRuns} matching runs.</p><button type="button" class="btn ghost" data-analytics-page="${Math.max(1, (f.page || 1) - 1)}"${(f.page || 1) <= 1 ? ' disabled' : ''}>Previous</button><button type="button" class="btn ghost" data-analytics-page="${(f.page || 1) + 1}">Next</button></div>`
    : `<div class="analytics-pager"><p class="subtle">${model.totalRuns ?? model.runs.length} runs in window · run charts use createdAt · usage buckets use attempt time.</p>${(f.page || 1) > 1 ? `<button type="button" class="btn ghost" data-analytics-page="${(f.page || 1) - 1}">Previous</button>` : ''}</div>`;
  return `<section class="analytics-panel analytics-filters" aria-label="Analytics filters">
    <form id="analyticsFiltersForm" class="analytics-filter-form">
      <div class="analytics-filter-row">
        <label>Range <select name="preset" aria-label="Time range preset">${rangeOptions}</select></label>
        <label class="analytics-custom-range"${f.preset === 'custom' ? '' : ' hidden'}>From <input name="from" type="datetime-local" value="${esc(analyticsDateTimeLocalValue(f.from))}"></label>
        <label class="analytics-custom-range"${f.preset === 'custom' ? '' : ' hidden'}>To <input name="to" type="datetime-local" value="${esc(analyticsDateTimeLocalValue(f.to))}"></label>
        <label>State <select name="state" multiple size="3" aria-label="Run states">${stateOptions}</select></label>
        <label>Workflow <input name="workflow" value="${esc(f.workflow)}" placeholder="name filter"></label>
        <label>Executor <input name="executor" value="${esc(f.executor)}" placeholder="executor filter"></label>
        <label>Label timezone <select name="tz" aria-label="Chart label timezone"><option value="UTC"${f.tz === 'UTC' ? ' selected' : ''}>UTC</option><option value="${esc(Intl.DateTimeFormat().resolvedOptions().timeZone)}"${f.tz !== 'UTC' ? ' selected' : ''}>Local</option></select></label>
        <button type="submit" class="btn">Apply</button>
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
      const ms = row.bucketProvenance ? null : null;
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
  return [];
}

function analyticsDrilldownMarkup(model, drill, measureId) {
  if (!drill) return '';
  const records = analyticsDrillRecords(model, drill, measureId);
  const title = drill.replace(':', ' · ');
  const measure = measureId ? model.measureRollups?.get(measureId)?.measure : null;
  const rows = records.slice(0, 100).map(rec => {
    if (rec.kind === 'run') {
      return `<tr><td>Run</td><td>${esc(rec.run.workflowName || 'Unnamed workflow')}</td><td><a class="analytics-run-link" href="#run/${encodeURIComponent(rec.run.id)}">${esc(rec.run.id)}</a></td><td>—</td><td>${esc(rec.run.state)}</td><td class="subtle">createdAt</td></tr>`;
    }
    return `<tr><td>Attempt</td><td>${esc(rec.run.workflowName || 'Unnamed workflow')}</td><td><a class="analytics-run-link" href="#run/${encodeURIComponent(rec.run.id)}">${esc(rec.run.id)}</a></td><td>${analyticsAttemptLink(rec.run.id, rec.attempt)}</td><td>${esc(rec.attempt.executor || 'unknown')} · ${esc(rec.attempt.state)}</td><td class="num">${measure && rec.value != null ? esc(analyticsFormatMeasureValue(measure, rec.value)) : '—'}</td></tr>`;
  }).join('');
  return `<section class="analytics-panel analytics-drilldown" id="analyticsDrilldown" tabindex="-1" aria-label="Contributing records">
    <div class="analytics-drill-head"><h2>Contributing records</h2><button type="button" class="btn ghost" id="analyticsCloseDrill">Close</button></div>
    <p class="subtle">${esc(title)} · ${records.length} records${records.length > 100 ? ' (first 100 shown)' : ''}</p>
    <div class="tablebox"><table><thead><tr><th>Type</th><th>Workflow</th><th>Run</th><th>Attempt</th><th>Detail</th><th>Value</th></tr></thead><tbody>${rows || '<tr><td colspan="6">No records</td></tr>'}</tbody></table></div>
  </section>`;
}

function analyticsWireInteractiveUI(container, model, options = {}) {
  const applyHash = filters => {
    const hash = analyticsFiltersToHash(filters);
    if (location.hash !== hash) location.hash = hash;
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
      applyHash(filters);
    };
  }
  const reset = container.querySelector('#analyticsResetFilters');
  if (reset) reset.onclick = () => applyHash(analyticsDefaultFilters());
  container.querySelectorAll('[data-analytics-page]').forEach(button => {
    button.onclick = () => applyHash({ ...(model.filters || analyticsDefaultFilters()), page: Number(button.dataset.analyticsPage) || 1 });
  });
  container.querySelectorAll('[data-analytics-drill]').forEach(el => {
    el.onclick = () => {
      const drill = el.getAttribute('data-analytics-drill');
      const filters = { ...(model.filters || analyticsDefaultFilters()), drill, measureId: options.measureId || model.defaultMeasure };
      applyHash(filters);
    };
  });
  const close = container.querySelector('#analyticsCloseDrill');
  if (close) close.onclick = () => {
    const filters = { ...(model.filters || analyticsDefaultFilters()), drill: '', measureId: options.measureId || model.defaultMeasure };
    applyHash(filters);
  };
  const select = container.querySelector('#analyticsMeasureSelect');
  if (select) {
    select.onchange = () => {
      const filters = { ...(model.filters || analyticsDefaultFilters()), measureId: select.value, drill: options.drill || '' };
      renderAnalytics(model, { measureId: select.value, drill: filters.drill });
      select.focus();
    };
  }
}

function analyticsUsageHeadMarkup(model) {
  const presentation = buildUsagePresentation(model.usageSummary || {}, { attemptCount: model.attemptCount });
  if (presentation.status === 'none') {
    return `<div class="analytics-panel" role="status"><p class="subtle">No attempts loaded yet. Usage subtotals appear when executor-reported measurements are available.</p></div>`;
  }
  const measures = presentation.primary.map(m =>
    `<div class="analytics-usage-measure"><span class="analytics-usage-val">${esc(usageMeasureValue(m))}</span><span class="analytics-usage-label">${esc(m.label)}${esc(m.coverage || '')}</span></div>`
  ).join('');
  const provenance = [];
  const bp = model.bucketProvenance || {};
  if (bp.finishedAt) provenance.push(`${bp.finishedAt} attempts bucketed by finishedAt`);
  if (bp.startedAt) provenance.push(`${bp.startedAt} by startedAt`);
  if (bp.runCreatedAt) provenance.push(`${bp.runCreatedAt} by run createdAt (legacy fallback)`);
  const provNote = provenance.length
    ? `<p class="subtle analytics-provenance">Usage trends: ${esc(provenance.join('; '))}. Subtotals are reported values with coverage, not complete spend.</p>`
    : `<p class="subtle analytics-provenance">Usage trends bucket by attempt finishedAt, then startedAt, then run createdAt. Subtotals are reported values with coverage, not complete spend.</p>`;
  return `<div class="analytics-panel analytics-usage-head"><h2>Reported usage</h2>${provNote}<div class="analytics-usage-primary">${measures || '<span class="subtle">No usage reported</span>'}</div></div>`;
}

function analyticsMeasureToolbarMarkup(model, selectedId) {
  if (!model.measures?.length) {
    return `<div class="analytics-panel subtle" role="status">No usage measures reported across loaded attempts.</div>`;
  }
  const id = selectedId || model.defaultMeasure;
  const options = model.measures.map(m =>
    `<option value="${esc(m.id)}"${m.id === id ? ' selected' : ''}>${esc(m.label)}</option>`
  ).join('');
  return `<h2>Usage trend and breakdown</h2>
    <div class="analytics-toolbar">
      <label class="analytics-measure-label">Measure
        <select id="analyticsMeasureSelect" class="analytics-measure-select" aria-label="Select usage measure">${options}</select>
      </label>
    </div>`;
}

function analyticsDetailsMarkup(model, measureId) {
  const runLinks = ids => [...ids].map(id => `<a class="analytics-run-link" href="#run/${encodeURIComponent(id)}">${esc(id)}</a>`).join(', ');
  const notes = [];
  if (model.runsWithNoAttempts) notes.push(`${model.runsWithNoAttempts} runs have no attempt records.`);
  if (model.runsWithUnavailableUsage) notes.push(`${model.runsWithUnavailableUsage} runs have incomplete token data: no provider-reported token usage for some measurements.`);
  for (const [id, error] of model.attemptErrors || []) notes.push(`Run ${runLinks([id])}: ${esc(error)}; statistics are partial.`);
  const sample = (total, count) => count ? `${esc(fmtDuration(total / count))} (n=${count})` : 'Unavailable';

  const rollup = measureId ? model.measureRollups?.get(measureId) : null;
  const measureRows = (rollup?.evidence || []).filter(row => row.value != null).slice(0, 200);
  const chartSubtotal = rollup?.subtotal ?? null;
  const evidenceSum = measureRows.reduce((acc, row) => acc + row.value, 0);

  const rows = [...model.executorStats].map(([name, stats]) => {
    const usage = model.executorUsage.get(name);
    const values = ['input', 'output', 'cached'].map(field => `<td class="num">${esc(fmtTok(usage?.[field]))}</td>`).join('');
    const complete = usage && ['input', 'output', 'cached'].every(field => usage[field] != null);
    const total = complete ? (analyticsTokenVolume(usage.input, usage.output, usage.cached)?.volume ?? null) : null;
    return { name, stats, values, total };
  });
  const maxTokens = Math.max(1, ...rows.map(row => row.total ?? 0));

  const reconcile = chartSubtotal != null && Math.abs(chartSubtotal - evidenceSum) < 0.0001
    ? `<p class="subtle">Chart subtotal ${esc(analyticsFormatMeasureValue(rollup.measure, chartSubtotal))} matches ${measureRows.length} listed evidence rows.</p>`
  : chartSubtotal != null
    ? `<p class="subtle">Chart subtotal ${esc(analyticsFormatMeasureValue(rollup.measure, chartSubtotal))}; visible evidence rows sum ${esc(analyticsFormatMeasureValue(rollup.measure, evidenceSum))} (table may be truncated).</p>`
    : '';

  const evidenceTable = rollup ? `<div class="tablebox"><table><thead><tr><th>Executor</th><th>Workflow</th><th>Run</th><th>Attempt</th><th>${esc(rollup.measure.label)}</th><th>Bucket time</th></tr></thead><tbody>
    ${measureRows.map(row => `<tr>
      <td>${esc(row.executor)}</td>
      <td>${esc(row.workflow)}</td>
      <td>${runLinks([row.runId])}</td>
      <td>${esc(row.attemptId || '—')}</td>
      <td class="num">${esc(analyticsFormatMeasureValue(rollup.measure, row.value))}</td>
      <td class="subtle">${esc(row.bucketProvenance || '—')}</td>
    </tr>`).join('')}
    </tbody></table></div>${reconcile}` : '';

  return `<details class="analytics-details"><summary>Data details and evidence</summary>
    ${notes.length ? `<div role="status">${notes.map(note => `<p>${note}</p>`).join('')}</div>` : ''}
    ${evidenceTable}
    <div class="tablebox"><table><thead><tr><th>Agent</th><th>Worker time</th><th>Coordinator elapsed</th><th>Timing availability</th><th>Input tokens</th><th>Output tokens</th><th>Cached tokens</th><th>Relative token volume</th><th>Runs</th></tr></thead><tbody>
    ${rows.map(({ name, stats, values, total }) => `<tr><td><strong>${esc(name)}</strong></td><td>${sample(stats.durationSumMs, stats.durationSamples)}</td><td>${sample(stats.elapsedSumMs, stats.elapsedSamples)}</td><td>${stats.missingTiming} missing timing; ${stats.running} in progress</td>${values}<td>${total == null ? 'Unavailable' : `<span class="abc-bar" style="display:block;width:${(100 * total / maxTokens).toFixed(1)}%">${esc(fmtTok(total))}</span>`}</td><td>${runLinks(stats.runIds)}</td></tr>`).join('')}
    </tbody></table></div></details>`;
}

function renderAnalytics(model, options = {}) {
  const { runs, buckets, tokenBuckets, executorStats, range, filters } = model;
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

  const workflows = new Map();
  for (const run of runs) {
    if (!['succeeded', 'failed', 'cancelled'].includes(run.state)) continue;
    const ms = Date.parse(run.updatedAt) - Date.parse(run.createdAt);
    if (!Number.isFinite(ms) || ms < 0) continue;
    const name = run.workflowName || 'Unnamed workflow';
    if (!workflows.has(name)) workflows.set(name, []);
    workflows.get(name).push(ms);
  }
  const workflowTime = [...workflows].map(([label, values]) => {
    values.sort((a, b) => a - b);
    const mid = Math.floor(values.length / 2);
    return { label, value: values.length % 2 ? values[mid] : (values[mid - 1] + values[mid]) / 2 };
  }).sort((a, b) => b.value - a.value).slice(0, 10);

  const agentTiming = [...(executorStats || [])].map(([label, s]) => ({
    label,
    low: s.taskTimeMin ?? null,
    high: s.taskTimeMax ?? null,
    average: s.taskTimeCount ? s.taskTimeTotal / s.taskTimeCount : null,
    count: s.taskTimeCount || 0,
    fallback: s.elapsedSamples || 0,
  })).sort((a, b) => (b.average ?? -1) - (a.average ?? -1));

  const failureRates = [...(executorStats || [])].map(([label, s]) => ({
    label,
    value: s.failed + s.succeeded ? 100 * s.failed / (s.failed + s.succeeded) : null,
  })).filter(x => x.value != null).sort((a, b) => b.value - a.value).slice(0, 10);

  const empty = '<div class="analytics-chart-empty" aria-label="No reported data">—</div>';
  const bars = (items, opts) => items.length ? barChartMarkup(items, opts) : empty;
  const card = (title, chart, wide = false) => `<section class="analytics-chart-card${wide ? ' analytics-chart-wide' : ''}"><h2>${title}</h2>${chart}</section>`;
  const legend = items => `<div class="spark-legend">${items.map(([label, cls]) => `<span class="spark-legend-item"><span class="spark-swatch ${cls}"></span>${label}</span>`).join('')}</div>`;

  const rawMax = Math.max(1, ...agentTiming.map(a => a.high ?? 0));
  const magnitude = 10 ** Math.floor(Math.log10(rawMax));
  const maxTiming = ([1, 2, 5, 10].find(n => n * magnitude >= rawMax) || 10) * magnitude;

  const timingChart = agentTiming.length ? `<div class="adapter-timing-chart">${agentTiming.map(agent => {
    const tooltip = `${agent.count} completed attempts; ${agent.fallback} use timestamp elapsed time, including queue and dispatch delays`;
    return `<div class="adapter-timing-group"><div class="adapter-timing-identity"><strong>${esc(agent.label)}</strong><span title="Completed attempts">n=${agent.count}</span></div><div class="adapter-timing-bars" title="${esc(tooltip)}">${[['low', 'Lowest'], ['average', 'Average'], ['high', 'Highest']].map(([field, label]) => {
      const value = agent[field];
      return `<div class="adapter-timing-series timing-${field}"><span class="adapter-series-label">${label}</span><div class="adapter-series-track"><span class="adapter-series-bar${value === 0 ? ' timing-zero' : ''}" style="width:${value == null ? 0 : 100 * value / maxTiming}%"></span></div><span class="adapter-series-value">${value == null ? '—' : esc(fmtDuration(value))}</span></div>`;
    }).join('')}</div></div>`;
  }).join('')}<div class="adapter-timing-axis"><span></span><div><span>0</span><span>${esc(fmtDuration(maxTiming / 2))}</span><span>${esc(fmtDuration(maxTiming))}</span></div></div></div>` : empty;

  const executorBreakdown = rollup ? rollup.byExecutor.slice(0, 10).map(row => ({ label: row.label, value: row.value })) : [];
  const workflowBreakdown = rollup ? rollup.byWorkflow.slice(0, 10).map(row => ({ label: row.label, value: row.value })) : [];

  const warning = model.attemptErrors?.size ? `<div class="analytics-data-status" role="status" title="Some attempt records could not be loaded">Partial data · ${model.attemptErrors.size} runs</div>` : '';
  const sparkOpts = { drillKind: 'runVolume', ariaLabel: `Run volume · ${windowLabel}`, emptyLabel: emptyRunLabel };
  const tokenOpts = { drillKind: 'tokenVolume', ariaLabel: `Token usage · ${windowLabel}` };
  const measureSparkOpts = { drillKind: 'measureVolume', ariaLabel: `${rollup?.measure?.label || 'Usage'} · ${windowLabel}` };

  container.innerHTML = analyticsFilterBarMarkup(model)
    + warning
    + analyticsUsageHeadMarkup(model)
    + (drill ? analyticsDrilldownMarkup(model, drill, measureId) : '')
    + (model.measures?.length ? `<section class="analytics-chart-card analytics-chart-wide analytics-usage-trends">${analyticsMeasureToolbarMarkup(model, measureId)}
      ${rollup ? `<p class="subtle">Subtotal ${esc(measureFmt(rollup.subtotal))} · ${rollup.reporting} reporting attempts (attempt-time population).</p>` : ''}
      ${usageMeasureSparklineMarkup(rollup, measureSparkOpts)}
      <div class="analytics-breakdown-grid">
        <div><h3 class="analytics-breakdown-title">By executor</h3>${bars(executorBreakdown, { fmt: measureFmt, drillKind: 'executor' })}</div>
        <div><h3 class="analytics-breakdown-title">By workflow</h3>${bars(workflowBreakdown, { fmt: measureFmt, drillKind: 'workflow' })}</div>
      </div></section>` : '')
    + `<div class="analytics-chart-grid">`
    + card('Task time by adapter', timingChart, true)
    + card(`Run outcomes · ${esc(windowLabel)}`, sparklineMarkup(buckets, sparkOpts) + legend([['Succeeded', 'succeeded'], ['Failed', 'failed'], ['Other', 'pending']]), true)
    + card('Median workflow duration', bars(workflowTime, { fmt: fmtDuration }))
    + card('Agent failure rate', bars(failureRates, { fmt: value => `${Math.round(value)}%`, maxValue: 100, colorVar: '--red' }))
    + card(`Daily tokens · ${esc(windowLabel)}`, tokenSparklineMarkup(tokenBuckets, tokenOpts) + legend([['Input', 'tok-input'], ['Output', 'tok-output'], ['Cached', 'tok-cached']]), true)
    + '</div>'
    + analyticsDetailsMarkup(model, measureId);

  if (typeof container.querySelector === 'function') {
    analyticsWireInteractiveUI(container, model, { measureId, drill });
    if (drill) container.querySelector('#analyticsDrilldown')?.focus();
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
  filters = filters || analyticsFiltersFromHash();
  try {
    const model = await loadAnalyticsData(filters, controller.signal);
    if (request !== analyticsRequest || controller.signal.aborted) return;
    model.filters = { ...filters, measureId: filters.measureId || model.defaultMeasure };
    renderAnalytics(model, { measureId: model.filters.measureId, drill: filters.drill });
  } catch (err) {
    if (request !== analyticsRequest || controller.signal.aborted) return;
    if (container) container.innerHTML = `<div class="notice error" role="alert">Unable to load analytics: ${esc(err.message)}</div>`;
  }
}
