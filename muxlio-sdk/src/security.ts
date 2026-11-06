import type { Plan } from './types.js';

/** Reject conventional credential fields before authoring APIs emit a plan.
 * Arbitrary strings are not classifiable: use handles for all secret delivery.
 */
export function assertNoLiteralCredentials(plan: Plan): void {
  const credentialKeys = new Set([
    'secrets', 'credentials', 'authorization', 'password', 'apikey',
    'accesstoken', 'refreshtoken', 'token', 'clientsecret',
  ]);
  const visit = (value: unknown): boolean => {
    if (Array.isArray(value)) return value.some(visit);
    if (value === null || typeof value !== 'object') return false;
    return Object.entries(value).some(([key, item]) => {
      const normalized = key.toLowerCase().replace(/[_-]/g, '');
      if (normalized === 'secretrefs' || normalized === 'allowedsecrets') return false;
      return (credentialKeys.has(normalized) && item != null && item !== '') || visit(item);
    });
  };
  for (const graph of Object.values(plan.subgraphs)) {
    for (const node of graph.nodes) {
      if (node.kind === 'execute' && visit(node.params)) {
        throw new Error('muxlio: literal credentials are forbidden in execution parameters; use secretRefs and an executionPolicy allowlist');
      }
    }
  }
}
