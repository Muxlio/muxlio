package plan_test

import (
	"encoding/json"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestLoopExhaustedPausesRun(t *testing.T) {
	max := 2
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "loop-pause",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:            "loop",
						Kind:          plan.KindLoop,
						While:         &plan.Expr{Op: "literal", Value: json.RawMessage(`true`)},
						InitState:     &plan.Expr{Op: "literal", Value: json.RawMessage(`{"passed":false}`)},
						MaxIterations: &max,
						OnExhausted:   "pause",
						BodyRef:       "body",
					},
				},
			},
			"body": {
				Nodes: []plan.Node{
					{
						ID:   "set",
						Kind: plan.KindProject,
						Inputs: map[string]plan.Binding{
							"passed": {Expr: &plan.Expr{Op: "literal", Value: json.RawMessage(`false`)}},
						},
					},
				},
				Output: map[string]plan.Binding{
					"passed": {Ref: &plan.ValueRef{Node: "set", Port: "passed"}},
				},
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
	run, _ := store.GetRun(runID)
	if run.State != plan.RunPaused {
		t.Fatalf("expected paused, got %q", run.State)
	}
	acts, _ := store.ListActivations(runID)
	var loopAct *state.Activation
	for i := range acts {
		if acts[i].NodeID == "loop" {
			loopAct = &acts[i]
			break
		}
	}
	if loopAct == nil || loopAct.State != plan.ActWaiting {
		t.Fatalf("loop activation = %#v, want waiting", loopAct)
	}
}

func TestLoopExhaustedCannotChangePinnedBudget(t *testing.T) {
	max := 2
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "loop-resume",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:            "loop",
						Kind:          plan.KindLoop,
						While:         &plan.Expr{Op: "literal", Value: json.RawMessage(`true`)},
						InitState:     &plan.Expr{Op: "literal", Value: json.RawMessage(`{"passed":false}`)},
						MaxIterations: &max,
						OnExhausted:   "pause",
						BodyRef:       "body",
					},
				},
			},
			"body": {
				Nodes: []plan.Node{
					{
						ID:   "set",
						Kind: plan.KindProject,
						Inputs: map[string]plan.Binding{
							"passed": {Expr: &plan.Expr{Op: "literal", Value: json.RawMessage(`false`)}},
						},
					},
				},
				Output: map[string]plan.Binding{
					"passed": {Ref: &plan.ValueRef{Node: "set", Port: "passed"}},
				},
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
		t.Fatalf("first Run: %v", err)
	}
	run, _ := store.GetRun(runID)
	if run.State != plan.RunPaused {
		t.Fatalf("expected paused, got %q", run.State)
	}

	max4 := 4
	p.Subgraphs["root"].Nodes[0].MaxIterations = &max4
	// Deliberately compile a new version: a paused run still owns the old version.
	p.Digest = ""
	if err := engine.RepinRunPlan(runID, p); err == nil {
		t.Fatal("changed plan was accepted for a paused run")
	}
	pinned, err := store.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	var original plan.Plan
	if err := json.Unmarshal(pinned.PlanJSON, &original); err != nil {
		t.Fatal(err)
	}
	if *original.Subgraphs["root"].Nodes[0].MaxIterations != 2 {
		t.Fatal("paused run lost its original iteration budget")
	}
}
