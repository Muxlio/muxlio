// @muxlio/compiler — restricted TypeScript frontend (Frontend A) that
// lowers async workflow definitions to the canonical Plan IR.
//
// For M0, this uses a recording-based approach: the `run` function is
// called once at compile time with a recording context. Calls to
// `ctx.execute`, `ctx.map`, `ctx.branch`, `ctx.loop` are traced to build
// the IR. A full AST-analysis compiler that validates the restricted
// subset without executing `run` is a later milestone.

export { defineAsyncWorkflow } from './async.js';
export type { AsyncWorkflowDefinition, CompiledWorkflow } from './async.js';
