import type { Binding, Expr } from './types.js';
import { toExpr, type BuildContext, type LoopResult, type MapResult } from './builder.js';

/** A binding whose value type is known to the workflow author. */
export type TypedBinding<T> = Binding & { readonly __valueType?: T };
/** An expression with an author-known value type. */
export type TypedExpr<T> = Expr & { readonly __valueType?: T };

/** Options for a bounded, loop-based refinement. */
export interface RefineOptions<T> {
  /** Initial value carried through the loop. */
  initial: TypedBinding<T>;
  /** Required bound; the loop stops after this many refinement passes. */
  maxIterations: number;
  /** Return true while another refinement pass should run. */
  continueWhile: (state: TypedExpr<T>) => Expr;
  /** Produce the next value. Failed executes propagate and fail the workflow. */
  refine: (ctx: BuildContext, state: TypedExpr<T>) => TypedBinding<T>;
}

/**
 * Compose a refinement loop from the existing Plan loop primitive.
 * Refinement executes zero to maxIterations times; reaching the bound returns
 * the latest state even when the continuation predicate remains true.
 */
export function refine<T>(g: BuildContext, id: string, opts: RefineOptions<T>): TypedBinding<T> {
  if (!Number.isSafeInteger(opts.maxIterations) || opts.maxIterations < 0) {
    throw new Error('muxlio: refine maxIterations must be a non-negative safe integer');
  }
  if (opts.maxIterations === 0) return { expr: toExpr(opts.initial) } as TypedBinding<T>;
  // Plan loops pause when the bound is hit with `while` still true, so the
  // pass counter is carried in state and ends the loop at the bound instead.
  const value = g.stateField('value') as TypedExpr<T>;
  const pass = g.stateField('pass');
  const loop: LoopResult = g.loop(id, {
    while: g.and(opts.continueWhile(value), g.lt(pass, g.literal(opts.maxIterations))),
    initState: { op: 'object', fields: { value: toExpr(opts.initial), pass: g.literal(0) } },
    maxIterations: opts.maxIterations,
    body: (ctx) => ({
      value: opts.refine(ctx, value),
      pass: { expr: ctx.add(pass, ctx.literal(1)) },
    }),
  });
  return { expr: g.field(loop.result(), 'value') } as TypedBinding<T>;
}

/** Options for mapping speculative candidates and selecting one by position. */
export interface SpeculateOptions<TCandidate, TResult> {
  candidates: TypedExpr<readonly TCandidate[]>;
  concurrency?: number;
  maxItems?: number;
  /** Transform each candidate. A failed candidate fails the map/workflow. */
  evaluate: (ctx: BuildContext, candidate: TypedExpr<TCandidate>) => TypedBinding<TResult>;
  /** Zero-based position in the input order; selection is deterministic. */
  select: number;
  /** Optional phantom type to document the candidate shape. */
  readonly candidateType?: TCandidate;
}

/**
 * Evaluate candidates with the existing map primitive and select one result.
 * All candidates must succeed; out-of-range selection fails during execution.
 */
export function speculate<TCandidate, TResult>(
  g: BuildContext,
  id: string,
  opts: SpeculateOptions<TCandidate, TResult>,
): TypedBinding<TResult> {
  if (!Number.isSafeInteger(opts.select) || opts.select < 0) {
    throw new Error('muxlio: speculate select must be a non-negative safe integer');
  }
  const mapped: MapResult = g.map(id, {
    items: opts.candidates,
    concurrency: opts.concurrency,
    maxItems: opts.maxItems,
    body: (ctx) => ({ value: opts.evaluate(ctx, ctx.item() as TypedExpr<TCandidate>) }),
  });
  return { expr: g.field(g.index(mapped.items(), opts.select), 'value') } as TypedBinding<TResult>;
}

