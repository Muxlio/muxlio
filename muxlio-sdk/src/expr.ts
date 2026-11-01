// Expression helpers for building the Muxlio expression language AST.
// Every helper returns an Expr object that the Go interpreter evaluates
// against the runtime context (input, item, state, node outputs).

import type { Expr } from './types.js';

/** Root run input. */
export function input(): Expr {
  return { op: 'input' };
}

/** Current map item. */
export function item(): Expr {
  return { op: 'item' };
}

/** Current loop-carried state. */
export function state(): Expr {
  return { op: 'state' };
}

/** A literal JSON value. */
export function literal(value: unknown): Expr {
  return { op: 'literal', value };
}

/** Field access on an object expression. */
export function field(obj: Expr, name: string): Expr {
  return { op: 'field', expr: obj, name };
}

/** Element access on an array expression. */
export function index(arr: Expr, idx: number): Expr {
  return { op: 'index', expr: arr, index: idx };
}

/** Length of an array, string, or object. */
export function len(e: Expr): Expr {
  return { op: 'len', expr: e };
}

/** Equality check. */
export function eq(left: Expr, right: Expr): Expr {
  return { op: 'eq', left, right };
}

/** Inequality check. */
export function ne(left: Expr, right: Expr): Expr {
  return { op: 'ne', left, right };
}

/** Less than. */
export function lt(left: Expr, right: Expr): Expr {
  return { op: 'lt', left, right };
}

/** Greater than. */
export function gt(left: Expr, right: Expr): Expr {
  return { op: 'gt', left, right };
}

/** Less than or equal. */
export function le(left: Expr, right: Expr): Expr {
  return { op: 'le', left, right };
}

/** Greater than or equal. */
export function ge(left: Expr, right: Expr): Expr {
  return { op: 'ge', left, right };
}

/** Boolean AND. */
export function and(left: Expr, right: Expr): Expr {
  return { op: 'and', left, right };
}

/** Boolean OR. */
export function or(left: Expr, right: Expr): Expr {
  return { op: 'or', left, right };
}

/** Boolean NOT. */
export function not(e: Expr): Expr {
  return { op: 'not', expr: e };
}

/** Numeric addition or string concatenation. */
export function add(left: Expr, right: Expr): Expr {
  return { op: 'add', left, right };
}

/** Numeric subtraction. */
export function sub(left: Expr, right: Expr): Expr {
  return { op: 'sub', left, right };
}

/** Numeric multiplication. */
export function mul(left: Expr, right: Expr): Expr {
  return { op: 'mul', left, right };
}

/** Numeric division. */
export function div(left: Expr, right: Expr): Expr {
  return { op: 'div', left, right };
}

/** Integer remainder. */
export function mod(left: Expr, right: Expr): Expr {
  return { op: 'mod', left, right };
}

/** Convenience: field access on the root input. */
export function inputField(name: string): Expr {
  return field(input(), name);
}

/** Convenience: field access on the current map item. */
export function itemField(name: string): Expr {
  return field(item(), name);
}

/** Convenience: field access on the current loop state. */
export function stateField(name: string): Expr {
  return field(state(), name);
}
