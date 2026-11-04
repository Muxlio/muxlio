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
      hasCompletedAttempt = true;
      // Keep worker measurements and coordinator elapsed samples separate.
      if (analyticsNumber(a.executionDurationMs) != null) {
        stats.durationSumMs += a.executionDurationMs;
        stats.durationSamples++;
      } else {
        const start = Date.parse(a.startedAt), finish = Date.parse(a.finishedAt);
        if (Number.isFinite(start) && Number.isFinite(finish) && finish >= start) {
          stats.elapsedSumMs += finish - start;
          stats.elapsedSamples++;
        } else {
          stats.missingTiming++;
        }
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

// Format token count with K/M suffix.
function fmtTok(n) {
  if (n == null) return 'Unavailable';
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}K`;
  return String(n || 0);
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

// Format a duration in milliseconds as a human-readable string.
// Returns '—' for null/undefined, '0 ms' for zero.
function fmtDuration(ms) {
  if (ms == null) return '—';
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60000) return `${(ms / 1000).toFixed(1)} s`;
  return `${Math.floor(ms / 60000)}m ${Math.floor(ms % 60000 / 1000)}s`;
}

// Render the full analytics page from the model.
function renderAnalytics(model) {
  const { runs, stateCounts, buckets, tokenBuckets, executorStats, executorUsage, runsWithNoAttempts, runsWithUnavailableUsage, totals } = model;

  const totalRuns = runs.length;
  const terminal = (stateCounts.succeeded || 0) + (stateCounts.failed || 0) + (stateCounts.cancelled || 0);
  const successRate = terminal > 0
    ? `${Math.round((stateCounts.succeeded / terminal) * 100)}%`
    : '—';

  // ── header summary row ─────────────────────────
  const summaryHtml = `<div class="summary-grid" style="margin-bottom:26px">
    <div class="summary"><div class="summary-label">Total runs</div><div class="summary-value">${esc(String(totalRuns))}</div></div>
    <div class="summary"><div class="summary-label">Succeeded</div><div class="summary-value green">${esc(String(stateCounts.succeeded || 0))}</div></div>
    <div class="summary"><div class="summary-label">Failed</div><div class="summary-value${(stateCounts.failed || 0) > 0 ? ' red' : ''}">${esc(String(stateCounts.failed || 0))}</div></div>
    <div class="summary"><div class="summary-label">Cancelled</div><div class="summary-value">${esc(String(stateCounts.cancelled || 0))}</div></div>
    <div class="summary"><div class="summary-label">Active now</div><div class="summary-value${(stateCounts.running || 0) > 0 ? ' amber' : ''}">${esc(String((stateCounts.running || 0) + (stateCounts.waiting || 0) + (stateCounts.paused || 0) + (stateCounts.created || 0)))}</div></div>
    <div class="summary"><div class="summary-label">Success rate</div><div class="summary-value${successRate !== '—' && stateCounts.failed > 0 ? '' : ' green'}">${esc(successRate)}</div></div>
  </div>`;

  // ── run volume chart ───────────────────────────
  const volumeSection = `<div class="section-head"><div><h2>Run volume · last 30 days</h2><p>Daily run counts in UTC, coloured by outcome.</p></div></div>
  <div class="analytics-panel">${sparklineMarkup(buckets)}</div>`;

  // ── state breakdown bar chart ──────────────────
  const stateItems = [
    { label: 'Succeeded', value: stateCounts.succeeded || 0, colorVar: '--green' },
    { label: 'Failed',    value: stateCounts.failed    || 0, colorVar: '--red'   },
    { label: 'Cancelled', value: stateCounts.cancelled || 0, colorVar: '--muted' },
    { label: 'Created / queued', value: stateCounts.created || 0, colorVar: '--amber' },
    { label: 'Running',   value: stateCounts.running   || 0, colorVar: '--amber' },
    { label: 'Waiting',   value: stateCounts.waiting   || 0, colorVar: '--amber' },
    { label: 'Paused',    value: stateCounts.paused    || 0, colorVar: '--amber' },
  ].filter(it => it.value > 0);
  const maxStateCount = Math.max(...stateItems.map(it => it.value), 1);
  const stateBreakdown = stateItems.length
    ? `<div class="analytics-bar-chart" role="list">${stateItems.map(it => {
        const pct = (it.value / maxStateCount) * 100;
        return `<div class="abc-row" role="listitem">
          <div class="abc-label">${esc(it.label)}</div>
          <div class="abc-track">
            <div class="abc-bar" style="width:${pct.toFixed(1)}%;background:var(${esc(it.colorVar)})"></div>
          </div>
          <div class="abc-val">${esc(String(it.value))}</div>
        </div>`;
      }).join('')}</div>`
    : '<p style="color:var(--muted);font-size:12px">No runs recorded yet.</p>';

  const outcomesSection = `<div class="section-head" style="margin-top:28px"><div><h2>Outcomes</h2><p>All-time run counts by state.</p></div></div>
  <div class="analytics-panel">${stateBreakdown}</div>`;

  // ── executor activity: attempts and timing ─────
  let executorActivitySection = '';
  const execStatsRows = [...(executorStats || new Map()).entries()]
    .sort((a, b) => b[1].attempts - a[1].attempts);

  if (execStatsRows.length > 0) {
    const execRows = execStatsRows.map(([exec, s]) => {
      const runCount = s.runIds.size;
      const runLinks = [...s.runIds].slice(0, 5).map(rid => {
        const r = runs.find(x => x.id === rid);
        const label = r ? (r.workflowName || rid.slice(0, 12)) : rid.slice(0, 12);
        return `<a href="#run/${encodeURIComponent(rid)}" class="analytics-run-link" title="${esc(rid)}">${esc(label)}</a>`;
      }).join(', ');
      const moreRuns = runCount > 5 ? ` <span style="color:var(--muted)">+${runCount - 5} more</span>` : '';

      const workerAverage = s.durationSamples ? fmtDuration(Math.round(s.durationSumMs / s.durationSamples)) : 'Unavailable';
      const elapsedAverage = s.elapsedSamples ? fmtDuration(Math.round(s.elapsedSumMs / s.elapsedSamples)) : 'Unavailable';
      const timingNote = `${s.missingTiming} missing timing; ${s.running} in progress`;

      return `<tr>
        <td class="primary-cell"><strong>${esc(exec)}</strong></td>
        <td class="num">${esc(String(s.attempts))}</td>
        <td class="num${s.succeeded > 0 ? ' green' : ''}">${esc(String(s.succeeded))}</td>
        <td class="num${s.failed > 0 ? ' red' : ''}">${esc(String(s.failed))}</td>
        <td class="num">${esc(workerAverage)} (n=${s.durationSamples})</td>
        <td class="num">${esc(elapsedAverage)} (n=${s.elapsedSamples})</td>
        <td style="font-size:11px;color:var(--muted)">${esc(timingNote)}; ${s.usageSamples} usage records; ${s.missingUsage} missing/incomplete usage</td>
        <td style="font-size:11px">${runLinks}${moreRuns}</td>
      </tr>`;
    }).join('');

    executorActivitySection = `<div class="section-head" style="margin-top:28px"><div><h2>Executor activity</h2><p>Attempt counts and average worker execution time per executor. Executors appear dynamically as their attempts are recorded.</p></div></div>
    <div class="analytics-panel">
      <div class="tablebox">
        <table>
          <thead><tr><th>Executor</th><th>Attempts</th><th>Succeeded</th><th>Failed</th><th>Avg worker execution</th><th>Avg elapsed fallback</th><th>Missing samples</th><th>Contributing runs</th></tr></thead>
          <tbody>${execRows}</tbody>
        </table>
      </div>
      <p style="font-size:11px;color:var(--muted);margin-top:6px">Worker execution excludes coordinator queue and dispatch overhead. Elapsed fallback includes those delays and uses only completed attempts without worker measurements. The averages use separate samples.</p>
    </div>`;
  } else if (runs.length > 0) {
    executorActivitySection = `<div class="section-head" style="margin-top:28px"><div><h2>Executor activity</h2><p>Attempt counts and average worker execution time per executor.</p></div></div>
    <div class="analytics-panel">
      <p style="color:var(--muted);font-size:13px">${
        runsWithNoAttempts === runs.length
          ? 'No execution attempt records found. Executor activity will appear once runs reach an executor.'
          : 'Execution attempt data is unavailable.'
      }</p>
    </div>`;
  }

  // ── token usage by executor ────────────────────
  const inputOutputTotal = usage => usage.input == null || usage.output == null ? null : usage.input + usage.output;
  const executorRows = [...executorUsage.entries()]
    .sort((a, b) => (inputOutputTotal(b[1]) ?? -1) - (inputOutputTotal(a[1]) ?? -1));

  let tokenByExecutorSection = '';
  if (executorRows.length > 0) {
    const maxTokens = Math.max(...executorRows.map(([, e]) => inputOutputTotal(e) ?? 0), 1);
    const rows = executorRows.map(([exec, e]) => {
      const tokenTotal = inputOutputTotal(e);
      const pct = tokenTotal == null ? null : (tokenTotal / maxTokens) * 100;
      const runCount = e.runIds.size;
      // Build a compact list of up to 5 run links with the executor.
      const runLinks = [...e.runIds].slice(0, 5).map(rid => {
        const r = runs.find(x => x.id === rid);
        const label = r ? (r.workflowName || rid.slice(0, 12)) : rid.slice(0, 12);
        return `<a href="#run/${encodeURIComponent(rid)}" class="analytics-run-link" title="${esc(rid)}">${esc(label)}</a>`;
      }).join(', ');
      const moreRuns = runCount > 5 ? ` <span style="color:var(--muted)">+${runCount - 5} more</span>` : '';

      return `<tr>
        <td class="primary-cell"><strong>${esc(exec)}</strong></td>
        <td class="num">${esc(fmtTok(e.input))}</td>
        <td class="num">${esc(fmtTok(e.output))}</td>
        <td class="num">${esc(fmtTok(e.cached))}</td>
        <td class="num">${esc(fmtTok(tokenTotal))}</td>
        <td>${pct == null ? 'Unavailable' : `<div class="abc-track" style="width:120px;display:inline-flex"><div class="abc-bar" style="width:${pct.toFixed(1)}%;background:var(--teal)"></div></div>`}</td>
        <td style="font-size:11px">${runLinks}${moreRuns}</td>
      </tr>`;
    }).join('');

    const incompleteNote = (runsWithUnavailableUsage > 0 || runsWithNoAttempts > 0)
      ? `<p style="font-size:11px;color:var(--muted);margin-top:8px">` +
        (runsWithNoAttempts > 0 ? `${runsWithNoAttempts} run${runsWithNoAttempts !== 1 ? 's' : ''} have no attempt records and are excluded. ` : '') +
        (runsWithUnavailableUsage > 0 ? `${runsWithUnavailableUsage} run${runsWithUnavailableUsage !== 1 ? 's' : ''} have incomplete token data (provider did not report usage).` : '') +
        `</p>`
      : '';

    const totalRow = totals
      ? `<tfoot><tr style="border-top:1px solid var(--border);font-weight:650">
          <td>Total</td>
          <td class="num">${esc(fmtTok(totals.input))}</td>
          <td class="num">${esc(fmtTok(totals.output))}</td>
          <td class="num">${esc(fmtTok(totals.cached))}</td>
          <td class="num">${esc(fmtTok(totals.input == null || totals.output == null ? null : totals.input + totals.output))}</td>
          <td colspan="2"></td>
        </tr></tfoot>`
      : '';

    tokenByExecutorSection = `<div class="section-head" style="margin-top:28px"><div><h2>Token usage by agent</h2><p>Provider-reported tokens aggregated across all runs, grouped by executor.</p></div></div>
    <div class="analytics-panel">
      <div class="tablebox">
        <table>
          <thead><tr><th>Agent / executor</th><th>Input</th><th>Output</th><th>Cached</th><th>Input + output</th><th>Relative volume</th><th>Contributing runs</th></tr></thead>
          <tbody>${rows}</tbody>
          ${totalRow}
        </table>
      </div>
      ${incompleteNote}
    </div>`;
  } else {
    // All runs either have no attempts or no reported usage.
    const reason = model.attemptErrors?.size === totalRuns && totalRuns > 0
      ? 'Token usage is unavailable because execution attempts could not be loaded.'
      : totalRuns === 0
      ? 'No runs recorded yet. Token usage will appear when executors report it for a run.'
      : runsWithNoAttempts === totalRuns
      ? 'No execution attempt records found. Some runs may be older or may not have reached an executor yet.'
      : 'No provider-reported token usage found. Token data is populated when executors report usage after each attempt.';
    tokenByExecutorSection = `<div class="section-head" style="margin-top:28px"><div><h2>Token usage by agent</h2><p>Provider-reported tokens aggregated across all runs, grouped by executor.</p></div></div>
    <div class="analytics-panel">
      <p style="color:var(--muted);font-size:13px">${esc(reason)}</p>
      ${(runsWithNoAttempts || runsWithUnavailableUsage) ? `<p style="color:var(--muted);font-size:11px">${runsWithNoAttempts ? `${runsWithNoAttempts} run${runsWithNoAttempts !== 1 ? 's' : ''} have no attempt records (often older runs). ` : ''}${runsWithUnavailableUsage ? `${runsWithUnavailableUsage} run${runsWithUnavailableUsage !== 1 ? 's' : ''} have attempts but no provider-reported token usage.` : ''}</p>` : ''}
    </div>`;
  }

  // ── token usage over time ──────────────────────
  const tokenTimeHtml = tokenSparklineMarkup(tokenBuckets);
  const tokenOverTimeSection = tokenTimeHtml
    ? `<div class="section-head" style="margin-top:28px"><div><h2>Token usage over time · last 30 days</h2><p>Reported daily input/output/cached token totals in UTC. Missing records are excluded; totals may be partial.</p></div></div>
      <div class="analytics-panel">
        ${tokenTimeHtml}
        <div class="spark-legend" style="margin-top:8px">
          <span class="spark-legend-item"><span class="spark-swatch tok-in"></span>Input</span>
          <span class="spark-legend-item"><span class="spark-swatch tok-out"></span>Output</span>
          <span class="spark-legend-item"><span class="spark-swatch tok-cached"></span>Cached</span>
        </div>
      </div>`
    : '';

  // ── run volume sparkline legend ─────────────────
  const sparkLegend = `<div class="spark-legend" style="margin-top:6px">
    <span class="spark-legend-item"><span class="spark-swatch succeeded"></span>Succeeded</span>
    <span class="spark-legend-item"><span class="spark-swatch failed"></span>Failed</span>
    <span class="spark-legend-item"><span class="spark-swatch pending"></span>In progress / other</span>
  </div>`;

  const container = document.getElementById('analyticsContent');
  if (!container) return;
  const failures = model.attemptErrors || new Map();
  const warning = failures.size ? `<div class="notice error" role="alert">Attempt data could not be loaded for ${failures.size} run(s). Attempt, timing and token statistics are partial; run counts include all runs.<ul>${[...failures].map(([id, error]) => `<li><a href="#run/${encodeURIComponent(id)}">${esc(id)}</a>: ${esc(error)}</li>`).join('')}</ul></div>` : '';
  container.innerHTML = warning + summaryHtml + volumeSection + sparkLegend + outcomesSection + executorActivitySection + tokenByExecutorSection + tokenOverTimeSection;
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
