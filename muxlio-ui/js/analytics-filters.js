/* ── analytics filters & URL state ───────────────── */

const ANALYTICS_RANGE_PRESETS = {
  '24h': { ms: 86400000, buckets: 24, bucketUnit: 'hour' },
  '7d': { ms: 7 * 86400000, buckets: 7, bucketUnit: 'day' },
  '30d': { ms: 30 * 86400000, buckets: 30, bucketUnit: 'day' },
  '90d': { ms: 90 * 86400000, buckets: 30, bucketUnit: 'day', bucketSpanDays: 3 },
};

const ANALYTICS_RUN_STATES = ['created', 'running', 'waiting', 'paused', 'succeeded', 'failed', 'cancelled'];

function analyticsDefaultFilters(now = new Date()) {
  const range = analyticsResolveRange({ preset: '30d' }, now);
  return {
    preset: '30d',
    from: range.from.toISOString(),
    to: range.to.toISOString(),
    states: [],
    workflow: '',
    executor: '',
    tz: 'UTC',
    measureId: '',
    drill: '',
    page: 1,
  };
}

function analyticsFiltersFromURL(hash = currentRoute()) {
  const base = hash.startsWith('/analytics') ? hash : '/analytics';
  const query = base.includes('?') ? base.slice(base.indexOf('?') + 1) : '';
  const params = new URLSearchParams(query);
  const filters = analyticsDefaultFilters();
  if (params.has('range')) filters.preset = params.get('range') || filters.preset;
  if (params.has('from')) filters.from = params.get('from');
  if (params.has('to')) filters.to = params.get('to');
  if (filters.preset === 'custom' && (!filters.from || !filters.to)) {
    filters.preset = '30d';
    const range = analyticsResolveRange(filters);
    filters.from = range.from.toISOString();
    filters.to = range.to.toISOString();
  }
  if (params.has('state')) {
    filters.states = params.get('state').split(',').map(s => s.trim()).filter(Boolean);
  }
  filters.workflow = params.get('workflow') || params.get('q') || '';
  filters.executor = params.get('executor') || '';
  filters.tz = params.get('tz') || 'UTC';
  filters.measureId = params.get('measure') || '';
  filters.drill = params.get('drill') || '';
  filters.page = Math.max(1, Number(params.get('page')) || 1);
  if (filters.preset !== 'custom') {
    const range = analyticsResolveRange(filters);
    filters.from = range.from.toISOString();
    filters.to = range.to.toISOString();
  }
  return filters;
}

function analyticsFiltersToURL(filters) {
  const params = new URLSearchParams();
  if (filters.preset && filters.preset !== '30d') params.set('range', filters.preset);
  if (filters.preset === 'custom') {
    if (filters.from) params.set('from', filters.from);
    if (filters.to) params.set('to', filters.to);
  }
  if (filters.states?.length) params.set('state', filters.states.join(','));
  if (filters.workflow) params.set('workflow', filters.workflow);
  if (filters.executor) params.set('executor', filters.executor);
  if (filters.tz && filters.tz !== 'UTC') params.set('tz', filters.tz);
  if (filters.measureId) params.set('measure', filters.measureId);
  if (filters.drill) params.set('drill', filters.drill);
  if (filters.page > 1) params.set('page', String(filters.page));
  const qs = params.toString();
  return qs ? `/analytics?${qs}` : '/analytics';
}

function analyticsResolveRange(filters, now = new Date()) {
  const end = filters.preset === 'custom' && filters.to ? new Date(filters.to) : now;
  const preset = ANALYTICS_RANGE_PRESETS[filters.preset];
  if (filters.preset === 'custom' && filters.from && filters.to) {
    const from = new Date(filters.from);
    const span = Math.max(0, end - from);
    const buckets = span <= 86400000 ? 24 : span <= 7 * 86400000 ? 7 : 30;
    const bucketMs = span / buckets;
    return { from, to: end, spanMs: span, buckets, bucketMs, bucketUnit: span <= 86400000 ? 'hour' : 'day', label: analyticsFormatRangeLabel(from, end) };
  }
  const span = preset?.ms || ANALYTICS_RANGE_PRESETS['30d'].ms;
  const from = new Date(end - span);
  const bucketSpanDays = preset?.bucketSpanDays || 1;
  const bucketMs = preset?.bucketUnit === 'hour' ? 3600000 : bucketSpanDays * 86400000;
  return {
    from,
    to: end,
    spanMs: span,
    buckets: preset?.buckets || 30,
    bucketMs,
    bucketUnit: preset?.bucketUnit || 'day',
    bucketSpanDays,
    label: analyticsFormatRangeLabel(from, end),
    preset: filters.preset || '30d',
  };
}

function analyticsPriorRange(range) {
  return {
    from: new Date(range.from - range.spanMs),
    to: new Date(range.from),
    spanMs: range.spanMs,
    label: 'previous period',
  };
}

function analyticsFormatRangeLabel(from, to) {
  const opts = { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', timeZone: 'UTC' };
  return `${from.toLocaleString('en-US', opts)} – ${to.toLocaleString('en-US', opts)} UTC`;
}

// datetime-local values are interpreted in the viewer's timezone. Format the
// shared ISO instants as local wall time instead of slicing their UTC text.
function analyticsDateTimeLocalValue(value) {
  const d = new Date(value);
  if (!Number.isFinite(d.getTime())) return '';
  const pad = n => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function analyticsDayBuckets(range, tz = 'UTC') {
  const buckets = [];
  const unitMs = range.bucketUnit === 'hour' ? 3600000 : 86400000;
  const width = range.bucketMs || unitMs;
  // Calendar buckets start on UTC hour/day boundaries. A trailing range can
  // cross one extra calendar bucket when its endpoints are partial days.
  const calendarStartMs = Math.floor(range.from.getTime() / unitMs) * unitMs;
  const startMs = range.bucketUnit === 'hour'
    ? calendarStartMs
    : Math.floor(calendarStartMs / width) * width;
  const bucketCount = Math.max(1, Math.ceil((range.to.getTime() - startMs) / width));
  for (let i = 0; i < bucketCount; i++) {
    const bucketStartMs = startMs + i * width;
    const d = new Date(bucketStartMs);
    const dateKey = analyticsBucketKey(d, range, tz);
    const label = analyticsBucketLabel(d, range, tz);
    buckets.push({ label, dateKey, startMs: bucketStartMs, endMs: bucketStartMs + width });
  }
  return buckets;
}

function analyticsBucketKey(d, range, tz) {
  if (range.bucketUnit === 'hour') {
    return d.toISOString().slice(0, 13);
  }
  return new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate())).toISOString().slice(0, 10);
}

function analyticsBucketLabel(d, range, tz) {
  if (range.bucketUnit === 'hour') {
    return d.toLocaleString('en-US', { hour: 'numeric', minute: '2-digit', timeZone: tz || 'UTC' });
  }
  const span = range.bucketSpanDays || 1;
  const label = d.toLocaleDateString([], { month: 'short', day: 'numeric', timeZone: tz || 'UTC' });
  return span > 1 ? `${label} (+${span - 1}d)` : label;
}

function analyticsRangeThroughNow(range, now = new Date()) {
  const toMs = Math.max(range.to.getTime(), now.getTime());
  if (toMs === range.to.getTime()) return range;
  return { ...range, to: new Date(toMs) };
}

function analyticsRunInRange(run, range) {
  const created = Date.parse(run.createdAt);
  if (!Number.isFinite(created)) return false;
  return created >= range.from.getTime() && created <= range.to.getTime();
}

function analyticsFiltersQueryString(filters, range) {
  const params = new URLSearchParams();
  params.set('from', range.from.toISOString());
  params.set('to', range.to.toISOString());
  if (filters.states?.length) params.set('state', filters.states.join(','));
  if (filters.workflow) params.set('workflow', filters.workflow);
  if (filters.executor) params.set('executor', filters.executor);
  params.set('limit', '200');
  params.set('offset', String(Math.max(0, (filters.page - 1) * 200)));
  return params.toString();
}

function analyticsActiveFilterLabels(filters, range) {
  const labels = [];
  labels.push({ key: 'range', text: filters.preset === 'custom' ? range.label : `${filters.preset || '30d'} window` });
  if (filters.states?.length) labels.push({ key: 'state', text: `States: ${filters.states.join(', ')}` });
  if (filters.workflow) labels.push({ key: 'workflow', text: `Workflow: ${filters.workflow}` });
  if (filters.executor) labels.push({ key: 'executor', text: `Executor: ${filters.executor}` });
  labels.push({ key: 'tz', text: `UTC calendar buckets · labels ${filters.tz || 'UTC'}` });
  labels.push({ key: 'populations', text: 'Runs by createdAt · usage by attempt time' });
  return labels;
}

function analyticsFiltersEqual(a, b) {
  return JSON.stringify({ ...a, page: 1 }) === JSON.stringify({ ...b, page: 1 });
}

const ANALYTICS_RUN_STATE_KEYS = ['created', 'running', 'waiting', 'paused', 'succeeded', 'failed', 'cancelled'];

function analyticsStateCountsFromContext(ctx, runs) {
  const counts = Object.fromEntries(ANALYTICS_RUN_STATE_KEYS.map(s => [s, 0]));
  const total = ctx.totalRuns ?? runs.length;
  const raw = ctx.stateCounts;
  if (raw && typeof raw === 'object' && runs.length < total) {
    for (const s of ANALYTICS_RUN_STATE_KEYS) {
      if (typeof raw[s] === 'number' && Number.isFinite(raw[s])) counts[s] = raw[s];
    }
    return counts;
  }
  for (const r of runs) {
    if (counts[r.state] !== undefined) counts[r.state]++;
  }
  return counts;
}

function analyticsBucketForTimestamp(ms, range, tz = 'UTC') {
  const buckets = analyticsDayBuckets(range, tz);
  for (const b of buckets) {
    if (ms >= b.startMs && ms < b.endMs) return b;
  }
  const key = analyticsBucketKey(new Date(ms), range, tz);
  return buckets.find(b => b.dateKey === key) || null;
}

function analyticsApplyFiltersToModelInputs(runs, attemptsByRun, filters, range) {
  const states = new Set(filters.states || []);
  const workflow = (filters.workflow || '').toLowerCase();
  const executor = (filters.executor || '').toLowerCase();
  const filteredRuns = runs.filter(r => {
    if (!analyticsRunInRange(r, range)) return false;
    if (states.size && !states.has(r.state)) return false;
    if (workflow && !(r.workflowName || '').toLowerCase().includes(workflow)) return false;
    return true;
  });
  const filteredAttempts = new Map();
  const executorFilteredRuns = [];
  for (const r of filteredRuns) {
    let attempts = attemptsByRun.get(r.id) || [];
    if (executor) {
      attempts = attempts.filter(a => (a.executor || '').toLowerCase().includes(executor));
      if (!attempts.length) continue;
    }
    executorFilteredRuns.push(r);
    filteredAttempts.set(r.id, attempts);
  }
  return { runs: executorFilteredRuns, attemptsByRun: filteredAttempts };
}
