// Analytics accounting mirrors the coordinator UsageSummary contract.
// Never infers tokens from credits, costs, or context occupancy.

const ANALYTICS_ATTEMPT_PENDING = new Set(['running', 'queued']);

function analyticsAttemptPending(state) {
  return ANALYTICS_ATTEMPT_PENDING.has(state);
}

function analyticsUsageNumber(value) {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null;
}

function analyticsUsageFieldReported(usage, field) {
  if (usage == null) return undefined;
  if (!Object.prototype.hasOwnProperty.call(usage, field)) {
    return undefined;
  }
  if (usage[field] === null) return undefined;
  const n = analyticsUsageNumber(usage[field]);
  return n == null ? undefined : n;
}

function analyticsUsageAnyReported(usage) {
  return ['input', 'output', 'cached'].some(field => analyticsUsageFieldReported(usage, field) !== undefined);
}

function analyticsUsageComplete(usage) {
  if (usage == null) return false;
  return ['input', 'output', 'cached'].every(field => analyticsUsageFieldReported(usage, field) !== undefined);
}

function analyticsAddFloat(total, value) {
  if (total == null) return value;
  return Math.min(Number.MAX_VALUE, total + value);
}

function analyticsAddInt(total, value) {
  if (total == null) return value;
  return total + value;
}

function analyticsMeasurementsValid(m) {
  if (!m) return false;
  if (m.estimatedTokens != null && (!Number.isInteger(m.estimatedTokens) || m.estimatedTokens < 0)) return false;
  for (const key of ['costUsd', 'estimatedCostUsd']) {
    const v = m[key];
    if (v != null && (!Number.isFinite(v) || v < 0)) return false;
  }
  const credit = m.credit;
  if (credit) {
    if (!credit.unit || !String(credit.unit).trim()) return false;
    if (credit.amount != null && (!Number.isFinite(credit.amount) || credit.amount < 0)) return false;
  }
  return true;
}

function analyticsSummarizeTokenDimensions(attempts) {
  const dims = {
    input: { observed: null, reporting: 0, unknown: 0 },
    output: { observed: null, reporting: 0, unknown: 0 },
    cached: { observed: null, reporting: 0, unknown: 0 },
    coverage: { attempts: attempts.length, pending: 0, reporting: 0 },
  };
  for (const a of attempts) {
    if (analyticsAttemptPending(a.state)) {
      dims.coverage.pending++;
      continue;
    }
    const usage = a.usage;
    if (analyticsUsageAnyReported(usage)) dims.coverage.reporting++;
    for (const dim of ['input', 'output', 'cached']) {
      const value = analyticsUsageFieldReported(usage, dim);
      if (value === undefined) {
        dims[dim].unknown++;
        continue;
      }
      dims[dim].reporting++;
      dims[dim].observed = analyticsAddInt(dims[dim].observed, value);
    }
  }
  return dims;
}

function analyticsSummarizeUsage(attempts) {
  const tokenDimensions = analyticsSummarizeTokenDimensions(attempts);
  const summary = {
    tokens: {
      observed: null,
      estimated: null,
      unknown: 0,
      pending: tokenDimensions.coverage.pending,
      reporting: tokenDimensions.coverage.reporting,
    },
    tokenDimensions,
    costUsd: { observed: null, estimated: null, unknown: 0 },
    credits: [],
  };
  const creditTotals = new Map();
  for (const a of attempts) {
    let estimatedTokens = null;
    let cost = null;
    let estimatedCost = null;
    let credit = null;
    const m = a.measurements;
    if (m && analyticsMeasurementsValid(m)) {
      if (m.estimatedTokens != null) estimatedTokens = m.estimatedTokens;
      cost = m.costUsd ?? null;
      estimatedCost = m.estimatedCostUsd ?? null;
      credit = m.credit ?? null;
    }
    const tokenPending = analyticsAttemptPending(a.state);
    const u = a.usage;
    if (u && analyticsUsageComplete(u)) {
      const input = analyticsUsageFieldReported(u, 'input');
      const output = analyticsUsageFieldReported(u, 'output');
      if (input !== undefined && output !== undefined) {
        summary.tokens.observed = analyticsAddFloat(summary.tokens.observed, input + output);
      }
    } else if (estimatedTokens != null) {
      summary.tokens.estimated = analyticsAddFloat(summary.tokens.estimated, estimatedTokens);
    } else if (!tokenPending) {
      summary.tokens.unknown++;
    }
    if (cost != null) {
      summary.costUsd.observed = analyticsAddFloat(summary.costUsd.observed, cost);
    } else if (estimatedCost != null) {
      summary.costUsd.estimated = analyticsAddFloat(summary.costUsd.estimated, estimatedCost);
    } else {
      summary.costUsd.unknown++;
    }
    if (!credit || !credit.unit) continue;
    const unit = credit.unit;
    let row = creditTotals.get(unit);
    if (!row) {
      row = { unit, observed: null, unknown: 0, reported: 0 };
      creditTotals.set(unit, row);
    }
    if (credit.amount == null) continue;
    row.reported++;
    row.observed = analyticsAddFloat(row.observed, credit.amount);
  }
  if (creditTotals.size) {
    summary.credits = [...creditTotals.values()]
      .map(row => ({ unit: row.unit, observed: row.observed, unknown: attempts.length - row.reported }))
      .sort((a, b) => a.unit.localeCompare(b.unit));
  }
  return summary;
}

// Bucket time: finishedAt, then startedAt, then run.createdAt (legacy fallback).
function analyticsAttemptBucketTime(attempt, run) {
  const finish = attempt.finishedAt != null ? Date.parse(attempt.finishedAt) : NaN;
  if (Number.isFinite(finish) && finish > 0) {
    return { ms: finish, provenance: 'finishedAt' };
  }
  const start = attempt.startedAt != null ? Date.parse(attempt.startedAt) : NaN;
  if (Number.isFinite(start) && start > 0) {
    return { ms: start, provenance: 'startedAt' };
  }
  const runCreated = run?.createdAt != null ? Date.parse(run.createdAt) : NaN;
  if (Number.isFinite(runCreated) && runCreated > 0) {
    return { ms: runCreated, provenance: 'runCreatedAt' };
  }
  return null;
}

function analyticsMeasureCatalog(summary) {
  const measures = [];
  const dims = summary?.tokenDimensions;
  const tokens = summary?.tokens;
  if (tokens?.observed != null || analyticsUsageDimensionsPresent(dims)) {
    measures.push({ id: 'tokens', label: 'Token subtotal', kind: 'tokens' });
    if (dims?.input?.observed != null) measures.push({ id: 'token-input', label: 'Input subtotal', kind: 'tokens' });
    if (dims?.output?.observed != null) measures.push({ id: 'token-output', label: 'Output subtotal', kind: 'tokens' });
    if (dims?.cached?.observed != null) measures.push({ id: 'token-cached', label: 'Cached subtotal', kind: 'tokens' });
  } else if (tokens?.estimated != null) {
    measures.push({ id: 'tokens-estimated', label: 'Tokens (estimated)', kind: 'tokens-estimated' });
  }
  if (summary?.costUsd?.observed != null) {
    measures.push({ id: 'cost-observed', label: 'Cost · observed', kind: 'cost-observed' });
  }
  if (summary?.costUsd?.estimated != null) {
    measures.push({ id: 'cost-estimated', label: 'Cost · estimated', kind: 'cost-estimated' });
  }
  for (const row of summary?.credits || []) {
    if (row.observed != null) {
      measures.push({ id: `credit:${row.unit}`, label: `Credits · ${row.unit}`, kind: 'credit', unit: row.unit });
    }
  }
  return measures;
}

function analyticsUsageDimensionsPresent(dimensions) {
  return ['input', 'output', 'cached'].some(name => dimensions?.[name]?.observed != null);
}

function analyticsMeasureValueForAttempt(attempt, measure) {
  if (!measure) return null;
  const m = attempt.measurements;
  const validM = m && analyticsMeasurementsValid(m) ? m : null;
  if (measure.kind === 'tokens') {
    const u = attempt.usage;
    if (measure.id === 'tokens') {
      if (u && analyticsUsageComplete(u)) {
        const input = analyticsUsageFieldReported(u, 'input');
        const output = analyticsUsageFieldReported(u, 'output');
        if (input !== undefined && output !== undefined) return input + output;
      }
      return null;
    }
    const field = { 'token-input': 'input', 'token-output': 'output', 'token-cached': 'cached' }[measure.id];
    if (field) return analyticsUsageFieldReported(u, field) ?? null;
    return null;
  }
  if (measure.kind === 'tokens-estimated') {
    return validM?.estimatedTokens != null ? validM.estimatedTokens : null;
  }
  if (measure.kind === 'cost-observed') {
    return validM?.costUsd != null ? validM.costUsd : null;
  }
  if (measure.kind === 'cost-estimated') {
    if (validM?.costUsd != null) return null;
    return validM?.estimatedCostUsd != null ? validM.estimatedCostUsd : null;
  }
  if (measure.kind === 'credit') {
    const credit = validM?.credit;
    if (!credit || credit.unit !== measure.unit || credit.amount == null) return null;
    return credit.amount;
  }
  return null;
}

function analyticsFormatMeasureValue(measure, value) {
  if (value == null) return 'Unavailable';
  if (measure.kind === 'tokens' || measure.kind === 'tokens-estimated') return fmtTok(value);
  if (measure.kind === 'cost-observed' || measure.kind === 'cost-estimated') return fmtCostUsd(value);
  if (measure.kind === 'credit') return fmtCredits(value);
  return String(value);
}
