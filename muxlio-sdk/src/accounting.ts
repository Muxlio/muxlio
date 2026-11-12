/** Decimal strings preserve integer precision and exact credit/currency values. */
export type Count = string;
export type Amount = string;
export type AccountingFidelity = 'metered' | 'derived-exact' | 'provider-estimate' | 'proxy';
export interface Reading<T> {
  value: T;
  source: string;
  fidelity: AccountingFidelity;
  method?: string;
  version?: string;
  catalogId?: string;
  attributes?: Record<string,string>;
}
export interface Measure<T> { collected?: Reading<T>; estimated?: Reading<T> }
export interface TokenMeasures {
  total?: Measure<Count>;
  input?: Measure<Count>;
  output?: Measure<Count>;
  cacheRead?: Measure<Count>;
  cacheWrite?: Measure<Count>;
  reasoning?: Measure<Count>;
}
export type CostBasis = 'api-equivalent' | 'plan-debit' | 'unspecified' | 'credit-replacement' | 'provider-metered' | 'model-reference';
export interface AccountingEntry {
  id: string;
  provider?: string;
  model?: string;
  tokens?: TokenMeasures;
  credits?: Array<{provider: string; unit: string; amount: Measure<Amount>}>;
  costs?: Array<{currency: string; basis: CostBasis; amount: Measure<Amount>}>;
}
export interface AccountingKey {
  kind: 'tokens' | 'credits' | 'cost';
  provider?: string;
  unit?: string;
  currency?: string;
  basis?: CostBasis;
}
export interface AccountingLimit extends AccountingKey {
  maximum: string;
  evidence?: 'collected-only' | 'collected-or-estimated';
  fidelity?: 'metered' | 'any';
  onMissing?: 'fail-closed' | 'fail-open';
}
export interface MeterCoverage {
  key: AccountingKey;
  state: 'complete' | 'partial' | 'unknown' | 'inapplicable';
  reason?: string;
}
export interface Accounting {
  revision: number;
  final: boolean;
  entries?: AccountingEntry[];
  coverage?: MeterCoverage[];
  models?: AccountingModel[];
  sessions?: AccountingSession[];
}
export interface AccountingSession { id: string; provider: string; source: string; version?: string; startedAt?: string; finishedAt?: string; principal?: string; isolated?: boolean }
export interface AccountingModel {
  id: string;
  role: 'requested' | 'selected' | 'served';
  source: string;
}
/** Authorized remote task evidence; ordinary lookup usage remains on its own attempt. */
export interface RemoteAccountingUpdate {
  taskId: string;
  sourceRevision: string;
  accounting: Accounting;
}
export interface SummaryAmount {
  evidence?: {collected?: AccountingFidelity[]; estimated?: AccountingFidelity[]};
  collectedSubtotal: string | null;
  estimatedFallbackSubtotal: string | null;
  knownSubtotal: string | null;
  effectiveTotal: string | null;
  completeness: MeterCoverage['state'];
  attempts: {complete: number; partial: number; unknown: number; pending: number; inapplicable: number};
}
export interface AccountingSummary {
  referenceValue?: SummaryAmount & { methods?: string[] };
  tokens: SummaryAmount;
  tokenDimensions: Record<'input' | 'output' | 'cacheRead' | 'cacheWrite' | 'reasoning', SummaryAmount>;
  credits: Array<{provider: string; unit: string; value: SummaryAmount}>;
  costs: Array<{currency: string; basis: CostBasis; value: SummaryAmount}>;
  unattributed: {credits: {unknownAttempts: number; pendingAttempts: number}; costs: {unknownAttempts: number; pendingAttempts: number}};
}
