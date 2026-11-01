// Resolve this workspace's packages without rewriting shared node_modules.
// Useful for isolated worktrees whose existing dependency symlinks target a
// different checkout. External dependencies still use normal Node resolution.
const entries = new Map([
  ['@muxlio/sdk', new URL('../muxlio-sdk/dist/index.js', import.meta.url).href],
  ['@muxlio/compiler', new URL('../muxlio-compiler/dist/index.js', import.meta.url).href],
  ['@muxlio/testing', new URL('../muxlio-testing/dist/index.js', import.meta.url).href],
]);

export async function resolve(specifier, context, nextResolve) {
  if (entries.has(specifier)) return { url: entries.get(specifier), shortCircuit: true };
  return nextResolve(specifier, context);
}
