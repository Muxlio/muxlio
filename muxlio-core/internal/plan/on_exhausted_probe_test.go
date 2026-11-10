package plan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/workers"
)

func loopExhaustionProbePlan(onExhausted string) *Plan {
	max := 1
	return &Plan{
		IRVersion: "1.0", Name: "on-exhausted-probe", Root: "root",
		Subgraphs: map[string]Subgraph{
			"root": {Nodes: []Node{{
				ID: "loop", Kind: KindLoop,
				While:         &Expr{Op: "literal", Value: json.RawMessage(`true`)},
				InitState:     &Expr{Op: "literal", Value: json.RawMessage(`0`)},
				MaxIterations: &max,
				OnExhausted:   onExhausted,
				BodyRef:       "body",
			}}},
			"body": {
				Nodes:  []Node{{ID: "s", Kind: KindProject, Inputs: map[string]Binding{"v": {Expr: &Expr{Op: "literal", Value: json.RawMessage(`1`)}}}}},
				Output: map[string]Binding{"v": {Ref: &ValueRef{Node: "s", Port: "v"}}},
			},
		},
	}
}

func TestInvalidOnExhaustedRejectedAtValidation(t *testing.T) {
	errs := Validate(loopExhaustionProbePlan("bogus"))
	if !HasErrors(errs) {
		t.Fatal("expected validation error for invalid onExhausted")
	}
	found := false
	for _, e := range errs {
		if e.Code == "MUXLIO_BAD_ON_EXHAUSTED" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected MUXLIO_BAD_ON_EXHAUSTED, got %+v", errs)
	}
}

func TestInvalidOnExhaustedFailsAtRuntimeForPinnedPlan(t *testing.T) {
	p := loopExhaustionProbePlan("bogus")
	engine := NewEngine(p, workers.NewFake())
	if _, err := engine.CreateRun(json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "invalid plan") {
		t.Fatalf("CreateRun should reject invalid plan: %v", err)
	}
	// A pinned plan may bypass CreateRun validation; the interpreter must not
	// treat exhaustion as success for unknown policies.
	node := &p.Subgraphs["root"].Nodes[0]
	if _, err := engine.executeLoop("run", node, nil, &EvalContext{}); err == nil || !strings.Contains(err.Error(), "invalid onExhausted") {
		t.Fatalf("executeLoop: %v", err)
	}
}
