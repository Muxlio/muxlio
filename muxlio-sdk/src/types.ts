// Plan IR type definitions. These mirror the Go types in
// muxlio-core/internal/plan/plan.go and are the canonical contract
// between the TypeScript authoring frontends and the Go interpreter.

/** A JSON Schema (subset) used for input/output contracts. */
export type JSONSchema = Record<string, unknown>;

/** A content-addressed artifact reference. */
export interface ArtifactRef {
  digest: string;
  mime: string;
  size?: number;
}

/** A reference to a specific node's output port within the current scope. */
export interface ValueRef {
  node: string;
  port: string;
}

/** A binding is either a reference to another node's output or a computed expression. */
export interface Binding {
  ref?: ValueRef;
  expr?: Expr;
}

/** An output port declaration with its file contract. */
export interface OutputDecl {
  mime?: string;
  path?: string;
  schema?: JSONSchema;
}

/** Retry/effect policy for an execute node. */
export interface RetryPolicy {
  maxAttempts?: number;
  backoffSeconds?: number;
  maxBackoffSeconds?: number;
  /** retryable | idempotency-key | reconcile | manual | none */
  effect?: string;
}

/** The set of supported node kinds. */
export type NodeKind =
  | 'execute'
  | 'map'
  | 'branch'
  | 'loop'
  | 'join'
  | 'wait'
  | 'child'
  | 'project';

/** Requirements checked against the advertised executor descriptor before dispatch.
 * Optional capability values are exact revisions, e.g. { progress: '1' }.
 * Output schemas alone request consumer validation, not native typed generation.
 */
export interface ExecutorRequirements {
  executorVersion?: string;
  contractVersion?: string;
  requiredCapabilities?: Record<string, string>;
  sessionId?: string;
  remoteTask?: { id: string; url?: string };
}

/** A single node within a subgraph. */
export interface Node extends ExecutorRequirements {
  id: string;
  kind: NodeKind;

  // execute
  executor?: string;
  fallbackExecutors?: string[];
  /** Private snapshot by default; shared writes require exclusive keys. */
  workspaceMode?: 'snapshot' | 'read-only' | 'shared-write';
  /** All keys are acquired atomically with a concurrency limit of one. */
  concurrencyKeys?: string[];
  task?: string;
  params?: unknown;
  inputs?: Record<string, Binding>;
  outputs?: Record<string, OutputDecl>;

  // map
  items?: Expr;
  key?: Expr;
  concurrency?: number;
  maxItems?: number;
  bodyRef?: string;

  // branch
  condition?: Expr;
  thenRef?: string;
  elseRef?: string;

  // loop
  while?: Expr;
  initState?: Expr;
  maxIterations?: number;
  stateRef?: string;
  /** When the loop hits maxIterations while `while` is still true: `pause` or `fail`. */
  onExhausted?: 'pause' | 'fail';

  // retry
  timeoutSeconds?: number;
  queueTimeoutSeconds?: number;
  retry?: RetryPolicy;
}

/** A named subgraph containing nodes and an output projection. */
export interface Subgraph {
  nodes: Node[];
  output?: Record<string, Binding>;
}

/** The immutable compiled workflow plan. */
export interface Plan {
  /** Wall-clock limit from persisted run creation, including queueing and pauses. */
  deadlineSeconds?: number;
  irVersion: string;
  name: string;
  /** Entry subgraph name. Defaults to "root". */
  root: string;
  inputSchema?: JSONSchema;
  outputSchema?: JSONSchema;
  subgraphs: Record<string, Subgraph>;
  requiredExecutors?: string[];
  digest?: string;
}

/**
 * A pure expression in the Muxlio expression language. This is a JSON-based
 * AST evaluated by the Go interpreter. See expr.ts for helper constructors.
 */
export interface Expr {
  op: string;
  value?: unknown;
  expr?: Expr;
  name?: string;
  index?: number;
  node?: string;
  port?: string;
  left?: Expr;
  right?: Expr;
  fields?: Record<string, Expr>;
  elements?: Expr[];
}
