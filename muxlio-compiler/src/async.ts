// Frontend A: restricted async TypeScript compiler (M0 recording-based approach).
//
// The `run(ctx, input)` function is called once at compile time with a
// recording context. The context's methods (execute, map, branch, loop)
// return traced values. Since these are NOT thenable, `await` is a no-op
// — it returns the traced value immediately. This lets the user write
// natural `async/await` syntax while the recording happens synchronously.
// The trace of calls builds the Plan IR.
//
// This is a pragmatic M0 implementation. A full AST-analysis compiler that
// validates the restricted TypeScript subset without executing `run` is a
// later milestone (see design §4.1, §4.2, M0 tasks).

import { computeDigest } from '@muxlio/sdk';
import type {
  Binding,
  Expr,
  JSONSchema,
  Node,
  OutputDecl,
  Plan,
  Subgraph,
} from '@muxlio/sdk';

// --- Traced values ---
//
// A Traced value wraps a Binding and supports property access that creates
// field expressions. It is deliberately NOT thenable: `await tracedValue`
// resolves to the traced value itself without any async work.

function createTraced(binding: Binding): any {
  const expr = bindingToExpr(binding);
  const handler: ProxyHandler<Record<string, unknown>> = {
    get(_t, prop) {
      if (prop === '__isTraced') return true;
      if (prop === '__binding') return binding;
      if (prop === '__expr') return expr;
      if (typeof prop === 'symbol') return undefined;
      const fieldExpr: Expr = { op: 'field', expr, name: String(prop) };
      return createTraced({ expr: fieldExpr });
    },
  };
  return new Proxy({}, handler);
}

function isTraced(v: unknown): v is { __binding: Binding; __expr: Expr } {
  return v !== null && typeof v === 'object' && (v as any).__isTraced === true;
}

function bindingToExpr(b: Binding): Expr {
  if (b.expr) return b.expr;
  if (b.ref) return { op: 'ref', node: b.ref.node, port: b.ref.port } as Expr;
  throw new Error('muxlio: empty binding has no expression');
}

function toBinding(value: unknown): Binding {
  if (isTraced(value)) return value.__binding;
  return { expr: { op: 'literal', value } };
}

function toBindings(value: unknown): Record<string, Binding> {
  if (isTraced(value)) {
    return { result: value.__binding };
  }
  if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
    const result: Record<string, Binding> = {};
    for (const [key, val] of Object.entries(value as Record<string, unknown>)) {
      result[key] = toBinding(val);
    }
    return result;
  }
  return { result: { expr: { op: 'literal', value } } };
}

function toExpr(value: unknown): Expr {
  if (isTraced(value)) return value.__expr;
  return { op: 'literal', value };
}

// --- Execute result ---
//
// A plain object (not thenable, not a proxy). `await` returns it directly.
// `result.outputs.portName` returns a Traced value for that port.

function createExecuteResult(nodeId: string, outputDecls: Record<string, OutputDecl>): any {
  const outputs: Record<string, any> = {};
  for (const port of Object.keys(outputDecls)) {
    outputs[port] = createTraced({ ref: { node: nodeId, port } });
  }
  return { __isTraced: true, __nodeId: nodeId, outputs };
}

// --- Recording context ---

interface MapOpts {
  key?: (item: any) => unknown;
  concurrency?: number;
  maxItems?: number;
}

interface LoopOpts {
  initState?: unknown;
  maxIterations: number;
  stateRef?: string;
}

class RecordingContext {
  private nodes: Node[] = [];
  private subgraphs: Record<string, Subgraph> = {};
  private requiredExecutors = new Set<string>();
  private counter: { value: number };

  constructor(counter: { value: number }) {
    this.counter = counter;
  }

  private uniqueName(base: string): string {
    return `${base}__${this.counter.value++}`;
  }

  execute(id: string, opts: {
    executor: string;
    task?: string;
    params?: unknown;
    inputs?: Record<string, unknown>;
    outputs?: Record<string, OutputDecl>;
  }): any {
    this.requiredExecutors.add(opts.executor);
    const inputs: Record<string, Binding> = {};
    if (opts.inputs) {
      for (const [name, val] of Object.entries(opts.inputs)) {
        inputs[name] = toBinding(val);
      }
    }
    this.nodes.push({
      id,
      kind: 'execute',
      executor: opts.executor,
      task: opts.task,
      params: opts.params,
      inputs,
      outputs: opts.outputs,
    });
    return createExecuteResult(id, opts.outputs || {});
  }

  async map(
    id: string,
    items: unknown,
    body: (item: any, ctx: RecordingContext) => unknown | Promise<unknown>,
    opts?: MapOpts,
  ): Promise<any> {
    const itemsExpr = toExpr(items);

    let keyExpr: Expr | undefined;
    if (opts?.key) {
      const itemProxy = createTraced({ expr: { op: 'item' } });
      const keyResult = opts.key(itemProxy);
      keyExpr = toExpr(keyResult);
    }

    const bodyName = this.uniqueName(`${id}_body`);
    const bodyCtx = new RecordingContext(this.counter);
    const itemProxy = createTraced({ expr: { op: 'item' } });
    const bodyResult = await body(itemProxy, bodyCtx);
    const bodyOutput = toBindings(bodyResult);
    const collected = bodyCtx.collect();
    this.subgraphs[bodyName] = { nodes: collected.nodes, output: bodyOutput };
    this.mergeSubgraphs(collected);
    this.mergeExecutors(collected);

    this.nodes.push({
      id,
      kind: 'map',
      items: itemsExpr,
      key: keyExpr,
      concurrency: opts?.concurrency,
      maxItems: opts?.maxItems,
      bodyRef: bodyName,
    });

    return createTraced({ ref: { node: id, port: 'items' } });
  }

  async branch(
    id: string,
    condition: unknown,
    thenFn: (ctx: RecordingContext) => unknown | Promise<unknown>,
    elseFn?: (ctx: RecordingContext) => unknown | Promise<unknown>,
  ): Promise<any> {
    const condExpr = toExpr(condition);

    const thenName = this.uniqueName(`${id}_then`);
    const thenCtx = new RecordingContext(this.counter);
    const thenResult = await thenFn(thenCtx);
    const thenOutput = toBindings(thenResult);
    const thenCollected = thenCtx.collect();
    this.subgraphs[thenName] = { nodes: thenCollected.nodes, output: thenOutput };
    this.mergeSubgraphs(thenCollected);
    this.mergeExecutors(thenCollected);

    let elseRef: string | undefined;
    if (elseFn) {
      const elseName = this.uniqueName(`${id}_else`);
      const elseCtx = new RecordingContext(this.counter);
      const elseResult = await elseFn(elseCtx);
      const elseOutput = toBindings(elseResult);
      const elseCollected = elseCtx.collect();
      this.subgraphs[elseName] = { nodes: elseCollected.nodes, output: elseOutput };
      this.mergeSubgraphs(elseCollected);
      this.mergeExecutors(elseCollected);
      elseRef = elseName;
    }

    this.nodes.push({
      id,
      kind: 'branch',
      condition: condExpr,
      thenRef: thenName,
      elseRef,
    });

    return createTraced({ ref: { node: id, port: 'result' } });
  }

  async loop(
    id: string,
    whileCond: unknown,
    body: (state: any, ctx: RecordingContext) => unknown | Promise<unknown>,
    opts: LoopOpts,
  ): Promise<any> {
    const whileExpr = toExpr(whileCond);
    const initStateExpr = opts.initState !== undefined ? toExpr(opts.initState) : undefined;

    const bodyName = this.uniqueName(`${id}_body`);
    const bodyCtx = new RecordingContext(this.counter);
    const stateProxy = createTraced({ expr: { op: 'state' } });
    const bodyResult = await body(stateProxy, bodyCtx);
    const bodyOutput = toBindings(bodyResult);
    const collected = bodyCtx.collect();
    this.subgraphs[bodyName] = { nodes: collected.nodes, output: bodyOutput };
    this.mergeSubgraphs(collected);
    this.mergeExecutors(collected);

    this.nodes.push({
      id,
      kind: 'loop',
      while: whileExpr,
      initState: initStateExpr,
      maxIterations: opts.maxIterations,
      stateRef: opts.stateRef,
      bodyRef: bodyName,
    });

    return createTraced({ ref: { node: id, port: 'result' } });
  }

  project(id: string, inputs: Record<string, unknown>): any {
    const bindings: Record<string, Binding> = {};
    for (const [name, val] of Object.entries(inputs)) {
      bindings[name] = toBinding(val);
    }
    this.nodes.push({ id, kind: 'project', inputs: bindings });
    return createExecuteResult(id, {});
  }

  collect(): {
    nodes: Node[];
    subgraphs: Record<string, Subgraph>;
    requiredExecutors: Set<string>;
  } {
    return {
      nodes: this.nodes,
      subgraphs: this.subgraphs,
      requiredExecutors: this.requiredExecutors,
    };
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

// --- defineAsyncWorkflow ---

export interface AsyncWorkflowDefinition {
  name: string;
  input?: JSONSchema;
  output?: JSONSchema;
  run: (ctx: RecordingContext, input: any) => unknown | Promise<unknown>;
}

export interface CompiledWorkflow {
  plan: Plan;
}

/**
 * Define a workflow using the restricted async TypeScript frontend.
 * The `run` function is called once at compile time with a recording
 * context; calls to `ctx.execute`, `ctx.map`, etc. are traced to build
 * the Plan IR. Since traced values are not thenable, `await` is a no-op
 * and the recording happens synchronously.
 */
export async function defineAsyncWorkflow(
  def: AsyncWorkflowDefinition,
): Promise<CompiledWorkflow> {
  const counter = { value: 0 };
  const ctx = new RecordingContext(counter);
  const inputProxy = createTraced({ expr: { op: 'input' } });

  const result = await def.run(ctx, inputProxy);
  const output = toBindings(result);
  const collected = ctx.collect();

  const plan: Plan = {
    irVersion: '1.0',
    name: def.name,
    root: 'root',
    inputSchema: def.input,
    outputSchema: def.output,
    subgraphs: {
      root: { nodes: collected.nodes, output },
      ...collected.subgraphs,
    },
    requiredExecutors: [...collected.requiredExecutors],
  };

  plan.digest = computeDigest(plan);
  return { plan };
}
