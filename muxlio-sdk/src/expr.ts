// Expression helpers for building the Muxlio expression language AST.
// Every helper returns an Expr object that the Go interpreter evaluates
// against the runtime context (input, item, state, node outputs).

import type { Expr } from './types.js';
import { authored } from './authored.js';
import { literalExpr, toExpr, type ExprLike } from './builder.js';

/** Root run input. */
export function input(): Expr {
  return authored({ op: 'input' });
}

/** Current map item. */
export function item(): Expr {
  return authored({ op: 'item' });
}

/** Current loop-carried state. */
export function state(): Expr {
  return authored({ op: 'state' });
}

/** A literal JSON value. */
export function literal(value: unknown): Expr {
  return literalExpr(value);
}

/** Field access on an object expression. */
export function field(obj: ExprLike, name: string): Expr {
  return authored({ op: 'field', expr: toExpr(obj), name });
}

/** Element access on an array, or a non-negative integer key on an object. */
export function index(arr: ExprLike, idx: number): Expr {
  return authored({ op: 'index', expr: toExpr(arr), index: idx });
}

/** Length of an array, string, or object. */
export function len(e: ExprLike): Expr {
  return authored({ op: 'len', expr: toExpr(e) });
}

/** Equality check. */
export function eq(left: ExprLike, right: ExprLike): Expr {
  return authored({ op: 'eq', left: toExpr(left), right: toExpr(right) });
}

/** Inequality check. */
export function ne(left: ExprLike, right: ExprLike): Expr {
  return authored({ op: 'ne', left: toExpr(left), right: toExpr(right) });
}

/** Less than. */
export function lt(left: ExprLike, right: ExprLike): Expr {
  return authored({ op: 'lt', left: toExpr(left), right: toExpr(right) });
}

/** Greater than. */
export function gt(left: ExprLike, right: ExprLike): Expr {
  return authored({ op: 'gt', left: toExpr(left), right: toExpr(right) });
}

/** Less than or equal. */
export function le(left: ExprLike, right: ExprLike): Expr {
  return authored({ op: 'le', left: toExpr(left), right: toExpr(right) });
}

/** Greater than or equal. */
export function ge(left: ExprLike, right: ExprLike): Expr {
  return authored({ op: 'ge', left: toExpr(left), right: toExpr(right) });
}

/** Boolean AND. */
export function and(left: ExprLike, right: ExprLike): Expr {
  return authored({ op: 'and', left: toExpr(left), right: toExpr(right) });
}

/** Boolean OR. */
export function or(left: ExprLike, right: ExprLike): Expr {
  return authored({ op: 'or', left: toExpr(left), right: toExpr(right) });
}

/** Boolean NOT. */
export function not(e: ExprLike): Expr {
  return authored({ op: 'not', expr: toExpr(e) });
}

/** Numeric addition or string concatenation. */
export function add(left: ExprLike, right: ExprLike): Expr {
  return authored({ op: 'add', left: toExpr(left), right: toExpr(right) });
}

/** Numeric subtraction. */
export function sub(left: ExprLike, right: ExprLike): Expr {
  return authored({ op: 'sub', left: toExpr(left), right: toExpr(right) });
}

/** Numeric multiplication. */
export function mul(left: ExprLike, right: ExprLike): Expr {
  return authored({ op: 'mul', left: toExpr(left), right: toExpr(right) });
}

/** Numeric division. */
export function div(left: ExprLike, right: ExprLike): Expr {
  return authored({ op: 'div', left: toExpr(left), right: toExpr(right) });
}

/** Integer remainder. */
export function mod(left: ExprLike, right: ExprLike): Expr {
  return authored({ op: 'mod', left: toExpr(left), right: toExpr(right) });
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
