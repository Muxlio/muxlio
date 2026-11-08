package conformance_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// Probe: a child subgraph with MULTIPLE output ports should not unwrap;
// the child node's "result" port should carry the full map.
func TestProbeChildMultiPortOutput(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0", Name: "multi", Root: "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {Nodes: []plan.Node{
				{ID: "c", Kind: plan.KindChild, ChildRef: "body", MaxDepth: ptr(2)},
			}, Output: map[string]plan.Binding{"value": bRef("c", "result")}},
			"body": {Nodes: []plan.Node{
				{ID: "a", Kind: plan.KindExecute, Executor: "test/x", Outputs: map[string]plan.OutputDecl{"v": {}}},
				{ID: "b", Kind: plan.KindExecute, Executor: "test/y", Outputs: map[string]plan.OutputDecl{"v": {}}},
			}, Output: map[string]plan.Binding{"first": bRef("a", "v"), "second": bRef("b", "v")}},
		},
	}
	exec := workers.NewFake().
		ByNode("a", workers.ExecuteResult{Outputs: map[string]any{"v": 1.0}}).
		ByNode("b", workers.ExecuteResult{Outputs: map[string]any{"v": 2.0}})
	e := plan.NewEngine(p, exec)
	runID, err := e.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Run(runID); err != nil {
		t.Fatal(err)
	}
	run, _ := e.Store().GetRun(runID)
	m, ok := run.Output.(map[string]any)
	if !ok {
		t.Fatalf("expected map output (no unwrap), got %T: %#v", run.Output, run.Output)
	}
	if m["first"] != 1.0 || m["second"] != 2.0 {
		t.Fatalf("unexpected output: %#v", m)
	}
}

// Probe: a child node nested inside a map body should inherit the current
// item, exercising context propagation across control-flow boundaries.
func TestProbeChildInheritsMapItem(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0", Name: "childinmap", Root: "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {Nodes: []plan.Node{
				{ID: "fan", Kind: plan.KindMap, Items: exprLiteral([]any{1.0, 2.0}), MaxItems: ptr(10), BodyRef: "itemBody"},
			}, Output: map[string]plan.Binding{"value": bRef("fan", "items")}},
			"itemBody": {Nodes: []plan.Node{
				{ID: "kid", Kind: plan.KindChild, ChildRef: "echo", MaxDepth: ptr(1)},
			}, Output: map[string]plan.Binding{"value": bRef("kid", "result")}},
			"echo": {Nodes: []plan.Node{
				{ID: "do", Kind: plan.KindExecute, Executor: "test/echo",
					Inputs:  map[string]plan.Binding{"n": bExpr(exprItem())},
					Outputs: map[string]plan.OutputDecl{"out": {}}},
			}, Output: map[string]plan.Binding{"value": bRef("do", "out")}},
		},
	}
	exec := &itemEchoExecutor{}
	e := plan.NewEngine(p, exec)
	runID, err := e.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Run(runID); err != nil {
		t.Fatal(err)
	}
	run, _ := e.Store().GetRun(runID)
	out, ok := run.Output.([]any)
	if !ok {
		t.Fatalf("expected array, got %T: %#v", run.Output, run.Output)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 items, got %d: %#v", len(out), out)
	}
	for i, v := range out {
		n, ok := v.(float64)
		if !ok || n != float64(i+1) {
			t.Fatalf("item %d: expected %d, got %#v", i, i+1, v)
		}
	}
}

type itemEchoExecutor struct{}

func (itemEchoExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	number, ok := req.Inputs["n"].(json.Number)
	if !ok {
		return workers.ExecuteResult{}, fmt.Errorf("expected JSON number, got %T", req.Inputs["n"])
	}
	n, err := number.Float64()
	if err != nil {
		return workers.ExecuteResult{}, err
	}
	return workers.ExecuteResult{Outputs: map[string]any{"out": n}}, nil
}

// Probe: mutual recursion between two distinct subgraphs must terminate at
// the per-subgraph depth bound, not recurse forever.
func TestProbeMutualRecursionTerminates(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0", Name: "mutual", Root: "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {Nodes: []plan.Node{
				{ID: "start", Kind: plan.KindChild, ChildRef: "a", MaxDepth: ptr(2)},
			}, Output: map[string]plan.Binding{"value": bRef("start", "result")}},
			"a": {Nodes: []plan.Node{
				{ID: "toB", Kind: plan.KindChild, ChildRef: "b", MaxDepth: ptr(2)},
			}, Output: map[string]plan.Binding{"value": bRef("toB", "result")}},
			"b": {Nodes: []plan.Node{
				{ID: "toA", Kind: plan.KindChild, ChildRef: "a", MaxDepth: ptr(2)},
			}, Output: map[string]plan.Binding{"value": bRef("toA", "result")}},
		},
	}
	e := plan.NewEngine(p, workers.NewFake())
	runID, err := e.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	err = e.Run(runID)
	if err == nil {
		t.Fatal("expected mutual recursion to hit maxDepth and fail, got nil")
	}
	run, _ := e.Store().GetRun(runID)
	if run.State != "failed" {
		t.Fatalf("expected failed, got %q", run.State)
	}
}

// Probe: maxDepth=1 must permit exactly one (non-recursive) invocation and
// succeed.
func TestProbeMaxDepthOneNonRecursive(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0", Name: "one", Root: "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {Nodes: []plan.Node{
				{ID: "c", Kind: plan.KindChild, ChildRef: "leaf", MaxDepth: ptr(1)},
			}, Output: map[string]plan.Binding{"value": bRef("c", "result")}},
			"leaf": {Nodes: []plan.Node{
				{ID: "echo", Kind: plan.KindExecute, Executor: "test/echo", Outputs: map[string]plan.OutputDecl{"v": {}}},
			}, Output: map[string]plan.Binding{"value": bRef("echo", "v")}},
		},
	}
	exec := workers.NewFake().ByNode("echo", workers.ExecuteResult{Outputs: map[string]any{"v": "ok"}})
	e := plan.NewEngine(p, exec)
	runID, err := e.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Run(runID); err != nil {
		t.Fatalf("expected success with maxDepth=1 non-recursive, got %v", err)
	}
	run, _ := e.Store().GetRun(runID)
	if run.Output != "ok" {
		t.Fatalf("expected ok, got %#v", run.Output)
	}
}
