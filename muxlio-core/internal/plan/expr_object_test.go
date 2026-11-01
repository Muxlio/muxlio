package plan_test

import (
	"encoding/json"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
)

func TestObjectExpressionBuildsNestedInput(t *testing.T) {
	ctx := &plan.EvalContext{
		Item: map[string]any{"id": "feat-1", "feature": "do thing"},
	}
	expr := &plan.Expr{
		Op: "object",
		Fields: map[string]*plan.Expr{
			"id": {Op: "field", Expr: &plan.Expr{Op: "item"}, Name: "id"},
		},
	}
	val, err := expr.Eval(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := val.(map[string]any)
	if !ok || m["id"] != "feat-1" {
		t.Fatalf("object expr = %#v", val)
	}
}

func TestValidateExecuteOutputsRejectsWrongType(t *testing.T) {
	schema, _ := json.Marshal(map[string]any{"type": "boolean"})
	outputs := map[string]plan.OutputDecl{
		"passed": {Mime: "application/json", Schema: schema},
	}
	err := plan.ValidateExecuteOutputs(outputs, map[string]any{"passed": "not-a-bool"})
	if err == nil {
		t.Fatal("expected validation error")
	}
}
