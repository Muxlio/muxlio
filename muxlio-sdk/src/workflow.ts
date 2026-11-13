// workflow.ts — High-level ergonomic TypeScript SDK for Muxlio.
//
// This module provides the primary `workflow`, `task`, and `artifact` authoring
// API. It compiles to Plan IR JSON consumed by the Go coordinator.
//
// Design contract:
//   - The `build` callback is called exactly once at compile time.
//   - `step`, `each`, and `repeatUntil` register IR nodes and return typed
//     symbolic proxies. They must not execute agents, commands, or I/O.
//   - `repeatUntil` redirects step() calls during attempt()/otherwise() to a
//     child body subgraph via a mutable "active target" on the context.
//
// Usage:
//   import { workflow, task, artifact } from '@muxlio/sdk';
//   import { z } from 'zod';
//
//   export default workflow('my-workflow', {
//     input: z.object({ id: z.string() }),
//     build: ({ input, step, each, repeatUntil }) => {
//       const result = step('run', myTask, { id: input.id });
//       return { result: result.output };
//     },
//   });

import { z } from 'zod';
import type { ExecutorRequirements, Binding, Expr, JSONSchema, OutputDecl, Plan, RetryPolicy, Subgraph } from './types.js';
import { computeDigest } from './builder.js';
import { assertNoLiteralCredentials } from './security.js';

/** Opaque compile-time symbolic reference (property access builds Plan IR expressions). */
export type SymbolicRef<T = unknown> = T & { readonly __muxlioSymbolic: unique symbol };

type TaskOutputPortType<T> =
  T extends ArtifactDecl ? SymbolicRef<ArtifactDecl>
  : T extends z.ZodTypeAny ? SymbolicRef<z.infer<T>>
  : T extends OutputDecl ? SymbolicRef<unknown>
  : SymbolicRef<unknown>;

// ---------------------------------------------------------------------------
// Zod → JSON Schema conversion (subset required by the spec)
// ---------------------------------------------------------------------------

/** Convert a Zod schema to a plain JSON Schema object for the Plan IR. */
export function zodToJsonSchema(schema: z.ZodTypeAny): JSONSchema {
  return zodTypeToJsonSchema(schema);
}

function zodTypeToJsonSchema(schema: z.ZodTypeAny): JSONSchema {
  if (schema instanceof z.ZodString) {
    const out: JSONSchema = { type: 'string' };
    const checks = (schema as any)._def?.checks ?? [];
    const patterns: string[] = [];
    for (const check of checks) {
      if (check.kind === 'min') {
        out['minLength'] = Math.max((out['minLength'] as number | undefined) ?? 0, check.value);
      }
      if (check.kind === 'max') {
        const current = out['maxLength'] as number | undefined;
        out['maxLength'] = current == null ? check.value : Math.min(current, check.value);
      }
      if (check.kind === 'length') {
        const minLength = out['minLength'] as number | undefined;
        const maxLength = out['maxLength'] as number | undefined;
        out['minLength'] = minLength == null ? check.value : Math.max(minLength, check.value);
        out['maxLength'] = maxLength == null ? check.value : Math.min(maxLength, check.value);
      }
      if (check.kind === 'regex' && check.regex) {
        let pattern = (check.regex as RegExp).source;
        const flags = (check.regex as RegExp).flags;
        const goFlags = `${flags.includes('i') ? 'i' : ''}${flags.includes('m') ? 'm' : ''}${flags.includes('s') ? 's' : ''}`;
        if (goFlags) pattern = `(?${goFlags})${pattern}`;
        patterns.push(pattern);
      }
    }
    if (patterns.length === 1) out['pattern'] = patterns[0];
    if (patterns.length > 1) out['allOf'] = patterns.map(pattern => ({ pattern }));
    return out;
  }
  if (schema instanceof z.ZodNumber) {
    const out: JSONSchema = { type: 'number' };
    const checks = (schema as any)._def?.checks ?? [];
    for (const check of checks) {
      if (check.kind === 'min') {
        const current = out['minimum'] ?? out['exclusiveMinimum'];
        if (current == null || check.value > current ||
            (check.value === current && !check.inclusive && out['minimum'] != null)) {
          delete out['minimum'];
          delete out['exclusiveMinimum'];
          if (check.inclusive) out['minimum'] = check.value;
          else out['exclusiveMinimum'] = check.value;
        }
      }
      if (check.kind === 'max') {
        const current = out['maximum'] ?? out['exclusiveMaximum'];
        if (current == null || check.value < current ||
            (check.value === current && !check.inclusive && out['maximum'] != null)) {
          delete out['maximum'];
          delete out['exclusiveMaximum'];
          if (check.inclusive) out['maximum'] = check.value;
          else out['exclusiveMaximum'] = check.value;
        }
      }
      if (check.kind === 'int') out['type'] = 'integer';
      if (check.kind === 'multipleOf') out['multipleOf'] = check.value;
    }
    return out;
  }
  if (schema instanceof z.ZodBoolean) {
    return { type: 'boolean' };
  }
  if (schema instanceof z.ZodNull) {
    return { type: 'null' };
  }
  if (schema instanceof z.ZodUndefined) {
    return {};
  }
  if (schema instanceof z.ZodOptional) {
    return zodTypeToJsonSchema((schema as any)._def.innerType);
  }
  if (schema instanceof z.ZodNullable) {
    const inner = zodTypeToJsonSchema((schema as any)._def.innerType);
    return { anyOf: [inner, { type: 'null' }] };
  }
  if (schema instanceof z.ZodArray) {
    const def = (schema as any)._def;
    const out: JSONSchema = {
      type: 'array',
      items: zodTypeToJsonSchema(def.type),
    };
    if (def.minLength?.value != null) {
      out['minItems'] = def.minLength.value;
    }
    if (def.maxLength?.value != null) {
      out['maxItems'] = def.maxLength.value;
    }
    const checks = def.checks ?? [];
    for (const check of checks) {
      if (check.kind === 'min') out['minItems'] = check.value;
      if (check.kind === 'max') out['maxItems'] = check.value;
    }
    return out;
  }
  if (schema instanceof z.ZodObject) {
    const shape = (schema as any)._def.shape();
    const properties: Record<string, JSONSchema> = {};
    const required: string[] = [];
    for (const [key, val] of Object.entries(shape)) {
      const field = val as z.ZodTypeAny;
      properties[key] = zodTypeToJsonSchema(field);
      if (!(field instanceof z.ZodOptional) && !(field instanceof z.ZodNullable) && !(field instanceof z.ZodDefault)) {
        required.push(key);
      }
    }
    const out: JSONSchema = { type: 'object', properties };
    if (required.length > 0) out['required'] = required;
    return out;
  }
  if (schema instanceof z.ZodUnion) {
    const options = (schema as any)._def.options as z.ZodTypeAny[];
    return { anyOf: options.map(zodTypeToJsonSchema) };
  }
  if (schema instanceof z.ZodDiscriminatedUnion) {
    const options = (schema as any)._def.options as z.ZodTypeAny[];
    return { anyOf: options.map(zodTypeToJsonSchema) };
  }
  if (schema instanceof z.ZodEffects) {
    return zodTypeToJsonSchema((schema as any)._def.schema);
  }
  if (schema instanceof z.ZodIntersection) {
    const def = (schema as any)._def;
    return {
      allOf: [zodTypeToJsonSchema(def.left), zodTypeToJsonSchema(def.right)],
    };
  }
  if (schema instanceof z.ZodRecord) {
    const def = (schema as any)._def;
    return {
      type: 'object',
      additionalProperties: zodTypeToJsonSchema(def.valueType),
    };
  }
  if (schema instanceof z.ZodTuple) {
    const def = (schema as any)._def;
    const items = (def.items ?? []) as z.ZodTypeAny[];
    const out: JSONSchema = {
      type: 'array',
      items: items.map(zodTypeToJsonSchema),
      minItems: items.length,
    };
    if (def.rest) {
      out['additionalItems'] = zodTypeToJsonSchema(def.rest);
    } else {
      out['maxItems'] = items.length;
    }
    return out;
  }
  if (schema instanceof z.ZodLiteral) {
    return { const: (schema as any)._def.value };
  }
  if (schema instanceof z.ZodEnum) {
    return { enum: (schema as any)._def.values };
  }
  if (schema instanceof z.ZodDefault) {
    const inner = zodTypeToJsonSchema((schema as any)._def.innerType);
    const defValue = (schema as any)._def.defaultValue;
    const value = typeof defValue === 'function' ? defValue() : defValue;
    return { ...inner, default: value };
  }
  // Fallback: allow any value
  return {};
}

// ---------------------------------------------------------------------------
// Artifact constructor
// ---------------------------------------------------------------------------

/** An artifact reference used in task output declarations. */
export interface ArtifactDecl {
  readonly __isArtifact: true;
  readonly mime: string;
  readonly path: string;
}

function isArtifactDecl(v: unknown): v is ArtifactDecl {
  return typeof v === 'object' && v !== null && (v as any).__isArtifact === true;
}

/** A plain OutputDecl has a `mime` string and no `__isArtifact` marker. */
function isOutputDecl(v: unknown): v is OutputDecl {
  return typeof v === 'object' && v !== null &&
    typeof (v as any).mime === 'string' &&
    (v as any).__isArtifact !== true &&
    typeof (v as any)._def === 'undefined'; // not a Zod schema
}

/**
 * Artifact constructors for task output declarations.
 *
 * @example
 * outputs: { report: artifact.markdown('report.md') }
 */
export const artifact = {
  /** Declares a markdown text artifact output written to a given path. */
  markdown(path: string): ArtifactDecl {
    return { __isArtifact: true, mime: 'text/markdown', path };
  },
  /** Declares a plain text artifact output. */
  text(path: string): ArtifactDecl {
    return { __isArtifact: true, mime: 'text/plain', path };
  },
  /** Declares a JSON artifact output. */
  json(path: string): ArtifactDecl {
    return { __isArtifact: true, mime: 'application/json', path };
  },
  /** Generic artifact declaration from explicit MIME type and path. */
  of(mime: string, path: string): ArtifactDecl {
    return { __isArtifact: true, mime, path };
  },
};

// ---------------------------------------------------------------------------
// Task definition
// ---------------------------------------------------------------------------

/**
 * Output schema for a task. Each port is one of:
 *   - `artifact.markdown(path)` / `artifact.json(path)` etc. — a file artifact
 *   - `{ mime: string, path?: string }` — a plain OutputDecl
 *   - a Zod type — compiled to `{ mime: 'application/json' }`
 */
export type TaskOutputSchema = Record<string, z.ZodTypeAny | ArtifactDecl | OutputDecl>;

/** Options for defining a task. */
export interface TaskDefinition<TOutputs extends TaskOutputSchema = TaskOutputSchema> extends ExecutorRequirements {
  /** Primary executor capability name, e.g. `'agent/kiro'`. */
  executor: string;
  /** Fallback executors tried on retry. */
  fallbackExecutors?: string[];
  /** Private snapshot by default; shared writes are serialized by workspace. */
  workspaceMode?: 'snapshot' | 'empty' | 'read-only' | 'shared-write';
  /** Optional external resource keys, acquired atomically with a concurrency limit of one. */
  concurrencyKeys?: string[];
  /** Natural-language task description / prompt. */
  task?: string;
  /** Arbitrary JSON params forwarded to the executor. */
  params?: unknown;
  /** Output port declarations: Zod types for JSON values, ArtifactDecl for files.
   *  Command tasks include standard process outputs automatically. */
  outputs?: TOutputs;
  /** Retry and effect policy. */
  timeoutSeconds?: number;
  queueTimeoutSeconds?: number;
  retry?: RetryPolicy;
}

/** A compiled task definition. Calling `task(...)` creates one of these. */
export interface TaskDef<TOutputs extends TaskOutputSchema = TaskOutputSchema> {
  readonly __isTaskDef: true;
  readonly def: TaskDefinition<TOutputs>;
}

const commandOutputs = {
  passed: z.boolean(),
  exitCode: z.number().nullable(),
  signal: z.string().nullable(),
  stdout: z.string(),
  stderr: z.string(),
  durationMs: z.number(),
};

/**
 * Define a reusable task. Tasks declare executor, prompt, output schema, and
 * retry policy, but do not schedule work. Command tasks infer their standard
 * process outputs; explicit outputs add to or override those declarations.
 * Use `step(id, taskDef, inputs)`
 * inside a `workflow` build function to schedule them.
 *
 * @example
 * const review = task({
 *   executor: 'agent/codex',
 *   task: 'Review the feature implementation.',
 *   outputs: { report: artifact.markdown('review.md') },
 *   retry: { maxAttempts: 3, effect: 'retryable' },
 * });
 */
export function task<TOutputs extends TaskOutputSchema = {}>(
  def: TaskDefinition<TOutputs> & { executor: 'command' },
): TaskDef<Omit<typeof commandOutputs, keyof TOutputs> & TOutputs>;
export function task<TOutputs extends TaskOutputSchema = {}>(
  def: TaskDefinition<TOutputs>,
): TaskDef<TOutputs>;
export function task(def: TaskDefinition): TaskDef {
  if (def.executor === 'command') {
    return { __isTaskDef: true, def: { ...def, outputs: { ...commandOutputs, ...def.outputs } } };
  }
  return { __isTaskDef: true, def };
}

// ---------------------------------------------------------------------------
// Symbolic reference system (proxy-based traced values)
// ---------------------------------------------------------------------------

const SYM_IS_TRACED = Symbol('muxlio.isTraced');
const SYM_BINDING = Symbol('muxlio.binding');
const SYM_EXPR = Symbol('muxlio.expr');

function createTracedRef(binding: Binding): any {
  const expr = bindingToExpr(binding);
  const handler: ProxyHandler<object> = {
    get(_target, prop) {
      if (prop === SYM_IS_TRACED) return true;
      if (prop === SYM_BINDING) return binding;
      if (prop === SYM_EXPR) return expr;
      if (typeof prop === 'symbol') return undefined;
      const name = String(prop);
      const fieldExpr: Expr = { op: 'field', expr, name };
      return createTracedRef({ expr: fieldExpr });
    },
  };
  return new Proxy({} as object, handler);
}

function isTraced(v: unknown): boolean {
  return typeof v === 'object' && v !== null && (v as any)[SYM_IS_TRACED] === true;
}

function getBinding(v: unknown): Binding {
  if (!isTraced(v)) throw new Error('muxlio: value is not a symbolic reference');
  return (v as any)[SYM_BINDING] as Binding;
}

function getExpr(v: unknown): Expr {
  if (!isTraced(v)) throw new Error('muxlio: value is not a symbolic reference');
  return (v as any)[SYM_EXPR] as Expr;
}

function bindingToExpr(b: Binding): Expr {
  if (b.expr) return b.expr;
  if (b.ref) return { op: 'ref', node: b.ref.node, port: b.ref.port };
  throw new Error('muxlio: empty binding');
}

function toBinding(value: unknown): Binding {
  if (isTraced(value)) return getBinding(value);
  if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
    const fields: Record<string, Expr> = {};
    for (const [key, val] of Object.entries(value as Record<string, unknown>)) {
      fields[key] = toExpr(val);
    }
    return { expr: { op: 'object', fields } as Expr };
  }
  if (Array.isArray(value)) {
    const elements = value.map((v) => toExpr(v));
    return { expr: { op: 'array', elements } as Expr };
  }
  return { expr: { op: 'literal', value } };
}

function toExpr(value: unknown): Expr {
  if (isTraced(value)) return getExpr(value);
  if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
    const fields: Record<string, Expr> = {};
    for (const [key, val] of Object.entries(value as Record<string, unknown>)) {
      fields[key] = toExpr(val);
    }
    return { op: 'object', fields } as Expr;
  }
  if (Array.isArray(value)) {
    return { op: 'array', elements: value.map((v) => toExpr(v)) } as Expr;
  }
  return { op: 'literal', value };
}

// ---------------------------------------------------------------------------
// Output proxy
// ---------------------------------------------------------------------------

/**
 * Create a proxy for a step's output ports. Accessing `.portName` returns a
 * symbolic reference to that port. Accessing `.portName.subfield` chains
 * field expressions.
 */
function createStepProxy(nodeId: string, ports: string[]): any {
  const portSet = new Set(ports);
  const portBindings: Record<string, Binding> = {};
  for (const port of ports) {
    portBindings[port] = { ref: { node: nodeId, port } };
  }
  // Control-flow `after: stepRef` uses the whole step result; depend on the
  // step completing via any declared output port (or a synthetic result ref).
  const completionBinding: Binding =
    ports.length > 0
      ? portBindings[ports[0]!]
      : { ref: { node: nodeId, port: 'result' } };
  const handler: ProxyHandler<object> = {
    get(_target, prop) {
      if (prop === SYM_IS_TRACED) return true;
      if (prop === SYM_BINDING) return completionBinding;
      if (typeof prop === 'symbol') return undefined;
      const name = String(prop);
      if (!portSet.has(name)) {
        throw new Error(
          `muxlio: output port '${name}' is not declared on step '${nodeId}' (declared: ${ports.join(', ')})`,
        );
      }
      return createTracedRef(portBindings[name]);
    },
  };
  return new Proxy({} as object, handler);
}

function refExpr(node: string, port: string): Expr {
  return { op: 'ref', node, port };
}

/** Fix runs only when the attempt failed and another verify attempt remains. */
function shouldRunFixExpr(captureId: string, maxAttempts: number): Expr {
  return {
    op: 'and',
    left: { op: 'not', expr: refExpr(captureId, 'passed') },
    right: {
      op: 'lt',
      left: {
        op: 'add',
        left: { op: 'field', expr: { op: 'state' }, name: 'attempts' },
        right: { op: 'literal', value: 1 },
      },
      right: { op: 'literal', value: maxAttempts },
    },
  };
}

// ---------------------------------------------------------------------------
// Internal IR node types
// ---------------------------------------------------------------------------

// Matches plan.Node but avoids circular import; all fields are optional JSON-safe.
interface IRNode extends ExecutorRequirements {
  id: string;
  kind: string;
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
  timeoutSeconds?: number;
  queueTimeoutSeconds?: number;
  retry?: RetryPolicy;
  // map
  items?: Expr;
  key?: Expr;
  concurrency?: number;
  maxItems?: number;
  bodyRef?: string;
  // loop
  while?: Expr;
  initState?: Expr;
  maxIterations?: number;
  stateRef?: string;
  onExhausted?: 'pause' | 'fail';
  // branch
  condition?: Expr;
  thenRef?: string;
  elseRef?: string;
  // project
  // (uses inputs)
}

// ---------------------------------------------------------------------------
// Build context types (public API surface)
// ---------------------------------------------------------------------------

/** Maps a TaskOutputSchema to the TypeScript type of its symbolic output proxy. */
export type StepResult<TOutputs extends TaskOutputSchema> = {
  readonly [K in keyof TOutputs]: TaskOutputPortType<TOutputs[K]>;
};

/** Symbolic collection reference returned by `each()`. */
export type EachResult = unknown;

/** Handle returned by `repeatUntil()`. */
export interface RepeatUntilResult<TOutputs extends TaskOutputSchema> {
  /** Symbolic reference to the successful attempt's typed outputs. */
  readonly value: StepResult<TOutputs>;
  /** Symbolic reference to the number of completed loop attempts. */
  readonly attempts: unknown;
  /** Loop node id (for `step(..., { requiresPassed })`). */
  readonly loopId: string;
}

/** Options for `step()`. */
export interface StepOptions {
  /** Control dependencies: this step runs after the listed steps even if
   *  it does not consume their outputs. */
  after?: unknown | unknown[];
  /** Only schedule this step when the given loop exited with `passed: true`. */
  requiresPassed?: RepeatUntilResult<TaskOutputSchema>;
}

/** Options for `each()`. */
export interface EachOptions {
  /** Function that extracts a stable string key from each item. */
  key: (item: any) => unknown;
  /** Maximum concurrency. `1` means fully serial. */
  concurrency?: number;
  /** Maximum collection size cap. */
  maxItems?: number;
}

/** Options for `repeatUntil()`. */
export interface RepeatUntilOptions<TOutputs extends TaskOutputSchema> {
  /** Build-time template for the attempt step(s). Called once during compilation. */
  attempt: () => StepResult<TOutputs>;
  /**
   * Predicate compiled to a runtime boolean expression. Receives a symbolic
   * proxy for the attempt's outputs. Must return a symbolic expression —
   * do not use native JS truthiness on the proxy.
   */
  until: (result: StepResult<TOutputs>) => unknown;
  /**
   * Build-time template for the fix/recovery step(s). Called once during
   * compilation with a symbolic proxy for the previous attempt's result.
   */
  otherwise: (result: StepResult<TOutputs>) => StepResult<any>;
  /** Maximum number of attempt runs (including the first). Default: 10. */
  maxAttempts?: number;
  /** What to do on exhaustion. 'pause': persist run as resumable. Default: 'pause'. */
  onExhausted?: 'pause' | 'fail';
}

/**
 * Context passed to the `build` function of a workflow.
 */
export interface WorkflowBuildContext<TInputSchema extends z.ZodTypeAny = z.ZodTypeAny> {
  /** Symbolic reference to the workflow input. Property access yields field expressions. */
  readonly input: SymbolicRef<z.infer<TInputSchema>>;

  step<TOutputs extends TaskOutputSchema>(
    id: string,
    taskDef: TaskDef<TOutputs>,
    inputs: Record<string, unknown>,
    options?: StepOptions,
  ): StepResult<TOutputs>;

  each<TItem>(
    collection: readonly TItem[],
    options: EachOptions,
    body: (item: SymbolicRef<TItem>, ctx: WorkflowBuildContext<TInputSchema>) => Record<string, unknown> | unknown,
  ): EachResult;

  repeatUntil<TOutputs extends TaskOutputSchema>(
    id: string,
    options: RepeatUntilOptions<TOutputs>,
  ): RepeatUntilResult<TOutputs>;
}

// ---------------------------------------------------------------------------
// Compiler state
// ---------------------------------------------------------------------------

/**
 * Mutable compilation state shared across nested scopes.
 * `activeNodes` is the currently active node list (root or a body subgraph).
 */
interface CompileState {
  counter: { value: number };
  subgraphs: Record<string, Subgraph>;
  requiredExecutors: Set<string>;
  activeNodes: IRNode[]; // points to the current scope's node list
}

function uniqueName(state: CompileState, base: string): string {
  return `${base}__${state.counter.value++}`;
}

// ---------------------------------------------------------------------------
// Core step compilation (shared by all scopes)
// ---------------------------------------------------------------------------

function compileStep<TOutputs extends TaskOutputSchema>(
  state: CompileState,
  id: string,
  taskDef: TaskDef<TOutputs>,
  inputs: Record<string, unknown>,
  options: StepOptions | undefined,
): StepResult<TOutputs> {
  if (options?.requiresPassed) {
    return compileStepRequiresPassed(state, id, taskDef, inputs, options);
  }
  return compileStepExecute(state, id, taskDef, inputs, options);
}

function compileStepRequiresPassed<TOutputs extends TaskOutputSchema>(
  state: CompileState,
  id: string,
  taskDef: TaskDef<TOutputs>,
  inputs: Record<string, unknown>,
  options: StepOptions,
): StepResult<TOutputs> {
  const loopId = options.requiresPassed!.loopId;
  const thenName = uniqueName(state, `${id}_then`);
  const elseName = uniqueName(state, `${id}_else`);
  const thenNodes: IRNode[] = [];
  const savedActive = state.activeNodes;
  state.activeNodes = thenNodes;
  const inner = compileStepExecute(state, id, taskDef, inputs, { after: options.after });
  state.activeNodes = savedActive;

  const outputPorts = Object.keys(taskDef.def.outputs ?? {});
  const thenOutput: Record<string, Binding> = {};
  for (const port of outputPorts) {
    thenOutput[port] = { ref: { node: id, port } };
  }
  state.subgraphs[thenName] = { nodes: thenNodes as any, output: thenOutput };
  state.subgraphs[elseName] = { nodes: [], output: {} };

  const branchId = uniqueName(state, `${id}_if_passed`);
  state.activeNodes.push({
    id: branchId,
    kind: 'branch',
    condition: {
      op: 'field',
      expr: refExpr(loopId, 'result'),
      name: 'passed',
    },
    thenRef: thenName,
    elseRef: elseName,
  });

  const branchPorts = outputPorts.length > 0 ? outputPorts : ['result'];
  const resultRef: Expr = { op: 'ref', node: branchId, port: 'result' };
  const handler: ProxyHandler<object> = {
    get(_t, prop) {
      if (prop === SYM_IS_TRACED) return true;
      if (typeof prop === 'symbol') return undefined;
      const name = String(prop);
      if (!branchPorts.includes(name) && !(outputPorts.length === 0 && name === 'result')) {
        throw new Error(
          `muxlio: output port '${name}' is not declared on step '${id}' (declared: ${branchPorts.join(', ')})`,
        );
      }
      if (outputPorts.length <= 1) {
        return createTracedRef({ ref: { node: branchId, port: 'result' } });
      }
      return createTracedRef({
        expr: { op: 'field', expr: resultRef, name },
      });
    },
  };
  void inner;
  return new Proxy({} as object, handler) as StepResult<TOutputs>;
}

function compileStepExecute<TOutputs extends TaskOutputSchema>(
  state: CompileState,
  id: string,
  taskDef: TaskDef<TOutputs>,
  inputs: Record<string, unknown>,
  options: StepOptions | undefined,
): StepResult<TOutputs> {
  const def = taskDef.def;
  state.requiredExecutors.add(def.executor);
  for (const fe of def.fallbackExecutors ?? []) state.requiredExecutors.add(fe);

  const inputBindings: Record<string, Binding> = {};
  for (const [key, val] of Object.entries(inputs)) {
    inputBindings[key] = toBinding(val);
  }

  // Add control-flow dependencies from `after`.
  if (options?.after) {
    const afters = Array.isArray(options.after) ? options.after : [options.after];
    let n = 0;
    for (const dep of afters) {
      if (isTraced(dep)) {
        inputBindings[`__after_${n++}`] = getBinding(dep);
      }
    }
  }

  const outputs: Record<string, OutputDecl> = {};
  if (def.outputs) {
    for (const [port, decl] of Object.entries(def.outputs)) {
      if (isArtifactDecl(decl)) {
        outputs[port] = { mime: decl.mime, path: decl.path };
      } else if (isOutputDecl(decl)) {
        outputs[port] = { mime: (decl as OutputDecl).mime, path: (decl as OutputDecl).path };
      } else {
        outputs[port] = {
          mime: 'application/json',
          schema: zodToJsonSchema(decl as z.ZodTypeAny),
        };
      }
    }
  }

  const node: IRNode = {
    id,
    kind: 'execute',
    executor: def.executor,
    environment: def.environment,
    executorVersion: def.executorVersion,
    contractVersion: def.contractVersion,
    requiredCapabilities: def.requiredCapabilities,
    session: def.session,
    sessionId: def.sessionId,
    remoteTask: def.remoteTask,
    workspaceMode: def.workspaceMode,
    concurrencyKeys: def.concurrencyKeys,
    task: def.task,
    params: def.params !== undefined ? def.params : undefined,
    inputs: Object.keys(inputBindings).length > 0 ? inputBindings : undefined,
    outputs: Object.keys(outputs).length > 0 ? outputs : undefined,
    timeoutSeconds: def.timeoutSeconds,
    queueTimeoutSeconds: def.queueTimeoutSeconds,
    retry: def.retry,
  };
  if (def.fallbackExecutors?.length) node.fallbackExecutors = def.fallbackExecutors;

  state.activeNodes.push(node);
  return createStepProxy(id, Object.keys(outputs)) as StepResult<TOutputs>;
}

// ---------------------------------------------------------------------------
// Build context implementation
// ---------------------------------------------------------------------------

function makeContext(state: CompileState, inputExpr: Expr): WorkflowBuildContext {
  const inputProxy = createTracedRef({ expr: inputExpr }) as SymbolicRef<unknown>;

  const ctx: WorkflowBuildContext = {
    input: inputProxy as WorkflowBuildContext['input'],

    step<TOutputs extends TaskOutputSchema>(
      id: string,
      taskDef: TaskDef<TOutputs>,
      inputs: Record<string, unknown>,
      options?: StepOptions,
    ): StepResult<TOutputs> {
      return compileStep(state, id, taskDef, inputs, options);
    },

    each(
      collection: unknown,
      options: EachOptions,
      body: (item: any, ctx: WorkflowBuildContext) => Record<string, unknown> | unknown,
    ): EachResult {
      const mapId = uniqueName(state, 'each');
      const itemsExpr = toExpr(collection);

      const itemProxy = createTracedRef({ expr: { op: 'item' } });
      const keyResult = options.key(itemProxy);
      const keyExpr = toExpr(keyResult);

      // Build the body subgraph in its own node list.
      const bodyName = uniqueName(state, `${mapId}_body`);
      const bodyNodes: IRNode[] = [];
      const savedActive = state.activeNodes;
      state.activeNodes = bodyNodes;

      // Body context: item as input
      const bodyCtx = makeContext(state, { op: 'item' });
      const bodyResult = body(itemProxy, bodyCtx);
      const bodyOutput = resultToBindings(bodyResult);

      state.activeNodes = savedActive;

      state.subgraphs[bodyName] = { nodes: bodyNodes as any, output: bodyOutput };

      state.activeNodes.push({
        id: mapId,
        kind: 'map',
        items: itemsExpr,
        key: keyExpr,
        concurrency: options.concurrency,
        maxItems: options.maxItems,
        bodyRef: bodyName,
      });

      return createTracedRef({ ref: { node: mapId, port: 'items' } }) as EachResult;
    },

    repeatUntil<TOutputs extends TaskOutputSchema>(
      loopId: string,
      opts: RepeatUntilOptions<TOutputs>,
    ): RepeatUntilResult<TOutputs> {
      return compileRepeatUntil(state, loopId, opts);
    },
  };

  return ctx;
}

// ---------------------------------------------------------------------------
// repeatUntil compilation
// ---------------------------------------------------------------------------

/**
 * Compiles `repeatUntil` to a `loop` IR node.
 *
 * Loop state: `{ passed: bool, attempts: number, <outputPort>: value, ... }`
 *
 * Each iteration of the body subgraph:
 *   1. Runs the attempt step(s).
 *   2. Evaluates the `until` predicate to obtain `passed`.
 *   3. If not passed, runs the fix/recovery step(s) as a branch.
 *   4. Projects the new loop state including incremented `attempts`.
 *
 * The `while` condition on the loop node is `!state.passed`, so the loop
 * continues until an attempt succeeds (passed=true) or maxAttempts is reached.
 *
 * Control flow for fix: we cannot use a branch node in the body because the
 * Plan IR branch produces a subgraph-scoped result, and the loop state update
 * still needs both paths to produce the same state shape. Instead, we use a
 * project node that selects values based on the passed flag.
 *
 * Actually, to keep the IR clean, we DO use a branch node whose both arms
 * produce a compatible output. The fix branch runs the fix steps and outputs
 * a sentinel; the no-op branch outputs the current state. Both converge in the
 * body subgraph output.
 *
 * Body subgraph node sequence:
 *   <attempt_nodes...>
 *   __<id>_fix_branch  (branch: thenRef=noop, elseRef=fix)
 *   __<id>_state       (project: passed, attempts+1, value_<ports>...)
 *
 * The body output:
 *   __loopState → { ref: __<id>_state }  (carries state to next iteration)
 *   passed      → __<id>_state.passed
 *   attempts    → __<id>_state.attempts
 *   value_<p>   → __<id>_state.value_<p>
 */
function compileRepeatUntil<TOutputs extends TaskOutputSchema>(
  state: CompileState,
  loopId: string,
  opts: RepeatUntilOptions<TOutputs>,
): RepeatUntilResult<TOutputs> {
  const maxIter = opts.maxAttempts ?? 10;
  const bodyName = uniqueName(state, `${loopId}_body`);

  // Body node list (all step() calls during attempt/otherwise go here).
  const bodyNodes: IRNode[] = [];
  const savedActive = state.activeNodes;
  state.activeNodes = bodyNodes;

  // ── 1. Record attempt steps ──────────────────────────────────────────────
  const attemptResult = opts.attempt();
  // The last node added is the primary attempt node.
  const attemptNode = bodyNodes[bodyNodes.length - 1];
  if (!attemptNode) {
    throw new Error(`muxlio: repeatUntil '${loopId}': attempt() must call step() at least once`);
  }
  const attemptPorts = Object.keys(attemptNode.outputs ?? {});

  const captureId = `${loopId}__cap`;

  // ── 2. Derive `passed` expression from `until(attemptResult)` ───────────
  const passedExpr = toExpr(opts.until(attemptResult as StepResult<TOutputs>));

  // ── 3. Record otherwise (fix) steps — wired to this iteration's capture ──
  const fixBranchName = uniqueName(state, `${loopId}_fix`);
  const noopBranchName = uniqueName(state, `${loopId}_noop`);
  const fixNodes: IRNode[] = [];
  state.activeNodes = fixNodes;

  const attemptResultProxy = createCaptureResultProxy(captureId, attemptPorts);
  opts.otherwise(attemptResultProxy as StepResult<TOutputs>);

  state.activeNodes = bodyNodes;

  const fixOutput: Record<string, Binding> = { result: { expr: { op: 'literal', value: null } } };
  state.subgraphs[fixBranchName] = { nodes: fixNodes as any, output: fixOutput };

  state.subgraphs[noopBranchName] = {
    nodes: [],
    output: { result: { expr: { op: 'literal', value: null } } },
  };

  // ── 4. Capture attempt outputs for loop state / fix inputs ───────────────
  const captureInputs: Record<string, Binding> = {
    passed: { expr: passedExpr },
  };
  // Also capture all attempt output ports so they're available in next iteration.
  for (const port of attemptPorts) {
    captureInputs[`value_${port}`] = { ref: { node: attemptNode.id, port } };
  }
  bodyNodes.push({
    id: captureId,
    kind: 'project',
    inputs: captureInputs,
  });

  // ── 5. Branch: run fix only when failed and attempts remain ──────────────
  const branchId = `${loopId}__branch`;
  bodyNodes.push({
    id: branchId,
    kind: 'branch',
    condition: shouldRunFixExpr(captureId, maxIter),
    thenRef: fixBranchName,
    elseRef: noopBranchName,
  });

  // ── 6. Add state project node ─────────────────────────────────────────────
  const stateId = `${loopId}__state`;
  const stateInputs: Record<string, Binding> = {
    passed: { ref: { node: captureId, port: 'passed' } },
    attempts: {
      expr: {
        op: 'add',
        left: { op: 'field', expr: { op: 'state' }, name: 'attempts' },
        right: { op: 'literal', value: 1 },
      },
    },
    __after_branch: { ref: { node: branchId, port: 'result' } },
  };
  for (const port of attemptPorts) {
    stateInputs[`value_${port}`] = { ref: { node: captureId, port: `value_${port}` } };
  }
  bodyNodes.push({
    id: stateId,
    kind: 'project',
    inputs: stateInputs,
  });

  // Restore original active node list.
  state.activeNodes = savedActive;

  // ── 7. Body subgraph output ───────────────────────────────────────────────
  const bodyOutput: Record<string, Binding> = {
    passed:      { ref: { node: stateId, port: 'passed' } },
    attempts:    { ref: { node: stateId, port: 'attempts' } },
  };
  for (const port of attemptPorts) {
    bodyOutput[`value_${port}`] = { ref: { node: stateId, port: `value_${port}` } };
  }
  state.subgraphs[bodyName] = { nodes: bodyNodes as any, output: bodyOutput };

  // Outer steps referenced from the body (for example `after: reviewed`) are
  // not nodes of this subgraph. Copy one binding per outer node onto the loop
  // so the parent schedules those steps first and validation can see them.
  const loopInputs = externalLoopDeps(state, bodyNodes, bodyName);

  const onExhausted = opts.onExhausted ?? 'pause';

  // ── 8. Loop node ──────────────────────────────────────────────────────────
  state.activeNodes.push({
    id: loopId,
    kind: 'loop',
    while: {
      op: 'not',
      expr: { op: 'field', expr: { op: 'state' }, name: 'passed' },
    },
    initState: {
      op: 'literal',
      value: { passed: false, attempts: 0, ...Object.fromEntries(attemptPorts.map(p => [`value_${p}`, null])) },
    },
    maxIterations: maxIter,
    onExhausted,
    bodyRef: bodyName,
    ...(Object.keys(loopInputs).length > 0 ? { inputs: loopInputs } : {}),
  });

  // ── 9. Expose loop results ────────────────────────────────────────────────
  const loopResultRef: Expr = { op: 'ref', node: loopId, port: 'result' };

  // value proxy: access .portName → state.value_portName
  const valueHandler: ProxyHandler<object> = {
    get(_t, prop) {
      if (prop === SYM_IS_TRACED) return true;
      if (typeof prop === 'symbol') return undefined;
      const port = String(prop);
      return createTracedRef({
        expr: { op: 'field', expr: loopResultRef, name: `value_${port}` },
      });
    },
  };
  const valueProxy = new Proxy({} as object, valueHandler);
  const attemptsProxy = createTracedRef({ expr: { op: 'field', expr: loopResultRef, name: 'attempts' } });

  return {
    value: valueProxy as StepResult<TOutputs>,
    attempts: attemptsProxy,
    loopId,
  };
}

/**
 * Bindings from a loop body to nodes that live outside that body.
 * One entry per referenced node, keyed `__after_N`.
 */
function externalLoopDeps(
  state: CompileState,
  bodyNodes: IRNode[],
  bodyName: string,
): Record<string, Binding> {
  const localIds = new Set<string>();
  const groups: IRNode[][] = [bodyNodes];
  const consider = (name: string) => {
    const sg = state.subgraphs[name];
    if (!sg) return;
    const nodes = (sg.nodes ?? []) as IRNode[];
    groups.push(nodes);
    for (const node of nodes) {
      localIds.add(node.id);
      if (node.bodyRef) consider(node.bodyRef);
      if (node.thenRef) consider(node.thenRef);
      if (node.elseRef) consider(node.elseRef);
    }
  };
  for (const node of bodyNodes) localIds.add(node.id);
  consider(bodyName);

  const found = new Map<string, Binding>();
  for (const nodes of groups) {
    for (const node of nodes) collectExternalNodeDeps(node, localIds, found);
  }

  const inputs: Record<string, Binding> = {};
  let n = 0;
  for (const binding of found.values()) {
    inputs[`__after_${n++}`] = binding;
  }
  return inputs;
}

function collectExternalNodeDeps(node: IRNode, localIds: Set<string>, found: Map<string, Binding>) {
  if (node.inputs) {
    for (const binding of Object.values(node.inputs)) noteExternalBinding(binding, localIds, found);
  }
  for (const expr of [node.items, node.key, node.condition, node.while, node.initState]) {
    if (expr) noteExternalExpr(expr, localIds, found);
  }
}

function noteExternalBinding(binding: Binding | undefined, localIds: Set<string>, found: Map<string, Binding>) {
  if (!binding) return;
  if (binding.ref && !localIds.has(binding.ref.node) && !found.has(binding.ref.node)) {
    found.set(binding.ref.node, binding);
  }
  if (binding.expr) noteExternalExpr(binding.expr, localIds, found);
}

function noteExternalExpr(expr: Expr, localIds: Set<string>, found: Map<string, Binding>) {
  if (expr.op === 'ref' && expr.node && !localIds.has(expr.node) && !found.has(expr.node)) {
    found.set(expr.node, { ref: { node: expr.node, port: expr.port ?? 'result' } });
  }
  if (expr.expr) noteExternalExpr(expr.expr, localIds, found);
  if (expr.left) noteExternalExpr(expr.left, localIds, found);
  if (expr.right) noteExternalExpr(expr.right, localIds, found);
  if (expr.fields) {
    for (const sub of Object.values(expr.fields)) noteExternalExpr(sub, localIds, found);
  }
  if (expr.elements) {
    for (const sub of expr.elements) noteExternalExpr(sub, localIds, found);
  }
}

/**
 * Routes `result.report` (etc.) to `{ ref: captureId, port: 'value_<port>' }`
 * for the current loop iteration's attempt outputs.
 */
function createCaptureResultProxy(captureId: string, ports: string[]): any {
  const portSet = new Set(ports);
  const handler: ProxyHandler<object> = {
    get(_t, prop) {
      if (prop === SYM_IS_TRACED) return true;
      if (typeof prop === 'symbol') return undefined;
      const port = String(prop);
      if (!portSet.has(port)) {
        throw new Error(
          `muxlio: attempt output port '${port}' is not declared (declared: ${ports.join(', ')})`,
        );
      }
      return createTracedRef({ ref: { node: captureId, port: `value_${port}` } });
    },
  };
  return new Proxy({} as object, handler);
}

// ---------------------------------------------------------------------------
// Helper: body result → output bindings
// ---------------------------------------------------------------------------

function resultToBindings(result: unknown): Record<string, Binding> {
  if (result === null || result === undefined) return {};
  if (isTraced(result)) return { result: getBinding(result) };
  if (typeof result === 'object' && !Array.isArray(result)) {
    const out: Record<string, Binding> = {};
    for (const [key, val] of Object.entries(result as Record<string, unknown>)) {
      out[key] = toBinding(val);
    }
    return out;
  }
  return { result: toBinding(result) };
}

// ---------------------------------------------------------------------------
// workflow() — top-level API
// ---------------------------------------------------------------------------

type BuildContextFor<TInput extends z.ZodTypeAny | JSONSchema> =
  TInput extends z.ZodTypeAny ? WorkflowBuildContext<TInput> : WorkflowBuildContext<z.ZodTypeAny>;

/** Options for declaring a workflow. */
export interface WorkflowOptions<TInput extends z.ZodTypeAny | JSONSchema = JSONSchema> {
  workspaceId?: string;
  /** SHA-256 identity of an operator-prepared workspace tree. */
  workspaceDigest?: string;
  limits?: Plan['limits'];
  deadlineSeconds?: number;
  /** Input schema: a Zod type (converted automatically) or a plain JSON Schema object. */
  input?: TInput;
  /**
   * Synchronous build function. Called once at compile time. Must NOT execute
   * agents, commands, or I/O of any kind.
   *
   * Returns an object whose values are symbolic output references.
   */
  build: (ctx: BuildContextFor<TInput>) => Record<string, unknown> | void;
}

/** A compiled workflow, suitable as a module default export. */
export interface CompiledWorkflow {
  /** The immutable compiled Plan IR. */
  readonly plan: Plan;
}

/**
 * Declare a workflow and compile it to a Plan IR document.
 *
 * @example
 * export default workflow('implement-features', {
 *   input,
 *   build: ({ input, step, each, repeatUntil }) => {
 *     // ...
 *   },
 * });
 */
export function workflow<TInput extends z.ZodTypeAny | JSONSchema>(
  name: string,
  options: WorkflowOptions<TInput>,
): CompiledWorkflow {
  let compiled: Plan | undefined;
  return {
    get plan() {
      if (!compiled) {
        compiled = compileWorkflowPlan(name, options);
      }
      return compiled;
    },
  };
}

function compileWorkflowPlan<TInput extends z.ZodTypeAny | JSONSchema>(
  name: string,
  options: WorkflowOptions<TInput>,
): Plan {
  const counter = { value: 0 };
  const rootNodes: IRNode[] = [];
  const state: CompileState = {
    counter,
    subgraphs: {},
    requiredExecutors: new Set(),
    activeNodes: rootNodes,
  };

  const ctx = makeContext(state, { op: 'input' }) as BuildContextFor<TInput>;
  const outputResult = options.build(ctx);
  const rootOutput = outputResult ? resultToBindings(outputResult) : {};

  // Accept either a Zod schema or a plain JSON Schema object.
  let inputSchema: JSONSchema | undefined;
  if (options.input) {
    if (typeof (options.input as any)._def !== 'undefined') {
      // Zod schema
      inputSchema = zodToJsonSchema(options.input as z.ZodTypeAny);
    } else {
      // Plain JSON Schema
      inputSchema = options.input as unknown as JSONSchema;
    }
  }

  const plan: Plan = {
    irVersion: '1.0',
    workspaceId: options.workspaceId,
    workspaceDigest: options.workspaceDigest,
    deadlineSeconds: options.deadlineSeconds,
    limits: options.limits,
    name,
    root: 'root',
    inputSchema: inputSchema as any,
    subgraphs: {
      root: {
        nodes: rootNodes as any,
        output: rootOutput,
      },
      ...state.subgraphs,
    },
    requiredExecutors: [...state.requiredExecutors].sort(),
  };

  assertNoLiteralCredentials(plan);
  plan.digest = computeDigest(plan);

  return plan;
}
