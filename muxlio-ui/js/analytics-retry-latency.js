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

function analyticsAttemptRecoveredFailure(attempt, activationAttempts, runState) {
  if (attempt.state !== 'failed') return false;
  const idx = activationAttempts.findIndex(a => a.id === attempt.id && a.retryNumber === attempt.retryNumber);
  const laterSuccess = activationAttempts.slice(idx + 1).some(a => a.state === 'succeeded');
  if (laterSuccess) return true;
  return runState === 'succeeded' && idx < activationAttempts.length - 1;
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

function analyticsFailedRetriedUsageRow(attempt) {
  const pending = analyticsAttemptPending(attempt.state);
  if (pending) return { kind: 'pending' };
  const tokens = analyticsMeasureValueForAttempt(attempt, { id: 'tokens', kind: 'tokens' });
  const costObserved = analyticsMeasureValueForAttempt(attempt, { id: 'cost-observed', kind: 'cost-observed' });
  const costEstimated = analyticsMeasureValueForAttempt(attempt, { id: 'cost-estimated', kind: 'cost-estimated' });
  const any = tokens != null || costObserved != null || costEstimated != null;
  const usage = attempt.usage;
  const partial = usage && analyticsUsageAnyReported(usage) && !analyticsUsageComplete(usage);
  return {
    kind: any ? 'reporting' : partial ? 'partial' : 'unknown',
    tokens,
    costObserved,
    costEstimated,
  };
}

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
          if (analyticsAttemptRecoveredFailure(attempt, activationAttempts, run.state)) {
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
            if (row.tokens != null) failedRetriedUsage.tokens = (failedRetriedUsage.tokens ?? 0) + row.tokens;
            if (row.costObserved != null) failedRetriedUsage.costObserved = (failedRetriedUsage.costObserved ?? 0) + row.costObserved;
            if (row.costEstimated != null) failedRetriedUsage.costEstimated = (failedRetriedUsage.costEstimated ?? 0) + row.costEstimated;
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
  if (!dist?.n) return '<span class="subtle">Unavailable</span>';
  const med = dist.median == null ? '—' : esc(fmt(dist.median));
  const p95 = dist.p95 == null ? '—' : esc(fmt(dist.p95));
  const note = dist.note ? `<span class="subtle"> ${esc(dist.note)}</span>` : '';
  return `${med} / ${p95}<span class="subtle"> (n=${dist.n})</span>${note}`;
}

function analyticsRetryLatencyMarkup(model) {
  const rl = model.retryLatency;
  if (!rl) return '';
  const o = rl.runOutcomes;
  const outcomeRows = [
    ['Succeeded (no retry)', o.succeededNoRetry, o.terminalRuns],
    ['Succeeded after retry', o.succeededAfterRetry, o.terminalRuns],
    ['Final failed run', o.finalFailed, o.terminalRuns],
    ['Cancelled run', o.cancelledRuns, o.terminalRuns],
  ].map(([label, count, denom]) => {
    const pct = denom ? `${Math.round(100 * count / denom)}%` : '—';
    return `<tr><td>${esc(label)}</td><td class="num">${count}</td><td class="num">${esc(pct)}</td><td class="subtle">of ${denom} terminal runs</td></tr>`;
  }).join('');
  const attemptRows = [
    ['Failed attempt, later recovered', o.recoveredFailureAttempts],
    ['Failed attempt on final failed run', o.finalFailureAttempts],
    ['Cancelled attempt', o.cancelledAttempts],
  ].map(([label, count]) => `<tr><td>${esc(label)}</td><td class="num">${count}</td></tr>`).join('');

  const overhead = rl.retryOverhead;
  const overheadLine = overhead.extraAttempts
    ? `${overhead.extraAttempts} non-final attempts across ${overhead.activationsWithRetry} activations · worker ${overhead.workerMs != null ? fmtDuration(overhead.workerMs) : 'Unavailable'} · coordinator ${overhead.coordinatorMs != null ? fmtDuration(overhead.coordinatorMs) : 'Unavailable'}`
    : 'No retry chains in the current window.';

  const fu = rl.failedRetriedUsage;
  const usageLine = fu.attempts
    ? `${fu.attempts} failed or retried attempts · reporting ${fu.reporting}, partial ${fu.partial}, unknown ${fu.unknown} · tokens ${fu.tokens != null ? fmtTok(fu.tokens) : 'Unavailable'} · cost observed ${fu.costObserved != null ? fmtCostUsd(fu.costObserved) : 'Unavailable'} · cost estimated ${fu.costEstimated != null ? fmtCostUsd(fu.costEstimated) : 'Unavailable'}`
    : 'No completed failed or retried attempts.';

  const distribHead = `<thead><tr><th scope="col">Group</th><th scope="col">Worker median / p95</th><th scope="col">Coordinator median / p95</th><th scope="col">Queue median / p95</th><th scope="col">Drill</th></tr></thead>`;
  const distribRow = (row, kind) => `<tr>
    <th scope="row">${esc(row.label)}</th>
    <td class="num">${analyticsDistribCell(row.worker)}</td>
    <td class="num">${analyticsDistribCell(row.coordinator)}</td>
    <td class="num">${analyticsDistribCell(row.queue)}</td>
    <td><button type="button" class="btn ghost" data-analytics-drill="${esc(`${kind}:${row.label}`)}">Attempts</button></td>
  </tr>`;

  const wfRows = rl.byWorkflow.slice(0, 15).map(row => distribRow(row, 'latencyWorkflow')).join('') || '<tr><td colspan="5" class="subtle">No timing samples</td></tr>';
  const exRows = rl.byExecutor.slice(0, 15).map(row => distribRow(row, 'latencyExecutor')).join('') || '<tr><td colspan="5" class="subtle">No timing samples</td></tr>';

  const contribRows = rl.contributors.slice(0, 25).map(c => {
    const dur = c.timing.workerMs != null ? fmtDuration(c.timing.workerMs) : c.timing.coordinatorMs != null ? fmtDuration(c.timing.coordinatorMs) : 'Unavailable';
    return `<tr>
      <td>${esc(c.workflow)}</td>
      <td>${esc(c.executor)}</td>
      <td><a class="analytics-run-link" href="#run/${encodeURIComponent(c.runId)}">${esc(c.runId)}</a></td>
      <td class="subtle">retry ${esc(c.retryNumber)} · ${esc(c.state)}</td>
      <td class="num">${esc(dur)}</td>
    </tr>`;
  }).join('');

  return `<section class="analytics-panel analytics-retry-latency analytics-chart-wide" aria-labelledby="analyticsRetryLatencyTitle">
    <h2 id="analyticsRetryLatencyTitle">Failure, retry, and latency</h2>
    <p class="subtle">${esc(rl.interpretation)}</p>
    <div class="analytics-retry-grid">
      <div>
        <h3 class="analytics-breakdown-title">Run outcomes</h3>
        <div class="tablebox"><table><thead><tr><th scope="col">Outcome</th><th scope="col">Count</th><th scope="col">Share</th><th scope="col">Denominator</th></tr></thead><tbody>${outcomeRows}</tbody></table></div>
      </div>
      <div>
        <h3 class="analytics-breakdown-title">Attempt outcomes</h3>
        <div class="tablebox"><table><thead><tr><th scope="col">Outcome</th><th scope="col">Count</th></tr></thead><tbody>${attemptRows}</tbody></table></div>
      </div>
    </div>
    <p class="subtle">Retry overhead. ${esc(overheadLine)}</p>
    <p class="subtle">Failed and retried usage. ${esc(usageLine)} Coverage reflects executor-reported values only; retries may still contribute useful work.</p>
    <h3 class="analytics-breakdown-title">Latency by workflow</h3>
    <div class="tablebox"><table>${distribHead}<tbody>${wfRows}</tbody></table></div>
    <h3 class="analytics-breakdown-title">Latency by executor</h3>
    <div class="tablebox"><table>${distribHead}<tbody>${exRows}</tbody></table></div>
    <details class="analytics-details"><summary>Slow or failing attempts (sorted)</summary>
      <div class="tablebox"><table><thead><tr><th scope="col">Workflow</th><th scope="col">Executor</th><th scope="col">Run</th><th scope="col">Attempt</th><th scope="col">Duration</th></tr></thead><tbody>${contribRows || '<tr><td colspan="5">None</td></tr>'}</tbody></table></div>
    </details>
  </section>`;
}
