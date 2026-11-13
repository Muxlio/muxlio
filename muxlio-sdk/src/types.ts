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
  /** pure | idempotent-with-key | reconcilable | nonretryable; legacy aliases are supported. */
  effect?: string;
  /** Stable provider deduplication key; defaults to the activation ID. */
  idempotencyKey?: string;
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
export interface ProviderSession {
  id: string;
  executor: string;
  runnerVersion: string;
  ownerWorker: string;
  ownerEnvironment: string;
  expiresAt: string;
  recoverable: boolean;
}

/** Public operator-managed metadata. Values and credentials are never permitted. */
export interface EnvironmentManifest {
  version: '1';
  digest: string;
  protocolDigest: string;
  runners?: Record<string, string>;
  packages?: Record<string, string>;
  containers?: Record<string, string>;
  skills?: Record<string, string>;
  allowedEnvironment?: string[];
  permissions?: ('host-execution' | 'network' | 'workspace-read' | 'workspace-write')[];
}

export interface ExecutorRequirements {
  environment?: EnvironmentManifest;
  executorVersion?: string;
  contractVersion?: string;
  requiredCapabilities?: Record<string, string>;
  /** Omit for one-shot execution. Resume requires the exact returned handle. */
  session?: { mode: 'create' } | { mode: 'resume'; handle: ProviderSession };
  sessionId?: string;
  remoteTask?: { id: string; url?: string; contextId?: string };
}

/** Shared security fields inside execute params. Restrictions fail closed;
 * legacy requests without a policy use trusted native execution.
 */
export interface ExecutionSecurityParams {
  /** Names map to opaque worker-configured handles, never credential values. */
  secretRefs?: Record<string, string>;
  executionPolicy?: {
    required?: boolean;
    allowedSecrets?: string[];
    allowedCapabilities?: string[];
    trust?: 'restricted' | 'trusted-native';
    network?: 'none' | 'host';
    filesystem?: 'scratch' | 'workspace-read-only' | 'workspace-write';
  };
  isolation?: {
    mode: 'required' | 'trusted-native';
    runtime?: 'bwrap' | 'oci' | 'docker-vm' | 'macos-vm';
    /** Required for OCI and Docker VM. Use an operator-approved image. */
    image?: string;
    /** Explicit opt-in for Docker Desktop VM execution on macOS. */
    enabled?: boolean;
  };
}

/** A single node within a subgraph. */
export interface Node extends ExecutorRequirements {
  id: string;
  kind: NodeKind;

  // execute
  executor?: string;
  fallbackExecutors?: string[];
  /** Private snapshot by default; shared writes are serialized by workspace. */
  workspaceMode?: 'snapshot' | 'empty' | 'read-only' | 'shared-write';
  /** Optional external resource keys, acquired atomically with a concurrency limit of one. */
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

  // Durable waits. Input requests return the accepted JSON value on `response`.
  waitFor?: 'timer' | 'signal' | 'input';
  timerSeconds?: number;
  signalName?: string;
  requestSchema?: JSONSchema;
  requestExpiresSeconds?: number;
  requestPermissions?: string[];

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
export interface RunLimits {
  accounting?: import('./accounting.js').AccountingLimit[];
  maxAttempts?: number;
  maxActivations?: number;
  maxActiveAttempts?: number;
}

export interface Plan {
  /** Operator-authorized workspace identity, independent of the client directory. */
  workspaceId?: string;
  /** SHA-256 identity of an operator-prepared workspace tree. */
  workspaceDigest?: string;
  limits?: RunLimits;
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
