// Retry overhead and latency distributions for analytics (generic contract only).

function analyticsPercentile(values, fraction) {
  if (!values?.length) return null;
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.max(0, Math.ceil(fraction * sorted.length) - 1)];
}

function analyticsAttemptTiming(attempt) {
  const workerMs = analyticsNumber(attempt.executionDurationMs);
  let coordinatorMs = null;
  const start = attempt.startedAt != null ? Date.parse(attempt.startedAt) : NaN;
  const finish = attempt.finishedAt != null ? Date.parse(attempt.finishedAt) : NaN;
  if (Number.isFinite(start) && start > 0 && Number.isFinite(finish) && finish >= start) {
    coordinatorMs = finish - start;
  }
  let queueMs = null;
  const queued = attempt.queuedAt != null ? Date.parse(attempt.queuedAt) : NaN;
  const dispatched = attempt.dispatchedAt != null ? Date.parse(attempt.dispatchedAt) : NaN;
  const workerStart = attempt.workerStartedAt != null ? Date.parse(attempt.workerStartedAt) : NaN;
  if (Number.isFinite(queued) && queued > 0 && Number.isFinite(workerStart) && workerStart >= queued) {
    queueMs = workerStart - queued;
  } else if (Number.isFinite(dispatched) && dispatched > 0 && Number.isFinite(workerStart) && workerStart >= dispatched) {
    queueMs = workerStart - dispatched;
  }
  return { workerMs, coordinatorMs, queueMs };
}

function analyticsTimingBucketEmpty() {
  return { worker: [], coordinator: [], queue: [] };
}

function analyticsTimingBucketPush(bucket, timing) {
  if (timing.workerMs != null) bucket.worker.push(timing.workerMs);
  if (timing.coordinatorMs != null) bucket.coordinator.push(timing.coordinatorMs);
  if (timing.queueMs != null) bucket.queue.push(timing.queueMs);
}

function analyticsTimingDistribution(samples) {
  const note = samples.length > 0 && samples.length < 5 ? `small sample (n=${samples.length})` : '';
  return {
    n: samples.length,
    median: analyticsPercentile(samples, 0.5),
    p95: samples.length >= 2 ? analyticsPercentile(samples, 0.95) : null,
    note,
  };
}

function analyticsGroupDistributions(bucket) {
  return {
    worker: analyticsTimingDistribution(bucket.worker),
    coordinator: analyticsTimingDistribution(bucket.coordinator),
    queue: analyticsTimingDistribution(bucket.queue),
  };
}

function analyticsActivationAttempts(attempts) {
  const byActivation = new Map();
  for (const a of attempts) {
    if (a.state === 'queued') continue;
    const key = a.activationId || a.id || '';
    if (!byActivation.has(key)) byActivation.set(key, []);
    byActivation.get(key).push(a);
  }
  for (const list of byActivation.values()) {
    list.sort((a, b) => (a.retryNumber ?? 0) - (b.retryNumber ?? 0) || String(a.id).localeCompare(String(b.id)));
  }
  return byActivation;
}

function analyticsAttemptRecoveredFailure(attempt, activationAttempts) {
  if (attempt.state !== 'failed') return false;
  const idx = activationAttempts.findIndex(a => a.id === attempt.id && a.retryNumber === attempt.retryNumber);
  return activationAttempts.slice(idx + 1).some(a => a.state === 'succeeded');
}

function analyticsRetryOverheadForActivation(activationAttempts) {
  const completed = activationAttempts.filter(a => !analyticsAttemptPending(a.state));
  if (completed.length <= 1) return { extraAttempts: 0, workerMs: null, coordinatorMs: null };
  const success = [...completed].reverse().find(a => a.state === 'succeeded');
  const terminal = success || completed[completed.length - 1];
  const overheadAttempts = completed.filter(a => a !== terminal);
  const extraAttempts = overheadAttempts.length;
  if (!extraAttempts) return { extraAttempts: 0, workerMs: null, coordinatorMs: null };
  let workerSum = null;
  let coordinatorSum = null;
  for (const a of overheadAttempts) {
    const t = analyticsAttemptTiming(a);
    if (t.workerMs != null) workerSum = (workerSum ?? 0) + t.workerMs;
    if (t.coordinatorMs != null) coordinatorSum = (coordinatorSum ?? 0) + t.coordinatorMs;
  }
  return { extraAttempts, workerMs: workerSum, coordinatorMs: coordinatorSum };
}

function analyticsFailedRetriedUsageRow(attempt){if(analyticsAttemptPending(attempt.state))return {kind:'pending'};const row=attempt.accountingSummary?.tokens;return {kind:row?.knownSubtotal!=null?(row.completeness==='complete'?'reporting':'partial'):'unknown'}}

function buildAnalyticsRetryLatency(flatAttempts, runs) {
  const byWorkflow = new Map();
  const byExecutor = new Map();
  const runOutcomes = {
    terminalRuns: 0,
    succeededNoRetry: 0,
    succeededAfterRetry: 0,
    finalFailed: 0,
    cancelledRuns: 0,
    recoveredFailureAttempts: 0,
    finalFailureAttempts: 0,
    cancelledAttempts: 0,
  };
  let retryOverhead = { activationsWithRetry: 0, extraAttempts: 0, workerMs: null, coordinatorMs: null };
  const failedRetriedUsage = {
    attempts: 0,
    reporting: 0,
    partial: 0,
    unknown: 0,
    tokens: null,
    costObserved: null,
    costEstimated: null,
  };
  const contributors = [];

  const attemptsByRun = new Map();
  for (const { attempt, run } of flatAttempts) {
  if (!attemptsByRun.has(run.id)) attemptsByRun.set(run.id, []);
    attemptsByRun.get(run.id).push(attempt);
  }

  for (const run of runs) {
    if (!['succeeded', 'failed', 'cancelled'].includes(run.state)) continue;
    runOutcomes.terminalRuns++;
    if (run.state === 'cancelled') {
      runOutcomes.cancelledRuns++;
      for (const attempt of (attemptsByRun.get(run.id) || []).filter(a => a.state !== 'queued')) {
        if (attempt.state === 'cancelled') runOutcomes.cancelledAttempts++;
      }
      continue;
    }
    if (run.state === 'failed') {
      runOutcomes.finalFailed++;
    }
    const attempts = (attemptsByRun.get(run.id) || []).filter(a => a.state !== 'queued');
    const activations = analyticsActivationAttempts(attempts);
    let runHadRetry = false;
    for (const activationAttempts of activations.values()) {
      const maxRetry = activationAttempts.reduce((m, a) => Math.max(m, a.retryNumber ?? 0), 0);
      const hadRecovery = activationAttempts.some(a => a.state === 'failed') && activationAttempts.some(a => a.state === 'succeeded');
      if (maxRetry > 0 || hadRecovery) runHadRetry = true;
      const overhead = analyticsRetryOverheadForActivation(activationAttempts);
      if (overhead.extraAttempts) {
        retryOverhead.activationsWithRetry++;
        retryOverhead.extraAttempts += overhead.extraAttempts;
        if (overhead.workerMs != null) retryOverhead.workerMs = (retryOverhead.workerMs ?? 0) + overhead.workerMs;
        if (overhead.coordinatorMs != null) retryOverhead.coordinatorMs = (retryOverhead.coordinatorMs ?? 0) + overhead.coordinatorMs;
      }
      for (const attempt of activationAttempts) {
        if (attempt.state === 'cancelled') runOutcomes.cancelledAttempts++;
        if (attempt.state === 'failed') {
          if (analyticsAttemptRecoveredFailure(attempt, activationAttempts)) {
            runOutcomes.recoveredFailureAttempts++;
          } else if (run.state === 'failed') {
            runOutcomes.finalFailureAttempts++;
          }
        }
        const isFailedOrRetry = attempt.state === 'failed' || (attempt.retryNumber ?? 0) > 0;
        if (isFailedOrRetry && !analyticsAttemptPending(attempt.state)) {
          failedRetriedUsage.attempts++;
          const row = analyticsFailedRetriedUsageRow(attempt);
          if (row.kind === 'reporting') {
            failedRetriedUsage.reporting++;
          } else if (row.kind === 'partial') failedRetriedUsage.partial++;
          else if (row.kind === 'unknown') failedRetriedUsage.unknown++;
        }
      }
    }
    if (run.state === 'succeeded') {
      if (runHadRetry) runOutcomes.succeededAfterRetry++;
      else runOutcomes.succeededNoRetry++;
    }
  }

  for (const { attempt, run } of flatAttempts) {
    if (analyticsAttemptPending(attempt.state)) continue;
    const timing = analyticsAttemptTiming(attempt);
    const wfKey = run.workflowName || 'Unnamed workflow';
    const exKey = attempt.executor || 'unknown';
    if (timing.workerMs != null || timing.coordinatorMs != null || timing.queueMs != null) {
      if (!byWorkflow.has(wfKey)) byWorkflow.set(wfKey, analyticsTimingBucketEmpty());
      if (!byExecutor.has(exKey)) byExecutor.set(exKey, analyticsTimingBucketEmpty());
      analyticsTimingBucketPush(byWorkflow.get(wfKey), timing);
      analyticsTimingBucketPush(byExecutor.get(exKey), timing);
    }
    if (attempt.state === 'failed' || attempt.state === 'cancelled' || timing.workerMs != null || timing.coordinatorMs != null) {
      contributors.push({
        runId: run.id,
        attemptId: attempt.id || '',
        activationId: attempt.activationId || '',
        workflow: wfKey,
        executor: exKey,
        state: attempt.state,
        retryNumber: attempt.retryNumber ?? 0,
        timing,
        slowScore: Math.max(timing.workerMs ?? 0, timing.coordinatorMs ?? 0),
      });
    }
  }

  const workflowRows = [...byWorkflow.entries()].map(([label, bucket]) => ({
    label,
    ...analyticsGroupDistributions(bucket),
  })).sort((a, b) => (b.worker.median ?? b.coordinator.median ?? 0) - (a.worker.median ?? a.coordinator.median ?? 0));

  const executorRows = [...byExecutor.entries()].map(([label, bucket]) => ({
    label,
    ...analyticsGroupDistributions(bucket),
  })).sort((a, b) => (b.worker.median ?? b.coordinator.median ?? 0) - (a.worker.median ?? a.coordinator.median ?? 0));

  const slowContributors = contributors
    .filter(c => c.slowScore > 0 || c.state === 'failed' || c.state === 'cancelled')
    .sort((a, b) => b.slowScore - a.slowScore)
    .slice(0, 50);

  return {
    runOutcomes,
    retryOverhead,
    failedRetriedUsage,
    byWorkflow: workflowRows,
    byExecutor: executorRows,
    contributors: slowContributors,
    interpretation: 'Median and p95 use completed attempts in the current filter window. Queue delay is shown only when queuedAt or dispatch timestamps are present on attempts. Coordinator elapsed from startedAt/finishedAt can include dispatch wait; it is not derived from run wall time. Failed or retried usage sums reported measurements only; missing usage is not treated as free.',
  };
}

function analyticsDistribCell(dist, fmt = fmtDuration) {
  if (!dist?.n) return renderViewTemplate("analytics-retry-latency/analytics-distrib-cell-subtle");
  const med = dist.median == null ? '—' : esc(fmt(dist.median));
  const p95 = dist.p95 == null ? '—' : esc(fmt(dist.p95));
  const note = dist.note ? renderViewTemplate("analytics-retry-latency/note-subtle", [viewValue(esc(dist.note))]) : '';
  return renderViewTemplate("analytics-retry-latency/analytics-distrib-cell-subtle-2", [viewValue(med), viewValue(p95), viewValue(dist.n), viewValue(note)]);
}

function analyticsRetryLatencyMarkup(model) {
  const rl = model.retryLatency;
  if (!rl) return '';
  const o = rl.runOutcomes;
  const bars = (items, colorVar, fmt?, drillKind = '') => items.some(item => item.value > 0)
    ? barChartMarkup(items, { colorVar, fmt, drillKind })
    : renderViewTemplate("shared/empty-chart");
  const outcomeData = [
    ['Succeeded · no retry', o.succeededNoRetry], ['Succeeded · retried', o.succeededAfterRetry],
    ['Failed', o.finalFailed], ['Cancelled', o.cancelledRuns],
  ].map(([label, value]) => ({ label, value }));
  const attemptData = [
    ['Recovered failures', o.recoveredFailureAttempts], ['Final failures', o.finalFailureAttempts],
    ['Cancelled', o.cancelledAttempts],
  ].map(([label, value]) => ({ label, value }));
  const medianRows = rows => rows.slice(0, 10).filter(row => row.worker?.median != null)
    .map(row => ({ label: row.label, value: row.worker.median }));
  const formatLatency = value => fmtDuration(value);

  const contribRows = rl.contributors.slice(0, 25).map(c => {
    const dur = c.timing.workerMs != null ? fmtDuration(c.timing.workerMs) : c.timing.coordinatorMs != null ? fmtDuration(c.timing.coordinatorMs) : 'Unavailable';
    return renderViewTemplate("analytics-retry-latency/contrib-rows-analytics-run-link", [viewValue(esc(c.workflow)), viewValue(esc(c.executor)), viewValue(encodeURIComponent(c.runId)), viewValue(esc(c.runId)), viewValue(esc(c.retryNumber)), viewValue(esc(c.state)), viewValue(esc(dur))]);
  }).join('');

  return renderViewTemplate("analytics-retry-latency/analytics-retry-latency-markup-analytics-panel", [viewValue(bars(outcomeData, '--teal')), viewValue(bars(attemptData, '--red')), viewValue(bars(medianRows(rl.byWorkflow), '--blue',formatLatency,'latencyWorkflow')), viewValue(bars(medianRows(rl.byExecutor), '--teal',formatLatency,'latencyExecutor')), viewValue(contribRows || renderViewTemplate("analytics-retry-latency/analytics-retry-latency-markup-tr"))]);
}
