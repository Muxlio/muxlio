package plan

import "testing"

func TestNumericAccessPreservesArrayAndObjectSemantics(t *testing.T) {
	expr := &Expr{Op: "index", Expr: &Expr{Op: "input"}, Index: 0}
	for _, tc := range []struct {
		name  string
		input any
		want  any
	}{
		{"array", []any{"first", "second"}, "first"},
		{"numeric object key", map[string]any{"0": "first", "1": "second"}, "first"},
		{"missing object key", map[string]any{"1": "second"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expr.Eval(&EvalContext{Input: tc.input})
			if err != nil || got != tc.want {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	for _, input := range []any{[]any{}, "text", true, nil} {
		if _, err := expr.Eval(&EvalContext{Input: input}); err == nil {
			t.Fatalf("invalid indexing accepted: %v", input)
		}
	}
}
