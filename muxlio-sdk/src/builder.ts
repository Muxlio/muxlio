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
import type {
  Binding,
  Expr,
  JSONSchema,
  Node,
  OutputDecl,
  Plan,
  RetryPolicy,
  Subgraph,
} from './types.js';

// --- Result types ---

/** A value that can be used as an expression operand: either an Expr or a Binding (ref/expr). */
export type ExprLike = Expr | Binding;

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

export interface ExecuteOptions {
  executor: string;
  fallbackExecutors?: string[];
  task?: string;
  params?: unknown;
  inputs?: Record<string, Binding>;
  outputs?: Record<string, OutputDecl>;
  timeoutSeconds?: number;
  queueTimeoutSeconds?: number;
  retry?: RetryPolicy;
}

export interface MapOptions {
  items: Expr;
  key?: Expr;
  concurrency?: number;
  maxItems?: number;
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
  body: (ctx: BuildContext) => Record<string, Binding>;
}

export interface ProjectOptions {
  inputs: Record<string, Binding>;
}

// --- Build context interface ---

/** Convert an Expr or Binding to an Expr. Bindings with `ref` become `{ op: 'ref', ... }`. */
export function toExpr(obj: ExprLike): Expr {
  if (obj && 'op' in obj && (obj as Expr).op) return obj as Expr;
  const binding = obj as unknown as Binding;
  if (binding.ref) return { op: 'ref', node: binding.ref.node, port: binding.ref.port };
  if (binding.expr) return binding.expr;
  throw new Error('muxlio: cannot convert to expression (empty binding)');
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

  input(): Expr { return { op: 'input' }; }
  inputField(name: string): Expr { return { op: 'field', expr: { op: 'input' }, name }; }
  item(): Expr { return { op: 'item' }; }
  itemField(name: string): Expr { return { op: 'field', expr: { op: 'item' }, name }; }
  state(): Expr { return { op: 'state' }; }
  stateField(name: string): Expr { return { op: 'field', expr: { op: 'state' }, name }; }
  field(obj: ExprLike, name: string): Expr { return { op: 'field', expr: toExpr(obj), name }; }
  index(arr: ExprLike, idx: number): Expr { return { op: 'index', expr: toExpr(arr), index: idx }; }
  len(e: ExprLike): Expr { return { op: 'len', expr: toExpr(e) }; }
  eq(l: ExprLike, r: ExprLike): Expr { return { op: 'eq', left: toExpr(l), right: toExpr(r) }; }
  ne(l: ExprLike, r: ExprLike): Expr { return { op: 'ne', left: toExpr(l), right: toExpr(r) }; }
  lt(l: ExprLike, r: ExprLike): Expr { return { op: 'lt', left: toExpr(l), right: toExpr(r) }; }
  gt(l: ExprLike, r: ExprLike): Expr { return { op: 'gt', left: toExpr(l), right: toExpr(r) }; }
  le(l: ExprLike, r: ExprLike): Expr { return { op: 'le', left: toExpr(l), right: toExpr(r) }; }
  ge(l: ExprLike, r: ExprLike): Expr { return { op: 'ge', left: toExpr(l), right: toExpr(r) }; }
  and(l: ExprLike, r: ExprLike): Expr { return { op: 'and', left: toExpr(l), right: toExpr(r) }; }
  or(l: ExprLike, r: ExprLike): Expr { return { op: 'or', left: toExpr(l), right: toExpr(r) }; }
  not(e: ExprLike): Expr { return { op: 'not', expr: toExpr(e) }; }
  literal(value: unknown): Expr { return { op: 'literal', value }; }
  add(l: ExprLike, r: ExprLike): Expr { return { op: 'add', left: toExpr(l), right: toExpr(r) }; }
  sub(l: ExprLike, r: ExprLike): Expr { return { op: 'sub', left: toExpr(l), right: toExpr(r) }; }
  mul(l: ExprLike, r: ExprLike): Expr { return { op: 'mul', left: toExpr(l), right: toExpr(r) }; }
  div(l: ExprLike, r: ExprLike): Expr { return { op: 'div', left: toExpr(l), right: toExpr(r) }; }
  mod(l: ExprLike, r: ExprLike): Expr { return { op: 'mod', left: toExpr(l), right: toExpr(r) }; }

  // --- Node builders ---

  execute(id: string, opts: ExecuteOptions): ExecuteResult {
    this.requiredExecutors.add(opts.executor);
    for (const executor of opts.fallbackExecutors ?? []) this.requiredExecutors.add(executor);
    const node: Node = {
      id,
      kind: 'execute',
      executor: opts.executor,
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
        outputs[port] = { ref: { node: id, port } };
      }
    }
    return {
      output: (port: string) => ({ ref: { node: id, port } }),
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
      bodyRef: bodyName,
    };
    this.nodes.push(node);

    return {
      items: () => ({ ref: { node: id, port: 'items' } }),
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

    let elseRef: string | undefined;
    if (opts.else) {
      const elseName = this.uniqueName(`${id}_else`);
      const elseBuilder = new IRBuilder(this.counter);
      const elseOutput = opts.else(elseBuilder);
      const elseCollected = elseBuilder.collect();
      this.subgraphs[elseName] = { nodes: elseCollected.nodes, output: elseOutput };
      this.mergeSubgraphs(elseCollected);
      this.mergeExecutors(elseCollected);
      elseRef = elseName;
    }

    const node: Node = {
      id,
      kind: 'branch',
      condition: opts.condition,
      thenRef: thenName,
      elseRef,
    };
    this.nodes.push(node);

    return { result: () => ({ ref: { node: id, port: 'result' } }) };
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
      bodyRef: bodyName,
    };
    this.nodes.push(node);

    return { result: () => ({ ref: { node: id, port: 'result' } }) };
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
      outputs[port] = { ref: { node: id, port } };
    }
    return {
      output: (port: string) => ({ ref: { node: id, port } }),
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

// --- defineWorkflow ---

export interface WorkflowDefinition {
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
    deadlineSeconds: def.deadlineSeconds,
    root: 'root',
    inputSchema: def.input,
    outputSchema: def.output,
    subgraphs: {
      root: {
        nodes: collected.nodes,
        output: collected.output,
      },
    },
    requiredExecutors: [...collected.requiredExecutors],
  };

  // Merge nested subgraphs into the plan.
  for (const name in collected.subgraphs) {
    plan.subgraphs[name] = collected.subgraphs[name];
  }

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
