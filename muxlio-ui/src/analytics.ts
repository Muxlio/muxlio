/* ── analytics ─────────────────────────────────── */
// Cross-run operational insights: run volume, outcomes, timing, and
// canonical accounting totals supplied by the coordinator.

async function loadAnalyticsApiPage(filters, range, signal) {
  const runs = [];
  const attemptsByRun = new Map();
  const data = await api(`/v1/analytics?${analyticsFiltersQueryString(filters, range)}`, { signal });
  if (!Array.isArray(data.runs)) throw new Error('Invalid analytics runs response');
  runs.push(...data.runs);
  for (const run of runs) {
    const list = data.attemptsByRun?.[run.id];
    const attempts = Array.isArray(list) ? list : [];
    for (const attempt of attempts) attempt.accountingSummary = data.accountingByAttempt?.[attempt.id];
    attemptsByRun.set(run.id, attempts);
  }
  runs.sort((a, b) => new Date(a.createdAt).getTime() - new Date(b.createdAt).getTime());
  return {
    accountingData: data,
    runs,
    attemptsByRun,
    totalRuns: data.totalRuns ?? runs.length,
    populations: data.populations,
    offset: data.offset ?? Math.max(0, ((filters.page || 1) - 1) * 200),
    hasMore: !!data.hasMore,
  };
}

async function loadAnalyticsData(filters = analyticsFiltersFromURL(), signal) {
  const range = analyticsResolveRange(filters);
  const attemptErrors = new Map();
  const snapshot=await loadAnalyticsApiPage(filters,range,signal);
  const { runs, attemptsByRun, totalRuns, populations, offset, hasMore } = snapshot;
  const model = buildAnalyticsModel(runs, attemptsByRun, attemptErrors, {
    accountingData:snapshot.accountingData,
    range,
    filters: { ...filters },
    totalRuns,
    hasMore,
    populations,
    offset,
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

function buildAnalyticsModel(runs, attemptsByRun, attemptErrors = new Map(), ctx: Record<string, any> = {}) {
  const filters = ctx.filters || analyticsDefaultFilters();
  const range = ctx.range || analyticsResolveRange(filters);
  const tz = filters.tz || 'UTC';
  const stateCounts = analyticsStateCounts(runs);

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

  const tokenBuckets=[];
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

      const tokenRow=a.accountingSummary?.tokens;
      const usage=tokenRow?.knownSubtotal!=null;
      const bucketTime=analyticsAttemptBucketTime(a,r);if(bucketTime)bucketProvenance.finishedAt++;
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

      if(!usage){stats.missingUsage++;incompleteUsage=true;continue}
      stats.usageSamples++;if(tokenRow.completeness!=='complete'){stats.missingUsage++;incompleteUsage=true}

    }
    if (hasCompletedAttempt && incompleteUsage) runsWithUnavailableUsage++;
  }

  const attemptRows = flatAttempts.map(({ attempt }) => attempt);
  const usageSummary=ctx.accountingData?.accounting||{};
  const measures=analyticsMeasureCatalog(usageSummary);
  const measureRollups=buildAnalyticsMeasureRollups(flatAttempts,measures,analyticsDayBuckets(range,'UTC'),range,'UTC',ctx.accountingData||{});
  const retryLatency=buildAnalyticsRetryLatency(flatAttempts,runs);

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
    totals: null,
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
    offset: ctx.offset ?? 0,
  };
}

function buildAnalyticsMeasureRollups(flatAttempts,measures,dayTemplate,range,tz='UTC',data: Record<string, any>={}){
 const rollups=new Map();
 for(const measure of measures){
  const row=analyticsMeasureSummary(data.accounting,measure),subtotal=analyticsMeasureExact(data.accounting,measure);
  const buckets=dayTemplate.map(b=>{const summary=data.accountingByDay?.[b.dateKey],exactTotal=analyticsMeasureExact(summary,measure);return {...b,total:analyticsChartValue(exactTotal),exactTotal,reporting:analyticsReporting(analyticsMeasureSummary(summary,measure)),hasData:exactTotal!=null}});
  const groups=(source)=>Object.entries(source||{}).map(([label,summary])=>{const exactValue=analyticsMeasureExact(summary,measure);return {label,value:analyticsChartValue(exactValue),exactValue,runIds:new Set()}}).filter(x=>x.exactValue!=null).sort((a,b)=>b.value-a.value);
  const evidence=flatAttempts.map(({attempt,run})=>{const exactValue=analyticsMeasureExact(attempt.accountingSummary,measure);return {runId:run.id,attemptId:attempt.id||'',executor:attempt.executor||'unknown',workflow:run.workflowName||'Unnamed workflow',value:analyticsChartValue(exactValue),exactValue,bucketProvenance:attempt.finishedAt?'finishedAt':null}});
  rollups.set(measure.id,{measure,buckets,byExecutor:groups(data.accountingByExecutor),byWorkflow:groups(data.accountingByWorkflow),evidence,subtotal,reporting:analyticsReporting(row),precisionWarning:analyticsPrecisionWarning(subtotal),completeness:row?.completeness||'unknown'});
 }
 return rollups;
}

function analyticsNumber(value) {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null;
}

function barChartMarkup(items, opts: Record<string, any> = {}) {
  if (!items.length) return renderViewTemplate("analytics/bar-chart-markup-p");
  const maxVal = opts.maxValue ?? Math.max(...items.map(it => it.value), 1);
  const colorVar = opts.colorVar || '--teal';
  const drillKind = opts.drillKind || '';
  return renderViewTemplate("analytics/bar-chart-markup-analytics-bar-chart", [viewValue(items.map(it => {
    const pct = maxVal > 0 ? Math.max(0, Math.min(100, (it.value / maxVal) * 100)) : 0;
    const label = it.label;
    const val = opts.fmt ? opts.fmt(it.exactValue??it.value) : String(it.value);
    const drillKey = it.drillKey || it.label;
    const drill = drillKind ? renderViewTemplate("analytics/drill-fragment", [viewValue(esc(`${drillKind}:${drillKey}`))]) : '';
    const tag = drillKind ? 'button' : 'div';
    const btnType = drillKind ? renderViewTemplate("analytics/btn-type-fragment") : '';
    return renderViewTemplate("analytics/bar-chart-markup-abc-row", [viewValue(tag), viewValue(drillKind ? ' abc-drill' : ''), viewValue(btnType), viewValue(drill), viewValue(esc(label)), viewValue(esc(label)), viewValue(pct.toFixed(1)), viewValue(esc(colorVar)), viewValue(esc(val)), viewValue(tag)]);
  }).join(''))]);
}

function sparklineMarkup(buckets, opts: Record<string, any> = {}) {
  const maxTotal = Math.max(...buckets.map(b => b.total), 1);
  const hasAny = buckets.some(b => b.total > 0);
  const emptyMsg = opts.emptyLabel || 'No runs in the selected window.';
  if (!hasAny) return renderViewTemplate("analytics/sparkline-markup-p", [viewValue(esc(emptyMsg))]);

  const drillKind = opts.drillKind || '';
  const bars = buckets.map(b => {
    const totalH = maxTotal > 0 ? (b.total / maxTotal) * 56 : 0;
    const succH = maxTotal > 0 ? (b.succeeded / maxTotal) * 56 : 0;
    const failH = maxTotal > 0 ? (b.failed / maxTotal) * 56 : 0;
    const pendH = Math.max(0, totalH - succH - failH);
    const tip = `${b.label}: ${b.total} runs (${b.succeeded} succeeded, ${b.failed} failed)`;
    const drill = drillKind ? renderViewTemplate("analytics/drill-fragment", [viewValue(esc(`${drillKind}:${b.dateKey}`))]) : '';
  const tag = drillKind ? 'button' : 'div';
  const btnType = drillKind ? renderViewTemplate("analytics/btn-type-fragment") : '';
    return renderViewTemplate("analytics/bars-spark-col", [viewValue(tag), viewValue(drillKind ? ' spark-drill' : ''), viewValue(btnType), viewValue(drill), viewValue(esc(tip)), viewValue(esc(tip)), viewValue(Math.max(1, Math.round(totalH))), viewValue(failH > 0 ? renderViewTemplate("analytics/bars-spark-seg", [viewValue(Math.round(failH))]) : ''), viewValue(succH > 0 ? renderViewTemplate("analytics/bars-spark-seg-2", [viewValue(Math.round(succH))]) : ''), viewValue(pendH > 0 ? renderViewTemplate("analytics/bars-spark-seg-3", [viewValue(Math.round(pendH))]) : ''), viewValue(tag)]);
  }).join('');

  const labels = buckets
    .filter((_, i) => i === 0 || i === Math.floor(buckets.length / 2) || i === buckets.length - 1)
    .map(b => renderViewTemplate("shared/inline-text", [viewValue(esc(b.label))])).join('');

  return renderViewTemplate("analytics/sparkline-markup-sparkline", [viewValue(esc(opts.ariaLabel || 'Run volume')), viewValue(bars), viewValue(labels)]);
}

function usageMeasureSparklineMarkup(rollup, opts: Record<string, any> = {}) {
  if (!rollup) return renderViewTemplate("shared/empty-chart");
  const buckets = rollup.buckets;
  const maxTotal = Math.max(...buckets.map(b => b.total ?? 0), 1);
  const hasAny = buckets.some(b => b.hasData);
  if (!hasAny) return renderViewTemplate("shared/empty-chart");
  const fmt = v => analyticsFormatMeasureValue(rollup.measure, v);
  const bars = buckets.map(b => {
    const total = b.total ?? 0;
    const totalH = maxTotal > 0 ? (total / maxTotal) * 56 : 0;
    const tip = `${b.label}: ${fmt(b.exactTotal??total)} · ${b.reporting} attempts`;
    const drill = opts.drillKind ? renderViewTemplate("analytics/drill-fragment", [viewValue(esc(`${opts.drillKind}:${b.dateKey}`))]) : '';
    const tag = opts.drillKind ? 'button' : 'div';
    const btnType = opts.drillKind ? renderViewTemplate("analytics/btn-type-fragment") : '';
    return renderViewTemplate("analytics/bars-spark-col-2", [viewValue(tag), viewValue(opts.drillKind ? ' spark-drill' : ''), viewValue(btnType), viewValue(drill), viewValue(esc(tip)), viewValue(esc(tip)), viewValue(Math.max(1, Math.round(totalH))), viewValue(total > 0 ? renderViewTemplate("analytics/bars-spark-seg-4", [viewValue(Math.round(totalH))]) : ''), viewValue(tag)]);
  }).join('');
  const labels = buckets
    .filter((_, i) => i === 0 || i === Math.floor(buckets.length / 2) || i === buckets.length - 1)
    .map(b => renderViewTemplate("shared/inline-text", [viewValue(esc(b.label))])).join('');
  return renderViewTemplate("analytics/usage-measure-sparkline-markup-sparkline", [viewValue(esc(rollup.measure.label)), viewValue(bars), viewValue(labels)]);
}

function analyticsAttemptLink(runId, attempt) {
  const act = attempt?.activationId;
  const label = attempt?.nodeId || (act ? act.split(':').at(-1) : 'Attempt');
  if (act) {
    return renderViewTemplate("analytics/analytics-attempt-link-analytics-run-link", [viewValue(encodeURIComponent(runId)), viewValue(encodeURIComponent(act)), viewValue(esc(label))]);
  }
  return renderViewTemplate("analytics/analytics-attempt-link-analytics-run-link-2", [viewValue(encodeURIComponent(runId)), viewValue(esc(label))]);
}

function analyticsFilterBarMarkup(model) {
  const f = model.filters || analyticsDefaultFilters();
  const range = model.range || analyticsResolveRange(f);
  const labels = analyticsActiveFilterLabels(f, range);
  const stateOptions = ANALYTICS_RUN_STATES.map(st =>
    renderViewTemplate("analytics/state-options-option", [viewValue(esc(st)), viewValue(f.states?.includes(st) ? ' selected' : ''), viewValue(esc(st))])
  ).join('');
  const rangeOptions = ['24h', '7d', '30d', '90d', 'custom'].map(p =>
    renderViewTemplate("analytics/state-options-option", [viewValue(esc(p)), viewValue(f.preset === p ? ' selected' : ''), viewValue(esc({'24h':'Last 24 hours','7d':'Last 7 days','30d':'Last 30 days','90d':'Last 90 days',custom:'Custom range'}[p]))])
  ).join('');
  const chips = labels.map(l => renderViewTemplate("analytics/chips-analytics-filter-chip", [viewValue(esc(l.text))])).join('');
  const compare = model.comparison;
  const compareNote = compare && compare.priorRunCount > 0
    ? renderPlaceholder(`${compare.priorLabel}: ${compare.priorRunCount} runs vs ${compare.currentRunCount} in current window (identical filters).`, "subtle analytics-compare")
    : '';
  const total = model.totalRuns ?? model.runs.length;
  const loaded = model.runs.length;
  const offset = model.offset || 0;
  const scopeNote = `Runs ${loaded ? offset + 1 : 0}–${offset + loaded} of ${total} · charts and accounting show this page`;
  const previous = (f.page || 1) > 1 ? renderViewTemplate("analytics/previous-btn", [viewValue(f.page - 1)]) : '';
  const next = model.hasMore ? renderViewTemplate("analytics/next-btn", [viewValue((f.page || 1) + 1)]) : '';
  const paging = renderViewTemplate("analytics/paging-analytics-pager", [viewValue(esc(scopeNote)), viewValue(previous), viewValue(next)]);
  return renderViewTemplate("analytics/analytics-filter-bar-markup-analytics-panel", [viewValue(rangeOptions), viewValue(f.preset === 'custom' ? '' : ' hidden'), viewValue(esc(analyticsDateTimeLocalValue(f.from))), viewValue(f.preset === 'custom' ? '' : ' hidden'), viewValue(esc(analyticsDateTimeLocalValue(f.to))), viewValue(stateOptions), viewValue(esc(f.executor)), viewValue(f.tz === 'UTC' ? ' selected' : ''), viewValue(esc(Intl.DateTimeFormat().resolvedOptions().timeZone)), viewValue(f.tz !== 'UTC' ? ' selected' : ''), viewValue(esc(f.workflow)), viewValue(chips), viewValue(compareNote), viewValue(paging)]);
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
    return model.flatAttempts.filter(({ attempt, run }) => {
      if ((run.workflowName || 'Unnamed workflow') !== key) return false;
      if (analyticsAttemptPending(attempt.state)) return false;
      return analyticsAttemptTiming(attempt).workerMs != null;
    }).map(({ attempt, run }) => ({ kind: 'attempt', run, attempt }));
  }
  if (kind === 'latencyExecutor') {
    return model.flatAttempts.filter(({ attempt }) => {
      if ((attempt.executor || 'unknown') !== key) return false;
      if (analyticsAttemptPending(attempt.state)) return false;
      return analyticsAttemptTiming(attempt).workerMs != null;
    }).map(({ attempt, run }) => ({ kind: 'attempt', run, attempt }));
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
      if(match?.value!=null)rec={...rec,value:match.exactValue??match.value};
    }
    if (rec.kind === 'run') {
      return renderViewTemplate("analytics/rows-analytics-run-link", [viewValue(esc(rec.run.workflowName || 'Unnamed workflow')), viewValue(encodeURIComponent(rec.run.id)), viewValue(esc(rec.run.state))]);
    }
    return renderViewTemplate("analytics/rows-analytics-run-link-2", [viewValue(esc(rec.run.workflowName || 'Unnamed workflow')), viewValue(encodeURIComponent(rec.run.id)), viewValue(analyticsAttemptLink(rec.run.id, rec.attempt)), viewValue(esc(rec.attempt.executor || 'unknown')), viewValue(esc(rec.attempt.state)), viewValue(measure && rec.value != null ? esc(analyticsFormatMeasureValue(measure, rec.value)) : '—')]);
  }).join('');
  return renderViewTemplate("analytics/analytics-drilldown-markup-analytics-panel", [viewValue(esc(title)), viewValue(records.length), viewValue(records.length===1?'record':'records'), viewValue(records.length > 100 ? ' (first 100 shown)' : ''), viewValue(esc(measure?.label||'Usage')), viewValue(rows || renderViewTemplate("analytics/analytics-drilldown-markup-tr"))]);
}

function analyticsWireInteractiveUI(container, model, options: Record<string, any> = {}) {
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
    return renderViewTemplate("analytics/analytics-measure-toolbar-markup-analytics-panel");
  }
  const id = selectedId || model.defaultMeasure;
  const options = model.measures.map(m =>
    renderViewTemplate("analytics/state-options-option", [viewValue(esc(m.id)), viewValue(m.id === id ? ' selected' : ''), viewValue(esc(m.label))])
  ).join('');
  return renderViewTemplate("analytics/analytics-measure-toolbar-markup-analytics-toolbar", [viewValue(options)]);
}

function analyticsDetailsMarkup(model, measureId) {
  const rows = [...model.executorStats].map(([label, stats]) => ({ label, value: stats.attempts })).sort((a,b)=>b.value-a.value).slice(0,10);
  const durationRows = [...model.executorStats].filter(([,s])=>s.durationSamples).map(([label,s])=>({label,value:s.durationSumMs/s.durationSamples})).sort((a,b)=>b.value-a.value).slice(0,10);
  const duration = value => fmtDuration(value);
  const card = (title, items, opts) => renderViewTemplate("analytics/card-analytics-chart-card", [viewValue(title), viewValue(items.length?barChartMarkup(items,opts):renderViewTemplate("shared/empty-chart"))]);
  const usageRows = measureId && model.measureRollups?.get(measureId)?.byExecutor?.slice(0,10).map(row=>({label:row.label,value:row.value,exactValue:row.exactValue})) || [];
  const usageFmt = value => analyticsFormatMeasureValue(model.measureRollups.get(measureId).measure,value);
  return renderViewTemplate("analytics/analytics-details-markup-analytics-chart-grid", [viewValue(card('Attempts by executor',rows,{drillKind:'executor'})), viewValue(card('Average worker time',durationRows,{drillKind:'executor',fmt:duration,colorVar:'--blue'})), viewValue(card('Usage by executor',usageRows,{drillKind:'executor',fmt:usageFmt,colorVar:'--amber'}))]);
}

function renderAnalytics(model, options: Record<string, any> = {}) {
  const { buckets, range, filters } = model;
  const container = (document.getElementById('analyticsContent') as UIElement);
  if (!container) return;
  container._analyticsModel = model;

  const measureId = options.measureId || filters?.measureId || model.defaultMeasure;
  const drill = options.drill ?? filters?.drill ?? '';
  const preset = filters?.preset || '30d';
  const windowLabel = preset === '30d' ? '30 days' : (range?.label || 'selected window');
  const emptyRunLabel = preset === '30d' ? 'No runs in the last 30 days.' : 'No runs in the selected window.';
  const rollup = measureId ? model.measureRollups?.get(measureId) : null;
  const measureFmt = v => rollup ? analyticsFormatMeasureValue(rollup.measure, v) : String(v);

  const empty = renderViewTemplate("shared/empty-chart");
  const bars = (items, opts) => items.length ? barChartMarkup(items, opts) : empty;
  const card = (title, chart, wide = false) => renderViewTemplate("analytics/card-analytics-chart-card-2", [viewValue(wide ? ' analytics-chart-wide' : ''), viewValue(title), viewValue(chart)]);
  const legend = items => renderViewTemplate("analytics/legend-spark-legend", [viewValue(items.map(([label, cls]) => renderViewTemplate("analytics/legend-spark-legend-item", [viewValue(cls), viewValue(label)])).join(''))]);

  const executorBreakdown = rollup ? rollup.byExecutor.slice(0, 10) : [];
  const workflowBreakdown = rollup ? rollup.byWorkflow.slice(0, 10) : [];

  const warning = model.attemptErrors?.size ? renderViewTemplate("analytics/warning-analytics-data-status", [viewValue(model.attemptErrors.size), viewValue([...model.attemptErrors].map(([id,error])=>renderViewTemplate("analytics/warning-li", [viewValue(encodeURIComponent(id)), viewValue(esc(id)), viewValue(esc(error))])).join(''))]) : '';
  const sparkOpts = { drillKind: 'runVolume', ariaLabel: `Run volume · ${windowLabel}`, emptyLabel: emptyRunLabel };
  const measureSparkOpts = { drillKind: 'measureVolume', ariaLabel: `${rollup?.measure?.label || 'Usage'} · ${windowLabel}` };

  container.innerHTML = analyticsFilterBarMarkup(model)
    + warning
    + (drill ? analyticsDrilldownMarkup(model, drill, measureId) : '')
    + (model.measures?.length ? renderViewTemplate("analytics/container-inner-html-analytics-chart-card", [viewValue(analyticsMeasureToolbarMarkup(model, measureId)), viewValue(rollup?.precisionWarning?renderPlaceholder("Chart heights use approximate numbers; labels retain exact decimal values.", "subtle"):''), viewValue(usageMeasureSparklineMarkup(rollup, measureSparkOpts)), viewValue(bars(executorBreakdown, { fmt: measureFmt, drillKind: 'executor' })), viewValue(bars(workflowBreakdown, { fmt: measureFmt, drillKind: 'workflow' }))]) : '')
    + renderViewTemplate("analytics/container-inner-html-analytics-chart-grid")
    + card(`Run outcomes · ${esc(windowLabel)}`, sparklineMarkup(buckets, sparkOpts) + legend([['Succeeded', 'succeeded'], ['Failed', 'failed'], ['Other', 'pending']]))
    + renderViewTemplate("shared/closing-div")
    + analyticsDetailsMarkup(model, measureId)
    + analyticsRetryLatencyMarkup(model);

  if (typeof container.querySelector === 'function') {
    analyticsWireInteractiveUI(container, model, { measureId, drill });
    if (drill) {
      const panel=container.querySelector<UIElement>('#analyticsDrilldown');
      if(panel?.showModal){panel.showModal();panel.addEventListener('cancel',event=>{event.preventDefault();container.querySelector<UIElement>('#analyticsCloseDrill')?.click()});}
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
  const container = (document.getElementById('analyticsContent') as UIElement);
  if (container) container.innerHTML = renderViewTemplate("analytics/container-inner-html-loading");
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
    if (container) container.innerHTML = renderNotice(`Unable to load analytics: ${err.message??''}`, {error:true});
  }
}
