package plan_test

import (
	"encoding/json"
	"strings"
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

func TestLoopZeroMaxIterationsIsRejected(t *testing.T) {
	for _, badMax := range []int{0, -1} {
		maxVal := badMax
		p := &plan.Plan{
			IRVersion: "1.0",
			Name:      "loop-zero",
			Root:      "root",
			Subgraphs: map[string]plan.Subgraph{
				"root": {
					Nodes: []plan.Node{
						{
							ID:            "loop",
							Kind:          plan.KindLoop,
							While:         &plan.Expr{Op: "literal", Value: json.RawMessage(`true`)},
							MaxIterations: &maxVal,
							OnExhausted:   "fail",
							BodyRef:       "body",
						},
					},
				},
				"body": {
					Nodes: []plan.Node{
						{
							ID:   "step",
							Kind: plan.KindProject,
							Inputs: map[string]plan.Binding{
								"v": {Expr: &plan.Expr{Op: "literal", Value: json.RawMessage(`1`)}},
							},
						},
					},
				},
			},
		}
		errs := plan.Validate(p)
		if !plan.HasErrors(errs) {
			t.Fatalf("maxIterations=%d was accepted; expected MUXLIO_UNBOUNDED_LOOP error", badMax)
		}
		found := false
		for _, e := range errs {
			if e.Code == "MUXLIO_UNBOUNDED_LOOP" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected MUXLIO_UNBOUNDED_LOOP for maxIterations=%d, got: %+v", badMax, errs)
		}
	}
}

func TestLoopExhaustedFailsRun(t *testing.T) {
	max := 2
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "loop-fail",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:            "loop",
						Kind:          plan.KindLoop,
						While:         &plan.Expr{Op: "literal", Value: json.RawMessage(`true`)},
						InitState:     &plan.Expr{Op: "literal", Value: json.RawMessage(`0`)},
						MaxIterations: &max,
						OnExhausted:   "fail",
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
							"v": {Expr: &plan.Expr{Op: "literal", Value: json.RawMessage(`1`)}},
						},
					},
				},
				Output: map[string]plan.Binding{
					"v": {Ref: &plan.ValueRef{Node: "set", Port: "v"}},
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
	err = engine.Run(runID)
	if err == nil || !strings.Contains(err.Error(), "loop \"loop\" exhausted after 2 iterations") {
		t.Fatalf("expected loop exhaustion failure, got: %v", err)
	}
	run, _ := store.GetRun(runID)
	if run.State != plan.RunFailed {
		t.Fatalf("expected failed, got %q", run.State)
	}
}

func TestLoopNonBooleanWhileAtExhaustionBoundaryFails(t *testing.T) {
	max := 1
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "loop-non-bool-while",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:            "loop",
						Kind:          plan.KindLoop,
						While:         &plan.Expr{Op: "state"},
						InitState:     &plan.Expr{Op: "literal", Value: json.RawMessage(`true`)},
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
							"v": {Expr: &plan.Expr{Op: "literal", Value: json.RawMessage(`"not-a-boolean"`)}},
						},
					},
				},
				Output: map[string]plan.Binding{
					"v": {Ref: &plan.ValueRef{Node: "set", Port: "v"}},
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
	err = engine.Run(runID)
	if err == nil || !strings.Contains(err.Error(), "loop while must be boolean, got string") {
		t.Fatalf("expected boolean type error, got: %v", err)
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
