package plan_test

import (
	"encoding/json"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestLoopPreservesEnclosingMapItem(t *testing.T) {
	for _, tc := range []struct {
		name      string
		limit     int
		exhausted bool
	}{
		{"condition-and-body", 2, false},
		{"condition-at-bound", 1, false},
		{"exhaustion-condition", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p plan.Plan
			err := json.Unmarshal([]byte(`{
  "irVersion":"1.0", "name":"map-loop-item", "root":"root",
  "subgraphs":{
    "root":{"nodes":[{
      "id":"items", "kind":"map", "items":{"op":"field","expr":{"op":"input"},"name":"items"},
      "key":{"op":"field","expr":{"op":"item"},"name":"id"}, "concurrency":1,"bodyRef":"mapped"
    }]},
    "mapped":{"nodes":[{
      "id":"refine", "kind":"loop", "maxIterations":2, "onExhausted":"pause", "bodyRef":"iteration",
      "initState":{"op":"literal","value":0},
      "while":{"op":"and",
        "left":{"op":"eq","left":{"op":"field","expr":{"op":"item"},"name":"enabled"},"right":{"op":"literal","value":true}},
        "right":{"op":"lt","left":{"op":"state"},"right":{"op":"literal","value":1}}
      }
    }]},
    "iteration":{"nodes":[{
      "id":"branch", "kind":"branch", "condition":{"op":"literal","value":true}, "thenRef":"inspect", "elseRef":"empty"
    }], "output":{"next":{"expr":{"op":"add","left":{"op":"state"},"right":{"op":"literal","value":1}}}}},
    "inspect":{"nodes":[{
      "id":"inspect", "kind":"execute", "executor":"test/inspect",
      "inputs":{"item":{"expr":{"op":"item"}},"kind":{"expr":{"op":"field","expr":{"op":"item"},"name":"kind"}}},
      "outputs":{"passed":{"mime":"application/json","schema":{"type":"boolean"}}}
    }]},
    "empty":{"nodes":[]}
  }
}`), &p)
			if err != nil {
				t.Fatal(err)
			}
			mapped := p.Subgraphs["mapped"]
			mapped.Nodes[0].MaxIterations = &tc.limit
			if tc.exhausted {
				mapped.Nodes[0].While = mapped.Nodes[0].While.Left
			}
			p.Subgraphs["mapped"] = mapped
			for _, diagnostic := range plan.Validate(&p) {
				if diagnostic.Severity == "error" {
					t.Fatalf("invalid fixture: %+v", diagnostic)
				}
			}
			executor := workers.NewFake().ByExecutor("test/inspect", workers.ExecuteResult{Outputs: map[string]any{"passed": true}})
			store := state.New()
			engine := plan.NewEngineWithStore(&p, executor, store)
			id, err := engine.CreateRun(json.RawMessage(`{"items":[{"id":"one","kind":"fixture-one","enabled":true},{"id":"two","kind":"fixture-two","enabled":true}]}`))
			if err != nil {
				t.Fatal(err)
			}
			if err := engine.Run(id); err != nil {
				t.Fatal(err)
			}
			run, _ := store.GetRun(id)
			wantState, wantCalls := plan.RunSucceeded, 2
			if tc.exhausted {
				wantState, wantCalls = plan.RunPaused, 1
			}
			if run.State != wantState {
				t.Fatalf("state = %s, want %s", run.State, wantState)
			}
			calls := executor.Calls()
			if len(calls) != wantCalls {
				t.Fatalf("executor calls = %d, want %d; enclosing map item was lost", len(calls), wantCalls)
			}
			for index, call := range calls {
				want := []string{"fixture-one", "fixture-two"}[index]
				item, ok := call.Inputs["item"].(map[string]any)
				if !ok || item["kind"] != want || call.Inputs["kind"] != want {
					t.Fatalf("resolved inputs = %#v, want enclosing item %s", call.Inputs, want)
				}
			}
		})
	}
}
