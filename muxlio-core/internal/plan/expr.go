package plan

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// Expr is a pure expression in the Muxlio expression language. It is a
// JSON-based AST evaluated by the Go interpreter. The language supports
// field access, indexing, literals, comparisons, boolean logic, length,
// and references to node outputs — nothing with side effects.
//
// Operators:
//
//	op        fields              semantics
//	input     —                   root run input
//	item      —                   current map item
//	state     —                   current loop-carried state
//	literal   value               literal JSON value
//	ref       node, port           output port of a node in current scope
//	field     expr, name           field access on an object
//	index     expr, index          element access on an array
//	len       expr                 length of an array or string
//	eq/ne     left, right          equality / inequality
//	lt/gt/le/ge left, right         numeric comparison
//	and/or    left, right          boolean (short-circuit not guaranteed)
//	not       expr                 boolean negation
//	add       left, right          numeric addition / string concatenation
//	sub       left, right          numeric subtraction
//	mul       left, right          numeric multiplication
//	div       left, right          numeric division
//	mod       left, right          integer remainder
type Expr struct {
	// Op is one of the operators documented above.
	Op    string          `json:"op"`
	Value json.RawMessage `json:"value,omitempty"`
	Expr  *Expr           `json:"expr,omitempty"`
	Name  string          `json:"name,omitempty"`
	Index int             `json:"index,omitempty"`
	Node  string          `json:"node,omitempty"`
	Port  string          `json:"port,omitempty"`
	Left  *Expr           `json:"left,omitempty"`
	Right *Expr           `json:"right,omitempty"`
	// object: field name → sub-expression
	Fields map[string]*Expr `json:"fields,omitempty"`
	// array: ordered element expressions
	Elements []*Expr `json:"elements,omitempty"`
}

// EvalContext provides the runtime context for expression evaluation.
type EvalContext struct {
	// subgraph identifies the source of the currently executing node.
	subgraph string
	// Input is the root run input.
	Input any

	// Item is the current map item, or nil outside a map body.
	Item any

	// State is the current loop-carried state, or nil outside a loop body.
	State any

	// Outputs maps nodeID → port → value for nodes that have completed
	// within the current scope.
	Outputs map[string]map[string]any
}

// Eval evaluates the expression against the given context.
func (e *Expr) Eval(ctx *EvalContext) (any, error) {
	if e == nil {
		return nil, nil
	}
	switch e.Op {
	case "input":
		return ctx.Input, nil
	case "item":
		return ctx.Item, nil
	case "state":
		return ctx.State, nil
	case "literal":
		return e.literalValue()
	case "ref":
		return e.evalRef(ctx)
	case "field":
		return e.evalField(ctx)
	case "index":
		return e.evalIndex(ctx)
	case "len":
		return e.evalLen(ctx)
	case "eq", "ne":
		return e.evalEquality(ctx)
	case "lt", "gt", "le", "ge":
		return e.evalCompare(ctx)
	case "and", "or":
		return e.evalBoolBinary(ctx)
	case "not":
		return e.evalNot(ctx)
	case "add":
		return e.evalAdd(ctx)
	case "sub":
		return e.evalSub(ctx)
	case "mul":
		return e.evalMul(ctx)
	case "div":
		return e.evalDiv(ctx)
	case "mod":
		return e.evalMod(ctx)
	case "object":
		return e.evalObject(ctx)
	case "array":
		return e.evalArray(ctx)
	default:
		return nil, fmt.Errorf("muxlio: unknown expression op %q", e.Op)
	}
}

func (e *Expr) literalValue() (any, error) {
	var v any
	if err := json.Unmarshal(e.Value, &v); err != nil {
		return nil, fmt.Errorf("muxlio: bad literal in expression: %w", err)
	}
	return v, nil
}

func (e *Expr) evalRef(ctx *EvalContext) (any, error) {
	nodeOut, ok := ctx.Outputs[e.Node]
	if !ok {
		return nil, fmt.Errorf("muxlio: no outputs for node %q", e.Node)
	}
	val, ok := nodeOut[e.Port]
	if !ok {
		return nil, fmt.Errorf("muxlio: no output port %q on node %q", e.Port, e.Node)
	}
	return val, nil
}

func (e *Expr) evalObject(ctx *EvalContext) (any, error) {
	out := make(map[string]any, len(e.Fields))
	for name, sub := range e.Fields {
		val, err := sub.Eval(ctx)
		if err != nil {
			return nil, err
		}
		out[name] = val
	}
	return out, nil
}

func (e *Expr) evalArray(ctx *EvalContext) (any, error) {
	out := make([]any, len(e.Elements))
	for i, sub := range e.Elements {
		val, err := sub.Eval(ctx)
		if err != nil {
			return nil, err
		}
		out[i] = val
	}
	return out, nil
}

func (e *Expr) evalField(ctx *EvalContext) (any, error) {
	obj, err := e.Expr.Eval(ctx)
	if err != nil {
		return nil, err
	}
	m, ok := obj.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("muxlio: cannot access field %q on non-object (%T)", e.Name, obj)
	}
	return m[e.Name], nil
}

func (e *Expr) evalIndex(ctx *EvalContext) (any, error) {
	arr, err := e.Expr.Eval(ctx)
	if err != nil {
		return nil, err
	}
	a, ok := arr.([]any)
	if !ok {
		return nil, fmt.Errorf("muxlio: cannot index non-array (%T)", arr)
	}
	if e.Index < 0 || e.Index >= len(a) {
		return nil, fmt.Errorf("muxlio: index %d out of bounds (len %d)", e.Index, len(a))
	}
	return a[e.Index], nil
}

func (e *Expr) evalLen(ctx *EvalContext) (any, error) {
	val, err := e.Expr.Eval(ctx)
	if err != nil {
		return nil, err
	}
	switch v := val.(type) {
	case []any:
		return float64(len(v)), nil
	case string:
		return float64(len(v)), nil
	case map[string]any:
		return float64(len(v)), nil
	default:
		return nil, fmt.Errorf("muxlio: len() requires array, string, or object (%T)", val)
	}
}

func (e *Expr) evalEquality(ctx *EvalContext) (any, error) {
	l, err := e.Left.Eval(ctx)
	if err != nil {
		return nil, err
	}
	r, err := e.Right.Eval(ctx)
	if err != nil {
		return nil, err
	}
	eq := reflect.DeepEqual(l, r)
	if e.Op == "ne" {
		return !eq, nil
	}
	return eq, nil
}

func (e *Expr) evalCompare(ctx *EvalContext) (any, error) {
	l, err := toFloat64(e.Left, ctx)
	if err != nil {
		return nil, err
	}
	r, err := toFloat64(e.Right, ctx)
	if err != nil {
		return nil, err
	}
	switch e.Op {
	case "lt":
		return l < r, nil
	case "gt":
		return l > r, nil
	case "le":
		return l <= r, nil
	case "ge":
		return l >= r, nil
	default:
		return nil, fmt.Errorf("muxlio: unknown comparison %q", e.Op)
	}
}

func (e *Expr) evalBoolBinary(ctx *EvalContext) (any, error) {
	l, err := e.Left.Eval(ctx)
	if err != nil {
		return nil, err
	}
	lb, err := toBool(l)
	if err != nil {
		return nil, err
	}
	r, err := e.Right.Eval(ctx)
	if err != nil {
		return nil, err
	}
	rb, err := toBool(r)
	if err != nil {
		return nil, err
	}
	if e.Op == "and" {
		return lb && rb, nil
	}
	return lb || rb, nil
}

func (e *Expr) evalNot(ctx *EvalContext) (any, error) {
	v, err := e.Expr.Eval(ctx)
	if err != nil {
		return nil, err
	}
	b, err := toBool(v)
	if err != nil {
		return nil, err
	}
	return !b, nil
}

func (e *Expr) evalAdd(ctx *EvalContext) (any, error) {
	l, err := e.Left.Eval(ctx)
	if err != nil {
		return nil, err
	}
	r, err := e.Right.Eval(ctx)
	if err != nil {
		return nil, err
	}
	ls, lok := l.(string)
	rs, rok := r.(string)
	if lok && rok {
		return ls + rs, nil
	}
	lf, err := toFloat64Val(l)
	if err != nil {
		return nil, fmt.Errorf("muxlio: add: %w", err)
	}
	rf, err := toFloat64Val(r)
	if err != nil {
		return nil, fmt.Errorf("muxlio: add: %w", err)
	}
	return lf + rf, nil
}

func (e *Expr) evalSub(ctx *EvalContext) (any, error) {
	l, err := toFloat64(e.Left, ctx)
	if err != nil {
		return nil, fmt.Errorf("muxlio: sub: %w", err)
	}
	r, err := toFloat64(e.Right, ctx)
	if err != nil {
		return nil, fmt.Errorf("muxlio: sub: %w", err)
	}
	return l - r, nil
}

func (e *Expr) evalMul(ctx *EvalContext) (any, error) {
	l, err := toFloat64(e.Left, ctx)
	if err != nil {
		return nil, fmt.Errorf("muxlio: mul: %w", err)
	}
	r, err := toFloat64(e.Right, ctx)
	if err != nil {
		return nil, fmt.Errorf("muxlio: mul: %w", err)
	}
	return l * r, nil
}

func (e *Expr) evalDiv(ctx *EvalContext) (any, error) {
	l, err := toFloat64(e.Left, ctx)
	if err != nil {
		return nil, fmt.Errorf("muxlio: div: %w", err)
	}
	r, err := toFloat64(e.Right, ctx)
	if err != nil {
		return nil, fmt.Errorf("muxlio: div: %w", err)
	}
	if r == 0 {
		return nil, fmt.Errorf("muxlio: div: divide by zero")
	}
	return l / r, nil
}

func (e *Expr) evalMod(ctx *EvalContext) (any, error) {
	l, err := toFloat64(e.Left, ctx)
	if err != nil {
		return nil, fmt.Errorf("muxlio: mod: %w", err)
	}
	r, err := toFloat64(e.Right, ctx)
	if err != nil {
		return nil, fmt.Errorf("muxlio: mod: %w", err)
	}
	li, ri := int(l), int(r)
	if ri == 0 {
		return nil, fmt.Errorf("muxlio: mod: divide by zero")
	}
	return float64(li % ri), nil
}

func toFloat64(e *Expr, ctx *EvalContext) (float64, error) {
	v, err := e.Eval(ctx)
	if err != nil {
		return 0, err
	}
	return toFloat64Val(v)
}

func toFloat64Val(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, fmt.Errorf("muxlio: invalid number %v", n)
		}
		return f, nil
	default:
		return 0, fmt.Errorf("muxlio: expected number, got %T", v)
	}
}

func toBool(v any) (bool, error) {
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("muxlio: expected bool, got %T", v)
	}
	return b, nil
}

// EvalBinding is a convenience that resolves a Binding using the context.
func EvalBinding(b *Binding, ctx *EvalContext) (any, error) {
	return b.Resolve(ctx)
}

// EvalBindings resolves a map of named bindings to concrete values.
func EvalBindings(bindings map[string]Binding, ctx *EvalContext) (map[string]any, error) {
	result := make(map[string]any, len(bindings))
	for name, b := range bindings {
		val, err := b.Resolve(ctx)
		if err != nil {
			return nil, fmt.Errorf("muxlio: resolving binding %q: %w", name, err)
		}
		result[name] = val
	}
	return result, nil
}
