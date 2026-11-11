// @muxlio/sdk — TypeScript plan builder and types for authoring Muxlio
// workflow definitions that compile to the Plan IR JSON consumed by the
// Go coordinator.

export { assertNoLiteralCredentials } from './security.js';
export type { Count, Amount, AccountingFidelity, Reading, Measure, TokenMeasures, CostBasis, AccountingEntry, AccountingKey, AccountingLimit, MeterCoverage, AccountingModel, Accounting, RemoteAccountingUpdate, SummaryAmount, AccountingSummary } from './accounting.js';

export type {
  Plan,
  RunLimits,
  Subgraph,
  Node,
  NodeKind,
  ExecutorRequirements,
  EnvironmentManifest,
  ProviderSession,
  ExecutionSecurityParams,
  Binding,
  ValueRef,
  Expr,
  OutputDecl,
  RetryPolicy,
  JSONSchema,
  ArtifactRef,
} from './types.js';

export {
  // Expression helpers
  input,
  item,
  state,
  literal,
  field,
  index,
  len,
  eq,
  ne,
  lt,
  gt,
  le,
  ge,
  and,
  or,
  not,
  add,
  sub,
  mul,
  div,
  mod,
  inputField,
  itemField,
  stateField,
} from './expr.js';

export {
  defineWorkflow,
  computeDigest,
  toExpr,
} from './builder.js';

export { refine, speculate } from './composition.js';
export type { TypedBinding, TypedExpr, RefineOptions, SpeculateOptions } from './composition.js';

export type {
  BuildContext,
  WorkflowDefinition,
  ExecuteOptions,
  ExecuteResult,
  MapOptions,
  MapResult,
  BranchOptions,
  BranchResult,
  LoopOptions,
  LoopResult,
  ProjectOptions,
  ProjectResult,
  ExprLike,
} from './builder.js';

export {
  // Main API
  workflow,
  task,
  artifact,
  zodToJsonSchema,
} from './workflow.js';

export type {
  // Task definition types
  TaskDef,
  TaskDefinition,
  TaskOutputSchema,
  ArtifactDecl,
  // Workflow types
  WorkflowOptions,
  WorkflowBuildContext,
  CompiledWorkflow,
  StepResult,
  StepOptions,
  EachOptions,
  EachResult,
  RepeatUntilOptions,
  RepeatUntilResult,
  SymbolicRef,
} from './workflow.js';
