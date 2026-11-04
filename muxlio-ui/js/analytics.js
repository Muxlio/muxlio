/* ── analytics ─────────────────────────────────── */
// Cross-run operational insights: run volume, outcomes, and provider-reported
// token usage. Aggregated client-side from the existing runs + attempts APIs.

// Fetch all runs and their execution attempts, then compute aggregated stats.
// Returns a structured object; callers render it via renderAnalytics().
async function loadAnalyticsData() {
  const runsData = await api('/v1/runs');
  const runs = (runsData.runs || []).slice().sort((a, b) =>
    new Date(a.createdAt) - new Date(b.createdAt)
  );

  // Fetch all attempts so all-time totals describe the same run set. Keep the
  // fan-out bounded to avoid overwhelming the coordinator on large histories.
  const targets = runs;
  const attemptsByRun = new Map();
  const concurrency = 12;
  for (let start = 0; start < targets.length; start += concurrency) await Promise.all(targets.slice(start, start + concurrency).map(async run => {
    try {
      const d = await api(`/v1/runs/${encodeURIComponent(run.id)}/attempts`);
      attemptsByRun.set(run.id, d.attempts || []);
    } catch {
      attemptsByRun.set(run.id, []);
    }
  }));

  return buildAnalyticsModel(runs, attemptsByRun);
}

// Build the analytics model from runs + attempt maps.
// Exported (module-style) only for testing; workspace.js uses loadAnalyticsData.
function buildAnalyticsModel(runs, attemptsByRun) {
  const states = ['running', 'waiting', 'paused', 'succeeded', 'failed', 'cancelled'];

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
      label: d.toLocaleDateString([], { month: 'short', day: 'numeric' }),
      dateKey: d.toISOString().slice(0, 10),
      total: 0,
      succeeded: 0,
      failed: 0,
    };
  });
  const bucketByDate = new Map(buckets.map(b => [b.dateKey, b]));
  for (const r of runs) {
    if (!r.createdAt) continue;
    const key = new Date(r.createdAt).toISOString().slice(0, 10);
    const b = bucketByDate.get(key);
    if (!b) continue;
    b.total++;
    if (r.state === 'succeeded') b.succeeded++;
    if (r.state === 'failed') b.failed++;
  }

  // ── token usage by executor/agent ─────────────
  // executor is the string label on ExecutionAttempt (e.g. "kiro", "openai").
  // We aggregate input+output+cached per executor.
  // Runs without token records are counted separately.
  const executorUsage = new Map(); // executor → {input, output, cached, runIds: Set, incomplete: 0}
  let runsWithNoAttempts = 0;
  let runsWithUnavailableUsage = 0;

  for (const r of runs) {
    const attempts = attemptsByRun.get(r.id) || [];
    if (!attempts.length) {
      runsWithNoAttempts++;
      continue;
    }
    let anyReported = false;
    let hasCompletedAttempt = false;
    for (const a of attempts) {
      // The initial running row is updated in place when the attempt finishes.
      // Ignore it even if a provider happens to attach partial usage to it.
      if (a.state === 'running') continue;
      hasCompletedAttempt = true;
      if (a.usage == null) continue;
      anyReported = true;
      const key = a.executor || 'unknown';
      let entry = executorUsage.get(key);
      if (!entry) {
        entry = { input: 0, output: 0, cached: 0, runIds: new Set(), incompleteRuns: 0 };
        executorUsage.set(key, entry);
      }
      entry.input += Number(a.usage.input) || 0;
      entry.output += Number(a.usage.output) || 0;
      entry.cached += Number(a.usage.cached) || 0;
      entry.runIds.add(r.id);
    }
    if (hasCompletedAttempt && !anyReported) {
      runsWithUnavailableUsage++;
    }
  }

  // ── token usage over time (last 30 days) ──────
  // Aggregate reported tokens per day bucket.
  const tokenBuckets = Array.from({ length: BUCKET_COUNT }, (_, i) => {
    const d = new Date(now - (BUCKET_COUNT - 1 - i) * DAY_MS);
    return {
      label: d.toLocaleDateString([], { month: 'short', day: 'numeric' }),
      dateKey: d.toISOString().slice(0, 10),
      input: 0,
      output: 0,
      cached: 0,
      hasData: false,
    };
  });
  const tokenBucketByDate = new Map(tokenBuckets.map(b => [b.dateKey, b]));
  for (const r of runs) {
    if (!r.createdAt) continue;
    const key = new Date(r.createdAt).toISOString().slice(0, 10);
    const b = tokenBucketByDate.get(key);
    if (!b) continue;
    const attempts = attemptsByRun.get(r.id) || [];
    for (const a of attempts) {
      if (a.state === 'running' || a.usage == null) continue;
      b.input += Number(a.usage.input) || 0;
      b.output += Number(a.usage.output) || 0;
      b.cached += Number(a.usage.cached) || 0;
      b.hasData = true;
    }
  }

  // ── totals ─────────────────────────────────────
  let totalInput = 0, totalOutput = 0, totalCached = 0, anyTokenData = false;
  for (const [, e] of executorUsage) {
    totalInput += e.input;
    totalOutput += e.output;
    totalCached += e.cached;
    anyTokenData = true;
  }

  return {
    runs,
    stateCounts,
    buckets,
    tokenBuckets,
    executorUsage,
    runsWithNoAttempts,
    runsWithUnavailableUsage,
    totals: anyTokenData ? { input: totalInput, output: totalOutput, cached: totalCached } : null,
  };
}

// Format token count with K/M suffix.
function fmtTok(n) {
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

// Render the full analytics page from the model.
function renderAnalytics(model) {
  const { runs, stateCounts, buckets, tokenBuckets, executorUsage, runsWithNoAttempts, runsWithUnavailableUsage, totals } = model;

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
    <div class="summary"><div class="summary-label">Active now</div><div class="summary-value${(stateCounts.running || 0) > 0 ? ' amber' : ''}">${esc(String((stateCounts.running || 0) + (stateCounts.waiting || 0) + (stateCounts.paused || 0)))}</div></div>
    <div class="summary"><div class="summary-label">Success rate</div><div class="summary-value${successRate !== '—' && stateCounts.failed > 0 ? '' : ' green'}">${esc(successRate)}</div></div>
  </div>`;

  // ── run volume chart ───────────────────────────
  const volumeSection = `<div class="section-head"><div><h2>Run volume · last 30 days</h2><p>Daily run counts, coloured by outcome.</p></div></div>
  <div class="analytics-panel">${sparklineMarkup(buckets)}</div>`;

  // ── state breakdown bar chart ──────────────────
  const stateItems = [
    { label: 'Succeeded', value: stateCounts.succeeded || 0, colorVar: '--green' },
    { label: 'Failed',    value: stateCounts.failed    || 0, colorVar: '--red'   },
    { label: 'Cancelled', value: stateCounts.cancelled || 0, colorVar: '--muted' },
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

  const outcomesSection = `<div class="section-head" style="margin-top:28px"><div><h2>Outcomes</h2><p>All-time run counts by terminal state.</p></div></div>
  <div class="analytics-panel">${stateBreakdown}</div>`;

  // ── token usage by executor ────────────────────
  const executorRows = [...executorUsage.entries()]
    .sort((a, b) => (b[1].input + b[1].output) - (a[1].input + a[1].output));

  let tokenByExecutorSection = '';
  if (executorRows.length > 0) {
    const maxTokens = Math.max(...executorRows.map(([, e]) => e.input + e.output), 1);
    const rows = executorRows.map(([exec, e]) => {
      const total = e.input + e.output + e.cached;
      const pct = maxTokens > 0 ? ((e.input + e.output) / maxTokens) * 100 : 0;
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
        <td class="num">${esc(fmtTok(e.input + e.output))}</td>
        <td><div class="abc-track" style="width:120px;display:inline-flex"><div class="abc-bar" style="width:${pct.toFixed(1)}%;background:var(--teal)"></div></div></td>
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
          <td class="num">${esc(fmtTok(totals.input + totals.output))}</td>
          <td colspan="2"></td>
        </tr></tfoot>`
      : '';

    tokenByExecutorSection = `<div class="section-head" style="margin-top:28px"><div><h2>Token usage by agent</h2><p>Provider-reported tokens aggregated across all runs, grouped by executor.</p></div></div>
    <div class="analytics-panel">
      <div class="tablebox">
        <table>
          <thead><tr><th>Agent / executor</th><th>Input</th><th>Output</th><th>Cached</th><th>Total</th><th>Share</th><th>Contributing runs</th></tr></thead>
          <tbody>${rows}</tbody>
          ${totalRow}
        </table>
      </div>
      ${incompleteNote}
    </div>`;
  } else {
    // All runs either have no attempts or no reported usage.
    const reason = totalRuns === 0
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
    ? `<div class="section-head" style="margin-top:28px"><div><h2>Token usage over time · last 30 days</h2><p>Daily input/output/cached token totals. Stacked by type.</p></div></div>
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
  container.innerHTML = summaryHtml + volumeSection + sparkLegend + outcomesSection + tokenByExecutorSection + tokenOverTimeSection;
}

async function loadAnalytics() {
  const container = document.getElementById('analyticsContent');
  if (container) container.innerHTML = '<div class="loading">Loading analytics…</div>';
  try {
    const model = await loadAnalyticsData();
    renderAnalytics(model);
  } catch (err) {
    if (err.name === 'AbortError') return;
    if (container) container.innerHTML = `<div class="notice error" role="alert">Unable to load analytics: ${esc(err.message)}</div>`;
  }
}
