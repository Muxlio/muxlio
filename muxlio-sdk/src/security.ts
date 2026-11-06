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
      if (node.environment != null) assertPublicEnvironment(node.environment);
      if (node.kind === 'execute' && visit(node.params)) {
        throw new Error('muxlio: literal credentials are forbidden in execution parameters; use secretRefs and an executionPolicy allowlist');
      }
    }
  }
}

/** A closed metadata schema prevents arbitrary environment values in exports. */
function assertPublicEnvironment(value: unknown): void {
  const fail = (): never => { throw new Error('muxlio: environment manifests accept public version pins, digests, and allowlist names only'); };
  const object = (value: unknown): Record<string, unknown> => {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return fail();
    return value as Record<string, unknown>;
  };
  const manifest = object(value);
  const fields = new Set(['version', 'digest', 'protocolDigest', 'runners', 'packages', 'containers', 'skills', 'allowedEnvironment', 'permissions']);
  const digest = /^sha256:[a-f0-9]{64}$/;
  const name = /^[A-Za-z_][A-Za-z0-9_. /-]{0,127}$/;
  const version = /^[0-9]+\.[0-9]+(?:\.[0-9]+)?(?:-[A-Za-z0-9.-]+)?(?:\+[A-Za-z0-9.-]+)?$/;
  if (Object.keys(manifest).some(key => !fields.has(key)) || manifest.version !== '1') fail();
  for (const field of ['digest', 'protocolDigest']) {
    if (typeof manifest[field] !== 'string' || !digest.test(manifest[field] as string)) fail();
  }
  for (const field of ['runners', 'packages', 'containers', 'skills']) {
    if (manifest[field] === undefined) continue;
    for (const [key, pin] of Object.entries(object(manifest[field]))) {
      if (!name.test(key) || typeof pin !== 'string' || !(field === 'runners' ? version : digest).test(pin)) fail();
    }
  }
  for (const field of ['allowedEnvironment', 'permissions']) {
    if (manifest[field] === undefined) continue;
    const list = manifest[field];
    if (!Array.isArray(list)) fail();
    for (const item of list as unknown[]) {
      if (typeof item !== 'string') fail();
      if (field === 'allowedEnvironment' ? !/^[A-Za-z_][A-Za-z0-9_]*$/.test(item as string) : !['host-execution', 'network', 'workspace-read', 'workspace-write'].includes(item as string)) fail();
    }
  }
}
