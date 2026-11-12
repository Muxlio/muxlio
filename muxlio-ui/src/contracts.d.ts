/** Coordinator accounting contract. Totals are decimal strings, never inferred
 * from credits or context windows. Null means unavailable; "0" is a reading. */
type AccountingTotal = string | null;
interface AccountingMeasureSummary {
  completeness?: string;
  knownSubtotal?: AccountingTotal;
  effectiveTotal?: AccountingTotal;
  collectedSubtotal?: AccountingTotal;
  estimatedFallbackSubtotal?: AccountingTotal;
  evidence?: { collected?: string[] };
  unknownAttempts?: number;
  pendingAttempts?: number;
  attempts?: { complete?: number; partial?: number; unknown?: number; pending?: number; inapplicable?: number };
}
interface AccountingSummary {
  referenceValue?: AccountingMeasureSummary & { methods?: string[] };
  tokens?: AccountingMeasureSummary;
  tokenDimensions?: Record<string, AccountingMeasureSummary>;
  credits?: { provider: string; unit: string; value: AccountingMeasureSummary }[];
  costs?: { currency: string; basis: string; value: AccountingMeasureSummary }[];
  unattributed?: Record<string, AccountingMeasureSummary>;
}
interface UsagePresentationOptions {
  attemptCount?: number;
  runState?: string;
  runError?: unknown;
}
