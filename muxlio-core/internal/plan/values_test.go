package plan

import (
	"encoding/json"
	"math"
	"testing"
)

func TestExpressionNumericEquality(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   any
		literal string
		equal   bool
	}{
		{"integer", json.Number("7"), `7`, true},
		{"zero", json.Number("0"), `0`, true},
		{"negative-zero", json.Number("-0.0"), `0`, true},
		{"decimal", json.Number("0.10"), `1e-1`, true},
		{"float", float64(0.1), `0.1`, true},
		{"native-int", int64(7), `7`, true},
		{"native-uint", uint64(18446744073709551615), `18446744073709551615`, true},
		{"large-exact", json.Number("9007199254740993"), `9007199254740993`, true},
		{"large-distinct", json.Number("9007199254740993"), `9007199254740992`, false},
		{"rounded-float", float64(9007199254740992), `9007199254740993`, false},
		{"outside-float-range", json.Number("1e400"), `10e399`, true},
		{"huge-exponent", json.Number("1e999999999999999999999"), `10e999999999999999999998`, true},
		{"tiny-distinct", json.Number("1e-400"), `0`, false},
		{"number-not-string", json.Number("7"), `"7"`, false},
		{"string-not-number", "7", `7`, false},
		{"nested", map[string]any{"values": []any{json.Number("7"), float64(0), map[string]any{"n": json.Number("1e400")}}}, `{"values":[7,0,{"n":10e399}]}`, true},
		{"nested-distinct", map[string]any{"values": []any{json.Number("9007199254740993")}}, `{"values":[9007199254740992]}`, false},
		{"missing-key", map[string]any{"a": nil}, `{"b":null}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, op := range []string{"eq", "ne"} {
				for _, reverse := range []bool{false, true} {
					left, right := &Expr{Op: "input"}, &Expr{Op: "literal", Value: json.RawMessage(tc.literal)}
					if reverse {
						left, right = right, left
					}
					got, err := (&Expr{Op: op, Left: left, Right: right}).Eval(&EvalContext{Input: tc.input})
					want := tc.equal
					if op == "ne" {
						want = !want
					}
					if err != nil || got != want {
						t.Fatalf("%s reverse=%v: got %v, %v; want %v", op, reverse, got, err, want)
					}
				}
			}
		})
	}
	// Arithmetic and len still produce native floats; both compare with literals.
	for _, computed := range []*Expr{
		{Op: "add", Left: &Expr{Op: "literal", Value: json.RawMessage(`3`)}, Right: &Expr{Op: "literal", Value: json.RawMessage(`4`)}},
		{Op: "len", Expr: &Expr{Op: "literal", Value: json.RawMessage(`"1234567"`)}},
	} {
		got, err := (&Expr{Op: "eq", Left: computed, Right: &Expr{Op: "literal", Value: json.RawMessage(`7`)}}).Eval(&EvalContext{})
		if err != nil || got != true {
			t.Fatalf("computed equality: %v %v", got, err)
		}
	}
}

func TestLoopCheckpointIteration(t *testing.T) {
	for _, value := range []any{json.Number("1"), float64(1), int(1), int64(1)} {
		got, err := loopCheckpointIteration(value)
		if err != nil || got != 1 {
			t.Errorf("%v (%T): got %d, %v", value, value, got, err)
		}
	}
	for _, value := range []any{json.Number("9223372036854775808"), json.Number("1.5"), json.Number("1e400"), json.Number("-1"), float64(-1), 1.5, math.Inf(1), math.NaN(), float64(0x1p63), "1", nil} {
		if _, err := loopCheckpointIteration(value); err == nil {
			t.Errorf("accepted %v (%T)", value, value)
		}
	}
}
