// This compile-time marker is excluded from JSON and plan digests. Only
// SDK-created expressions/bindings are executable in the symbolic workflow
// API; a user object with the same JSON shape remains data.
const AUTHORED = Symbol.for('muxlio.authored');

export function authored<T extends object>(value: T): T {
  return Object.defineProperty({ ...value }, AUTHORED, { value: true });
}

export function isAuthored(value: unknown): boolean {
  return typeof value === 'object' && value !== null && (value as any)[AUTHORED] === true;
}
