package plan_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestLoopPersistsProgressAfterEachIteration(t *testing.T) {
	max := 2
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "loop-progress",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {Nodes: []plan.Node{{
				ID: "loop", Kind: plan.KindLoop,
				While:         &plan.Expr{Op: "literal", Value: json.RawMessage(`true`)},
				InitState:     &plan.Expr{Op: "literal", Value: json.RawMessage(`0`)},
				MaxIterations: &max,
				OnExhausted:   "pause",
				BodyRef:       "body",
			}}},
			"body": {
				Nodes: []plan.Node{{
					ID: "step", Kind: plan.KindProject,
					Inputs: map[string]plan.Binding{
						"value": {Expr: &plan.Expr{Op: "add", Left: &plan.Expr{Op: "state"}, Right: &plan.Expr{Op: "literal", Value: json.RawMessage(`1`)}}},
					},
				}},
				Output: map[string]plan.Binding{"value": {Ref: &plan.ValueRef{Node: "step", Port: "value"}}},
			},
		},
	}
	store := state.New()
	engine := plan.NewEngineWithStore(p, workers.NewFake(), store)
	runID, err := engine.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(runID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	act, err := store.GetActivation(runID + ":loop")
	if err != nil {
		t.Fatal(err)
	}
	prog, _ := act.Outputs[state.LoopProgressOutputKey].(map[string]any)
	if prog == nil || prog["exhausted"] != true {
		t.Fatalf("expected exhausted progress: %+v", act.Outputs)
	}
	// Completed child activations must remain after exhaustion checkpoint.
	acts, _ := store.ListActivations(runID)
	var bodyDone int
	for _, a := range acts {
		if a.State == plan.ActSucceeded && a.NodeID == "step" {
			bodyDone++
		}
	}
	if bodyDone != 2 {
		t.Fatalf("completed body activations=%d", bodyDone)
	}
}

func TestRejectExhaustedLoopRetryCreatesNoAttempts(t *testing.T) {
	max := 1
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "exhausted-retry",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {Nodes: []plan.Node{{
				ID: "loop", Kind: plan.KindLoop,
				While:         &plan.Expr{Op: "literal", Value: json.RawMessage(`true`)},
				InitState:     &plan.Expr{Op: "literal", Value: json.RawMessage(`0`)},
				MaxIterations: &max,
				OnExhausted:   "fail",
				BodyRef:       "body",
			}}},
			"body": {Nodes: []plan.Node{{
				ID: "step", Kind: plan.KindProject,
				Inputs: map[string]plan.Binding{"v": {Expr: &plan.Expr{Op: "literal", Value: json.RawMessage(`1`)}}},
			}}},
		},
	}
	store := state.New()
	engine := plan.NewEngineWithStore(p, workers.NewFake(), store)
	runID, err := engine.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(runID); err == nil {
		t.Fatal("expected exhaustion failure")
	}
	before, err := store.ListExecutionAttempts(runID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Retry(runID); !errors.Is(err, plan.ErrLoopExhaustedUnchanged) {
		t.Fatalf("expected exhausted conflict, got %v", err)
	}
	after, err := store.ListExecutionAttempts(runID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("retry created attempts: before=%d after=%d", len(before), len(after))
	}
}

func TestLoopCompletesWhenWhileFalseOnLastIteration(t *testing.T) {
	max := 2
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "loop-complete",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {Nodes: []plan.Node{{
				ID: "loop", Kind: plan.KindLoop,
				While: &plan.Expr{Op: "lt", Left: &plan.Expr{Op: "state"}, Right: &plan.Expr{Op: "literal", Value: json.RawMessage(`2`)}},
				InitState:     &plan.Expr{Op: "literal", Value: json.RawMessage(`0`)},
				MaxIterations: &max,
				OnExhausted:   "fail",
				BodyRef:       "body",
			}}},
			"body": {
				Nodes: []plan.Node{{
					ID: "step", Kind: plan.KindProject,
					Inputs: map[string]plan.Binding{
						"value": {Expr: &plan.Expr{Op: "add", Left: &plan.Expr{Op: "state"}, Right: &plan.Expr{Op: "literal", Value: json.RawMessage(`1`)}}},
					},
				}},
				Output: map[string]plan.Binding{"value": {Ref: &plan.ValueRef{Node: "step", Port: "value"}}},
			},
		},
	}
	store := state.New()
	engine := plan.NewEngineWithStore(p, workers.NewFake(), store)
	runID, err := engine.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(runID); err != nil {
		t.Fatalf("expected normal completion: %v", err)
	}
	run, _ := store.GetRun(runID)
	if run.State != plan.RunSucceeded {
		t.Fatalf("state=%q", run.State)
	}
}
