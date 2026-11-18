// The symbolic builder (Frontend B) constructs a Plan IR declaratively.
// Each method on the build context registers a node in the current subgraph
// and returns typed handles that can be used as inputs to downstream nodes.
//
// This is the reference and escape-hatch frontend described in the design:
// "declarative g.map, g.branch, g.loop, g.step, and expr.*". Symbolic
// references cannot be used in ordinary JS truthiness or native array
// operations. The builder produces a well-formed Plan IR JSON that the Go
// interpreter consumes.

import { createHash } from 'node:crypto';
import { assertNoLiteralCredentials } from './security.js';
import type {
  AppendableCollection,
  Binding,
  ExecutorRequirements,
  Expr,
  JSONSchema,
  Node,
  OutputDecl,
  Plan,
  RetryPolicy,
  Subgraph,
} from './types.js';
import type { SymbolicRef } from './workflow.js';
import { authored } from './authored.js';

// --- Result types ---

/** A value that can be used as an expression operand: an Expr, a Binding (ref/expr), or a `workflow()` symbolic reference. */
export type ExprLike = Expr | Binding | SymbolicRef<unknown>;

const TRACED_EXPR = Symbol.for('muxlio.expr');

/** Result of `execute()`: provides typed access to declared output ports. */
export interface ExecuteResult {
  /** Returns a binding referencing the named output port. */
  output(port: string): Binding;
  /** Map of all declared output ports to bindings. */
  outputs: Record<string, Binding>;
}

/** Result of `map()`: provides access to the collected items array. */
export interface MapResult {
  /** Binding referencing the map node's "items" output port (the array of body outputs). */
  items(): Binding;
}

/** Result of `branch()`: provides access to the selected path's output. */
export interface BranchResult {
  /** Binding referencing the branch node's "result" output port. */
  result(): Binding;
}

/** Result of `loop()`: provides access to the final loop state. */
export interface LoopResult {
  /** Binding referencing the loop node's "result" output port. */
  result(): Binding;
}

/** Result of `project()`: provides typed access to projected values. */
export interface ProjectResult {
  output(port: string): Binding;
  outputs: Record<string, Binding>;
}

// --- Option types ---

export interface ExecuteOptions extends ExecutorRequirements {
  executor: string;
  fallbackExecutors?: string[];
  /** Private snapshot by default; shared writes are serialized by workspace. */
  workspaceMode?: 'snapshot' | 'empty' | 'read-only' | 'shared-write';
  /** Optional external resource keys, acquired atomically with a concurrency limit of one. */
  concurrencyKeys?: string[];
  task?: string;
  params?: unknown;
  inputs?: Record<string, Binding>;
  outputs?: Record<string, OutputDecl>;
  timeoutSeconds?: number;
  /** Admission timeout per attempt; omitted or zero defaults to 180 seconds. */
  queueTimeoutSeconds?: number;
  retry?: RetryPolicy;
}

export interface MapOptions {
  items: Expr;
  key?: Expr;
  concurrency?: number;
  maxItems?: number;
  appendable?: AppendableCollection;
  /** Body subgraph builder. Receives a context scoped to the current item. */
  body: (ctx: BuildContext) => Record<string, Binding>;
}

export interface BranchOptions {
  condition: Expr;
  then: (ctx: BuildContext) => Record<string, Binding>;
  else?: (ctx: BuildContext) => Record<string, Binding>;
}

export interface LoopOptions {
  while: Expr;
  initState?: Expr;
  maxIterations: number;
  /** Port name on the body output that carries the next iteration's state. */
  stateRef?: string;
  /** When maxIterations is reached while `while` is still true. Default: 'pause'. */
  onExhausted?: 'pause' | 'fail';
  body: (ctx: BuildContext) => Record<string, Binding>;
}

export interface ProjectOptions {
  inputs: Record<string, Binding>;
}

// --- Build context interface ---

/** Convert an Expr or Binding to an Expr. Bindings with `ref` become `{ op: 'ref', ... }`. */
export function toExpr(obj: ExprLike): Expr {
  const traced = obj == null ? undefined : (obj as any)[TRACED_EXPR];
  if (traced) return authored(traced as Expr);
  if (obj && 'op' in obj && (obj as Expr).op) return authored(obj as Expr);
  const binding = obj as unknown as Binding;
  if (binding.ref) return authored({ op: 'ref', node: binding.ref.node, port: binding.ref.port });
  if (binding.expr) return authored(binding.expr);
  throw new Error('muxlio: cannot convert to expression (empty binding)');
}

/** A literal JSON value expression. `undefined` has no JSON encoding. */
export function literalExpr(value: unknown): Expr {
  if (value === undefined) {
    throw new Error('muxlio: literal(undefined) is not a JSON value; use null or omit the value');
  }
  return authored({ op: 'literal', value });
}

export interface BuildContext {
  // Expression helpers (pure, no side effects)
  input(): Expr;
  inputField(name: string): Expr;
  item(): Expr;
  itemField(name: string): Expr;
  state(): Expr;
  stateField(name: string): Expr;
  field(obj: ExprLike, name: string): Expr;
  index(arr: ExprLike, idx: number): Expr;
  len(e: ExprLike): Expr;
  eq(l: ExprLike, r: ExprLike): Expr;
  ne(l: ExprLike, r: ExprLike): Expr;
  lt(l: ExprLike, r: ExprLike): Expr;
  gt(l: ExprLike, r: ExprLike): Expr;
  le(l: ExprLike, r: ExprLike): Expr;
  ge(l: ExprLike, r: ExprLike): Expr;
  and(l: ExprLike, r: ExprLike): Expr;
  or(l: ExprLike, r: ExprLike): Expr;
  not(e: ExprLike): Expr;
  literal(value: unknown): Expr;
  add(l: ExprLike, r: ExprLike): Expr;
  sub(l: ExprLike, r: ExprLike): Expr;
  mul(l: ExprLike, r: ExprLike): Expr;
  div(l: ExprLike, r: ExprLike): Expr;
  mod(l: ExprLike, r: ExprLike): Expr;

  // Node builders
  execute(id: string, opts: ExecuteOptions): ExecuteResult;
  map(id: string, opts: MapOptions): MapResult;
  branch(id: string, opts: BranchOptions): BranchResult;
  loop(id: string, opts: LoopOptions): LoopResult;
  project(id: string, opts: ProjectOptions): ProjectResult;

  // Set the current subgraph's output bindings
  output(bindings: Record<string, Binding>): void;
}

// --- IR builder implementation ---

class IRBuilder implements BuildContext {
  private nodes: Node[] = [];
  private outputBindings: Record<string, Binding> = {};
  private subgraphs: Record<string, Subgraph> = {};
  private requiredExecutors = new Set<string>();
  private counter: { value: number };

  constructor(counter: { value: number }) {
    this.counter = counter;
  }

  // Collect results for the parent builder.
  collect(): {
    nodes: Node[];
    output: Record<string, Binding>;
    subgraphs: Record<string, Subgraph>;
    requiredExecutors: Set<string>;
  } {
    return {
      nodes: this.nodes,
      output: this.outputBindings,
      subgraphs: this.subgraphs,
      requiredExecutors: this.requiredExecutors,
    };
  }

  private uniqueName(base: string): string {
    const name = `${base}__${this.counter.value}`;
    this.counter.value++;
    return name;
  }

  // --- Expression helpers ---

  input(): Expr { return authored({ op: 'input' }); }
  inputField(name: string): Expr { return authored({ op: 'field', expr: { op: 'input' }, name }); }
  item(): Expr { return authored({ op: 'item' }); }
  itemField(name: string): Expr { return authored({ op: 'field', expr: { op: 'item' }, name }); }
  state(): Expr { return authored({ op: 'state' }); }
  stateField(name: string): Expr { return authored({ op: 'field', expr: { op: 'state' }, name }); }
  field(obj: ExprLike, name: string): Expr { return authored({ op: 'field', expr: toExpr(obj), name }); }
  index(arr: ExprLike, idx: number): Expr { return authored({ op: 'index', expr: toExpr(arr), index: idx }); }
  len(e: ExprLike): Expr { return authored({ op: 'len', expr: toExpr(e) }); }
  eq(l: ExprLike, r: ExprLike): Expr { return authored({ op: 'eq', left: toExpr(l), right: toExpr(r) }); }
  ne(l: ExprLike, r: ExprLike): Expr { return authored({ op: 'ne', left: toExpr(l), right: toExpr(r) }); }
  lt(l: ExprLike, r: ExprLike): Expr { return authored({ op: 'lt', left: toExpr(l), right: toExpr(r) }); }
  gt(l: ExprLike, r: ExprLike): Expr { return authored({ op: 'gt', left: toExpr(l), right: toExpr(r) }); }
  le(l: ExprLike, r: ExprLike): Expr { return authored({ op: 'le', left: toExpr(l), right: toExpr(r) }); }
  ge(l: ExprLike, r: ExprLike): Expr { return authored({ op: 'ge', left: toExpr(l), right: toExpr(r) }); }
  and(l: ExprLike, r: ExprLike): Expr { return authored({ op: 'and', left: toExpr(l), right: toExpr(r) }); }
  or(l: ExprLike, r: ExprLike): Expr { return authored({ op: 'or', left: toExpr(l), right: toExpr(r) }); }
  not(e: ExprLike): Expr { return authored({ op: 'not', expr: toExpr(e) }); }
  literal(value: unknown): Expr { return literalExpr(value); }
  add(l: ExprLike, r: ExprLike): Expr { return authored({ op: 'add', left: toExpr(l), right: toExpr(r) }); }
  sub(l: ExprLike, r: ExprLike): Expr { return authored({ op: 'sub', left: toExpr(l), right: toExpr(r) }); }
  mul(l: ExprLike, r: ExprLike): Expr { return authored({ op: 'mul', left: toExpr(l), right: toExpr(r) }); }
  div(l: ExprLike, r: ExprLike): Expr { return authored({ op: 'div', left: toExpr(l), right: toExpr(r) }); }
  mod(l: ExprLike, r: ExprLike): Expr { return authored({ op: 'mod', left: toExpr(l), right: toExpr(r) }); }

  // --- Node builders ---

  execute(id: string, opts: ExecuteOptions): ExecuteResult {
    this.requiredExecutors.add(opts.executor);
    for (const executor of opts.fallbackExecutors ?? []) this.requiredExecutors.add(executor);
    const node: Node = {
      id,
      kind: 'execute',
      executor: opts.executor,
      environment: opts.environment,
      executorVersion: opts.executorVersion,
      contractVersion: opts.contractVersion,
      requiredCapabilities: opts.requiredCapabilities,
      session: opts.session,
      sessionId: opts.sessionId,
      remoteTask: opts.remoteTask,
      workspaceMode: opts.workspaceMode,
      concurrencyKeys: opts.concurrencyKeys,
      fallbackExecutors: opts.fallbackExecutors,
      task: opts.task,
      params: opts.params,
      inputs: opts.inputs,
      outputs: opts.outputs,
      timeoutSeconds: opts.timeoutSeconds,
      queueTimeoutSeconds: opts.queueTimeoutSeconds,
      retry: opts.retry,
    };
    this.nodes.push(node);

    const outputs: Record<string, Binding> = {};
    if (opts.outputs) {
      for (const port of Object.keys(opts.outputs)) {
        outputs[port] = authored({ ref: { node: id, port } });
      }
    }
    return {
      output: (port: string) => authored({ ref: { node: id, port } }),
      outputs,
    };
  }

  map(id: string, opts: MapOptions): MapResult {
    const bodyName = this.uniqueName(`${id}_body`);
    const bodyBuilder = new IRBuilder(this.counter);
    const bodyOutput = opts.body(bodyBuilder);
    const collected = bodyBuilder.collect();
    this.subgraphs[bodyName] = {
      nodes: collected.nodes,
      output: bodyOutput,
    };
    for (const name in collected.subgraphs) {
      this.subgraphs[name] = collected.subgraphs[name];
    }
    for (const exec of collected.requiredExecutors) {
      this.requiredExecutors.add(exec);
    }

    const node: Node = {
      id,
      kind: 'map',
      items: opts.items,
      key: opts.key,
      concurrency: opts.concurrency,
      maxItems: opts.maxItems,
      appendable: opts.appendable,
      bodyRef: bodyName,
    };
    this.nodes.push(withInputs(node, externalDependencies(id, [bodyName], this.subgraphs)));

    return {
      items: () => authored({ ref: { node: id, port: 'items' } }),
    };
  }

  branch(id: string, opts: BranchOptions): BranchResult {
    const thenName = this.uniqueName(`${id}_then`);
    const thenBuilder = new IRBuilder(this.counter);
    const thenOutput = opts.then(thenBuilder);
    const thenCollected = thenBuilder.collect();
    this.subgraphs[thenName] = { nodes: thenCollected.nodes, output: thenOutput };
    this.mergeSubgraphs(thenCollected);
    this.mergeExecutors(thenCollected);

    // An omitted else still needs a subgraph so the branch emits `result`.
    const elseName = this.uniqueName(`${id}_else`);
    const elseBuilder = new IRBuilder(this.counter);
    const elseOutput = opts.else ? opts.else(elseBuilder) : {};
    const elseCollected = elseBuilder.collect();
    this.subgraphs[elseName] = { nodes: elseCollected.nodes, output: elseOutput };
    this.mergeSubgraphs(elseCollected);
    this.mergeExecutors(elseCollected);

    const node: Node = {
      id,
      kind: 'branch',
      condition: opts.condition,
      thenRef: thenName,
      elseRef: elseName,
    };
    this.nodes.push(withInputs(node, externalDependencies(id, [thenName, elseName], this.subgraphs)));

    return { result: () => authored({ ref: { node: id, port: 'result' } }) };
  }

  loop(id: string, opts: LoopOptions): LoopResult {
    const bodyName = this.uniqueName(`${id}_body`);
    const bodyBuilder = new IRBuilder(this.counter);
    const bodyOutput = opts.body(bodyBuilder);
    const collected = bodyBuilder.collect();
    this.subgraphs[bodyName] = { nodes: collected.nodes, output: bodyOutput };
    this.mergeSubgraphs(collected);
    this.mergeExecutors(collected);

    const node: Node = {
      id,
      kind: 'loop',
      while: opts.while,
      initState: opts.initState,
      maxIterations: opts.maxIterations,
      stateRef: opts.stateRef,
      onExhausted: opts.onExhausted,
      bodyRef: bodyName,
    };
    this.nodes.push(withInputs(node, externalDependencies(id, [bodyName], this.subgraphs)));

    return { result: () => authored({ ref: { node: id, port: 'result' } }) };
  }

  project(id: string, opts: ProjectOptions): ProjectResult {
    const node: Node = {
      id,
      kind: 'project',
      inputs: opts.inputs,
    };
    this.nodes.push(node);

    const outputs: Record<string, Binding> = {};
    for (const port of Object.keys(opts.inputs)) {
      outputs[port] = authored({ ref: { node: id, port } });
    }
    return {
      output: (port: string) => authored({ ref: { node: id, port } }),
      outputs,
    };
  }

  output(bindings: Record<string, Binding>): void {
    this.outputBindings = bindings;
  }

  private mergeSubgraphs(collected: { subgraphs: Record<string, Subgraph> }) {
    for (const name in collected.subgraphs) {
      this.subgraphs[name] = collected.subgraphs[name];
    }
  }

  private mergeExecutors(collected: { requiredExecutors: Set<string> }) {
    for (const exec of collected.requiredExecutors) {
      this.requiredExecutors.add(exec);
    }
  }
}

/**
 * Bindings from a body (its nodes, nested bodies, and outputs) to nodes
 * defined outside it, one per referenced node, keyed `__after_N`. Placing
 * them on the control node makes the parent schedule those producers first,
 * which nested-scope validation requires.
 */
export function externalDependencies(
  controlId: string,
  bodyNames: string[],
  subgraphs: Record<string, Subgraph>,
): Record<string, Binding> {
  const localIds = new Set<string>([controlId]);
  const graphs: Subgraph[] = [];
  const visited = new Set<string>();
  const visit = (name: string | undefined) => {
    if (!name || visited.has(name)) return;
    visited.add(name);
    const sg = subgraphs[name];
    if (!sg) return;
    graphs.push(sg);
    for (const node of sg.nodes ?? []) {
      localIds.add(node.id);
      visit(node.bodyRef);
      visit(node.thenRef);
      visit(node.elseRef);
    }
  };
  for (const name of bodyNames) visit(name);

  const found = new Map<string, Binding>();
  const noteExpr = (expr: Expr | undefined) => {
    if (!expr) return;
    if (expr.op === 'ref' && expr.node && !localIds.has(expr.node) && !found.has(expr.node)) {
      found.set(expr.node, { ref: { node: expr.node, port: expr.port ?? 'result' } });
    }
    noteExpr(expr.expr);
    noteExpr(expr.left);
    noteExpr(expr.right);
    for (const sub of Object.values(expr.fields ?? {})) noteExpr(sub);
    for (const sub of expr.elements ?? []) noteExpr(sub);
  };
  const noteBinding = (binding: Binding | undefined) => {
    if (!binding) return;
    if (binding.ref && !localIds.has(binding.ref.node) && !found.has(binding.ref.node)) {
      found.set(binding.ref.node, binding);
    }
    noteExpr(binding.expr);
  };
  for (const sg of graphs) {
    for (const node of sg.nodes ?? []) {
      for (const binding of Object.values(node.inputs ?? {})) noteBinding(binding);
      for (const expr of [node.items, node.key, node.condition, node.while, node.initState]) noteExpr(expr);
    }
    for (const binding of Object.values(sg.output ?? {})) noteBinding(binding);
  }

  const inputs: Record<string, Binding> = {};
  let n = 0;
  for (const binding of found.values()) inputs[`__after_${n++}`] = binding;
  return inputs;
}

function withInputs(node: Node, inputs: Record<string, Binding>): Node {
  return Object.keys(inputs).length > 0 ? { ...node, inputs } : node;
}

// --- defineWorkflow ---

export interface WorkflowDefinition {
  workspaceId?: string;
  /** SHA-256 identity of an operator-prepared workspace tree. */
  workspaceDigest?: string;
  limits?: Plan['limits'];
  deadlineSeconds?: number;
  name: string;
  input?: JSONSchema;
  output?: JSONSchema;
  build: (g: BuildContext) => void;
}

export interface CompiledWorkflow {
  plan: Plan;
}

/** Define a workflow using the symbolic builder and compile it to a Plan IR. */
export function defineWorkflow(def: WorkflowDefinition): CompiledWorkflow {
  const counter = { value: 0 };
  const root = new IRBuilder(counter);
  def.build(root);
  const collected = root.collect();

  const plan: Plan = {
    irVersion: '1.0',
    name: def.name,
    workspaceId: def.workspaceId,
    workspaceDigest: def.workspaceDigest,
    deadlineSeconds: def.deadlineSeconds,
    limits: def.limits,
    root: 'root',
    inputSchema: def.input,
    outputSchema: def.output,
    subgraphs: {
      root: {
        nodes: collected.nodes,
        output: collected.output,
      },
    },
    requiredExecutors: [...collected.requiredExecutors].sort(),
  };

  // Merge nested subgraphs into the plan.
  for (const name in collected.subgraphs) {
    plan.subgraphs[name] = collected.subgraphs[name];
  }

  assertNoLiteralCredentials(plan);
  plan.digest = computeDigest(plan);
  return { plan };
}

/** Hash canonical JSON with sorted object keys and the digest field omitted. */
export function computeDigest(plan: Plan): string {
  const { digest: _, ...copy } = plan;
  const canonical = (value: unknown): string => {
    if (Array.isArray(value)) return '[' + value.map(canonical).join(',') + ']';
    if (value !== null && typeof value === 'object') {
      const object = value as Record<string, unknown>;
      return '{' + Object.keys(object).filter(key => object[key] !== undefined).sort()
        .map(key => JSON.stringify(key) + ':' + canonical(object[key])).join(',') + '}';
    }
    return JSON.stringify(value);
  };
  // Match the JSON bytes that cross the API boundary (undefined array
  // entries become null and object properties with undefined are omitted).
  const data = canonical(JSON.parse(JSON.stringify(copy)));
  return 'sha256:' + createHash('sha256').update(data).digest('hex');
}
