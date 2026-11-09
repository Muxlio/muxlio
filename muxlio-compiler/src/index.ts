// @muxlio/compiler — restricted TypeScript frontend (Frontend A) that
// lowers async workflow definitions to the canonical Plan IR.
//
// The `run` function is called once at compile time with a recording context. Calls to
// `ctx.execute`, `ctx.map`, `ctx.branch`, `ctx.loop` are traced to build
// the IR.

export { defineAsyncWorkflow } from './async.js';
export type { AsyncWorkflowDefinition, CompiledWorkflow } from './async.js';
