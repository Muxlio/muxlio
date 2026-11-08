// Package conformance_test implements the M0 conformance tests for the
// Muxlio Plan IR interpreter. These verify stable scope identity, output
// reference resolution, branch/loop semantics, duplicate key rejection,
// maxItems enforcement, and event generation — the dataflow invariants
// the design requires before building on top of the engine.
package conformance_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// --- expression and binding helpers ---

func exprInput() *plan.Expr { return &plan.Expr{Op: "input"} }
func exprItem() *plan.Expr  { return &plan.Expr{Op: "item"} }
func exprState() *plan.Expr { return &plan.Expr{Op: "state"} }

func exprField(e *plan.Expr, name string) *plan.Expr {
	return &plan.Expr{Op: "field", Expr: e, Name: name}
}

func exprLiteral(v any) *plan.Expr {
	data, _ := json.Marshal(v)
	return &plan.Expr{Op: "literal", Value: data}
}

func exprEq(l, r *plan.Expr) *plan.Expr  { return &plan.Expr{Op: "eq", Left: l, Right: r} }
func exprLt(l, r *plan.Expr) *plan.Expr  { return &plan.Expr{Op: "lt", Left: l, Right: r} }
func exprLen(e *plan.Expr) *plan.Expr    { return &plan.Expr{Op: "len", Expr: e} }
func exprAdd(l, r *plan.Expr) *plan.Expr { return &plan.Expr{Op: "add", Left: l, Right: r} }

func bRef(node, port string) plan.Binding {
	return plan.Binding{Ref: &plan.ValueRef{Node: node, Port: port}}
}

func bExpr(e *plan.Expr) plan.Binding { return plan.Binding{Expr: e} }

func ptr(n int) *int { return &n }

// --- Test 1: simple execute with output reference ---

func TestSimpleExecute(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "simple",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:       "step",
						Kind:     "execute",
						Executor: "test/echo",
						Outputs: map[string]plan.OutputDecl{
							"result": {Mime: "text/plain"},
						},
					},
				},
				Output: map[string]plan.Binding{
					"result": bRef("step", "result"),
				},
			},
		},
	}

	exec := workers.NewFake().ByNode("step", workers.ExecuteResult{
		Outputs: map[string]any{"result": "hello"},
	})

	engine := plan.NewEngine(p, exec)
	input, _ := json.Marshal(map[string]any{})
	runID, err := engine.CreateRun(input)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := engine.Run(runID); err != nil {
		t.Fatalf("Run: %v", err)
	}

	run, _ := engine.Store().GetRun(runID)
	if run.State != "succeeded" {
		t.Fatalf("expected succeeded, got %s", run.State)
	}
	if run.Output != "hello" {
		t.Fatalf("expected output %q, got %v", "hello", run.Output)
	}
}

func TestChildNestingAndOutputResolution(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0", Name: "child", Root: "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {Nodes: []plan.Node{
				{ID: "invoke", Kind: plan.KindChild, ChildRef: "middle", MaxDepth: ptr(3)},
			}, Output: map[string]plan.Binding{"value": bRef("invoke", "result")}},
			"middle": {Nodes: []plan.Node{
				{ID: "nested", Kind: plan.KindChild, ChildRef: "leaf", MaxDepth: ptr(3)},
			}, Output: map[string]plan.Binding{"value": bRef("nested", "result")}},
			"leaf": {Nodes: []plan.Node{
				{ID: "echo", Kind: plan.KindExecute, Executor: "test/echo", Outputs: map[string]plan.OutputDecl{"value": {}}},
			}, Output: map[string]plan.Binding{"value": bRef("echo", "value")}},
		},
	}
	exec := workers.NewFake().ByNode("echo", workers.ExecuteResult{Outputs: map[string]any{"value": "nested result"}})
	e := plan.NewEngine(p, exec)
	runID, err := e.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Run(runID); err != nil {
		t.Fatal(err)
	}
	run, err := e.Store().GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Output != "nested result" {
		t.Fatalf("output = %#v", run.Output)
	}
	acts, err := e.Store().ListActivations(runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 3 {
		t.Fatalf("expected three nested activations, got %d: %+v", len(acts), acts)
	}
	seen := map[string]bool{}
	for _, act := range acts {
		seen[act.ID] = true
	}
	if !seen[runID+":invoke"] || !seen[runID+":child%3Amiddle:invoke%3Ainvoke:nested"] || !seen[runID+":child%3Amiddle:invoke%3Ainvoke:child%3Aleaf:invoke%3Anested:echo"] {
		t.Fatalf("unexpected child activation scopes: %+v", acts)
	}
}

// TestChildDepthBoundIsEnforced verifies the runtime guard that bounds
// recursive child invocations of the same subgraph. A self-referential
// child must stop and fail the run when it reaches maxDepth, rather than
// recursing without limit.
func TestChildDepthBoundIsEnforced(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0", Name: "recursion", Root: "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {Nodes: []plan.Node{
				{ID: "start", Kind: plan.KindChild, ChildRef: "recurse", MaxDepth: ptr(2)},
			}, Output: map[string]plan.Binding{"value": bRef("start", "result")}},
			"recurse": {Nodes: []plan.Node{
				{ID: "self", Kind: plan.KindChild, ChildRef: "recurse", MaxDepth: ptr(2)},
			}, Output: map[string]plan.Binding{"value": bRef("self", "result")}},
		},
	}
	e := plan.NewEngine(p, workers.NewFake())
	runID, err := e.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Run(runID); err == nil {
		t.Fatal("expected run to fail when child recursion exceeds maxDepth, got nil")
	} else if !strings.Contains(err.Error(), "maxDepth") {
		t.Fatalf("expected maxDepth error, got %v", err)
	}
	run, err := e.Store().GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "failed" {
		t.Fatalf("expected run state failed, got %q", run.State)
	}
}

func TestChildValidationRequiresReferenceAndFiniteDepth(t *testing.T) {
	for _, tc := range []struct {
		name string
		node plan.Node
		code string
	}{
		{name: "missing ref", node: plan.Node{ID: "child", Kind: plan.KindChild, MaxDepth: ptr(1)}, code: "MUXLIO_MISSING_CHILD_REF"},
		{name: "unknown ref", node: plan.Node{ID: "child", Kind: plan.KindChild, ChildRef: "missing", MaxDepth: ptr(1)}, code: "MUXLIO_BAD_SUBGRAPH_REF"},
		{name: "missing depth", node: plan.Node{ID: "child", Kind: plan.KindChild, ChildRef: "body"}, code: "MUXLIO_UNBOUNDED_CHILD"},
		{name: "zero depth", node: plan.Node{ID: "child", Kind: plan.KindChild, ChildRef: "body", MaxDepth: ptr(0)}, code: "MUXLIO_UNBOUNDED_CHILD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &plan.Plan{IRVersion: "1.0", Name: "validation", Root: "root", Subgraphs: map[string]plan.Subgraph{"root": {Nodes: []plan.Node{tc.node}}, "body": {}}}
			for _, err := range plan.Validate(p) {
				if err.Code == tc.code {
					return
				}
			}
			t.Fatalf("expected validation code %s", tc.code)
		})
	}
}

func TestExecuteRetryAttemptsAreVisible(t *testing.T) {
	p := &plan.Plan{IRVersion: "1.0", Name: "attempts", Root: "root", Subgraphs: map[string]plan.Subgraph{"root": {Nodes: []plan.Node{{ID: "task", Kind: "execute", Executor: "test/echo", Retry: &plan.RetryPolicy{MaxAttempts: 2}}}}}}
	exec := &attemptSequenceExecutor{}
	e := plan.NewEngine(p, exec)
	runID, err := e.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Run(runID); err != nil {
		t.Fatal(err)
	}
	acts, err := e.Store().ListActivations(runID)
	if err != nil || len(acts) != 1 {
		t.Fatalf("activations=%+v err=%v", acts, err)
	}
	attempts, err := e.Store().ListExecutionAttempts(runID, acts[0].ID)
	if err != nil || len(attempts) != 2 || attempts[0].State != "failed" || attempts[1].State != "succeeded" {
		t.Fatalf("attempts=%+v err=%v", attempts, err)
	}
}

type attemptSequenceExecutor struct{ calls int }

func (e *attemptSequenceExecutor) Execute(workers.ExecuteRequest) (workers.ExecuteResult, error) {
	e.calls++
	if e.calls == 1 {
		return workers.ExecuteResult{Error: "transient"}, nil
	}
	return workers.ExecuteResult{Outputs: map[string]any{}}, nil
}

// --- Test 2: map fan-out with stable per-item scope ---

func TestMapFanOut(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "map-test",
		Root:      "root",
		InputSchema: []byte(`{
			"type": "object",
			"properties": {"items": {"type": "array"}},
			"required": ["items"]
		}`),
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:       "items",
						Kind:     "map",
						Items:    exprField(exprInput(), "items"),
						Key:      exprField(exprItem(), "id"),
						MaxItems: ptr(100),
						BodyRef:  "itemBody",
					},
				},
				Output: map[string]plan.Binding{
					"result": bRef("items", "items"),
				},
			},
			"itemBody": {
				Nodes: []plan.Node{
					{
						ID:       "process",
						Kind:     "execute",
						Executor: "test/echo",
						Inputs: map[string]plan.Binding{
							"id": bExpr(exprField(exprItem(), "id")),
						},
						Outputs: map[string]plan.OutputDecl{
							"echo": {},
						},
					},
				},
				Output: map[string]plan.Binding{
					"id":   bExpr(exprField(exprItem(), "id")),
					"echo": bRef("process", "echo"),
				},
			},
		},
	}

	exec := workers.NewFake().ByExecutor("test/echo", workers.ExecuteResult{
		Outputs: map[string]any{"echo": "processed"},
	})

	engine := plan.NewEngine(p, exec)
	input, _ := json.Marshal(map[string]any{
		"items": []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
		},
	})
	runID, err := engine.CreateRun(input)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := engine.Run(runID); err != nil {
		t.Fatalf("Run: %v", err)
	}

	run, _ := engine.Store().GetRun(runID)
	if run.State != "succeeded" {
		t.Fatalf("expected succeeded, got %s", run.State)
	}

	output, ok := run.Output.([]any)
	if !ok {
		t.Fatalf("expected array output, got %T", run.Output)
	}
	if len(output) != 3 {
		t.Fatalf("expected 3 items, got %d", len(output))
	}
	for i, item := range output {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("item %d: expected map, got %T", i, item)
		}
		expectedID := string([]byte{'a' + byte(i)})
		if m["id"] != expectedID {
			t.Errorf("item %d: expected id %q, got %v", i, expectedID, m["id"])
		}
		if m["echo"] != "processed" {
			t.Errorf("item %d: expected echo %q, got %v", i, "processed", m["echo"])
		}
	}

	// Verify each item got its own activation with a stable scope path.
	events, _ := engine.Store().GetEvents(runID, 0, 0)
	var mapExpandCount int
	for _, e := range events {
		if e.Type == "map.expanded" {
			mapExpandCount++
		}
	}
	if mapExpandCount != 1 {
		t.Errorf("expected 1 map.expanded event, got %d", mapExpandCount)
	}
}

// --- Test 3: branch selects the true path ---

func TestBranchTrue(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "branch-true",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:        "check",
						Kind:      "branch",
						Condition: exprEq(exprLiteral("yes"), exprLiteral("yes")),
						ThenRef:   "thenPath",
						ElseRef:   "elsePath",
					},
				},
				Output: map[string]plan.Binding{
					"result": bRef("check", "result"),
				},
			},
			"thenPath": {
				Nodes: []plan.Node{
					{
						ID:       "then",
						Kind:     "execute",
						Executor: "test/then",
						Outputs:  map[string]plan.OutputDecl{"value": {}},
					},
				},
				Output: map[string]plan.Binding{
					"value": bRef("then", "value"),
				},
			},
			"elsePath": {
				Nodes: []plan.Node{
					{
						ID:       "else",
						Kind:     "execute",
						Executor: "test/else",
						Outputs:  map[string]plan.OutputDecl{"value": {}},
					},
				},
				Output: map[string]plan.Binding{
					"value": bRef("else", "value"),
				},
			},
		},
	}

	exec := workers.NewFake().
		ByNode("then", workers.ExecuteResult{Outputs: map[string]any{"value": "then-val"}}).
		ByNode("else", workers.ExecuteResult{Outputs: map[string]any{"value": "else-val"}})

	engine := plan.NewEngine(p, exec)
	input, _ := json.Marshal(map[string]any{})
	runID, err := engine.CreateRun(input)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := engine.Run(runID); err != nil {
		t.Fatalf("Run: %v", err)
	}

	run, _ := engine.Store().GetRun(runID)
	if run.State != "succeeded" {
		t.Fatalf("expected succeeded, got %s", run.State)
	}
	if run.Output != "then-val" {
		t.Fatalf("expected then-val, got %v", run.Output)
	}

	// Verify the else executor was never called.
	calls := exec.Calls()
	for _, c := range calls {
		if c.Executor == "test/else" {
			t.Fatal("else executor should not have been called")
		}
	}
}

// --- Test 4: branch selects the false path ---

func TestBranchFalse(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "branch-false",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:        "check",
						Kind:      "branch",
						Condition: exprEq(exprLiteral("yes"), exprLiteral("no")),
						ThenRef:   "thenPath",
						ElseRef:   "elsePath",
					},
				},
				Output: map[string]plan.Binding{
					"result": bRef("check", "result"),
				},
			},
			"thenPath": {
				Nodes: []plan.Node{
					{ID: "then", Kind: "execute", Executor: "test/then",
						Outputs: map[string]plan.OutputDecl{"value": {}}},
				},
				Output: map[string]plan.Binding{"value": bRef("then", "value")},
			},
			"elsePath": {
				Nodes: []plan.Node{
					{ID: "else", Kind: "execute", Executor: "test/else",
						Outputs: map[string]plan.OutputDecl{"value": {}}},
				},
				Output: map[string]plan.Binding{"value": bRef("else", "value")},
			},
		},
	}

	exec := workers.NewFake().
		ByNode("then", workers.ExecuteResult{Outputs: map[string]any{"value": "then-val"}}).
		ByNode("else", workers.ExecuteResult{Outputs: map[string]any{"value": "else-val"}})

	engine := plan.NewEngine(p, exec)
	input, _ := json.Marshal(map[string]any{})
	runID, _ := engine.CreateRun(input)
	if err := engine.Run(runID); err != nil {
		t.Fatalf("Run: %v", err)
	}

	run, _ := engine.Store().GetRun(runID)
	if run.Output != "else-val" {
		t.Fatalf("expected else-val, got %v", run.Output)
	}
}

// --- Test 5: loop with carried state ---

func TestLoopWithState(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "loop-test",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:            "count",
						Kind:          "loop",
						While:         exprLt(exprState(), exprLiteral(5.0)),
						InitState:     exprLiteral(0.0),
						MaxIterations: ptr(10),
						BodyRef:       "body",
						StateRef:      "next",
					},
				},
				Output: map[string]plan.Binding{
					"result": bRef("count", "result"),
				},
			},
			"body": {
				Nodes: []plan.Node{
					{
						ID:       "incr",
						Kind:     "execute",
						Executor: "test/incr",
						Inputs: map[string]plan.Binding{
							"current": bExpr(exprState()),
						},
						Outputs: map[string]plan.OutputDecl{"next": {}},
					},
				},
				Output: map[string]plan.Binding{
					"next": bRef("incr", "next"),
				},
			},
		},
	}

	// Use a custom executor that increments the input.
	var exec workers.Executor = &incrementExecutor{}

	engine := plan.NewEngine(p, exec)
	input, _ := json.Marshal(map[string]any{})
	runID, _ := engine.CreateRun(input)
	if err := engine.Run(runID); err != nil {
		t.Fatalf("Run: %v", err)
	}

	run, _ := engine.Store().GetRun(runID)
	if run.State != "succeeded" {
		t.Fatalf("expected succeeded, got %s", run.State)
	}
	// The loop runs while state < 5, starting at 0: iterations 0→1→2→3→4→5 (stops)
	// Final state should be 5.
	if run.Output != 5.0 {
		t.Fatalf("expected final state 5, got %v", run.Output)
	}
}

// incrementExecutor increments the "current" input by 1.
type incrementExecutor struct{}

func (incrementExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	number, ok := req.Inputs["current"].(json.Number)
	if !ok {
		return workers.ExecuteResult{}, fmt.Errorf("expected JSON number, got %T", req.Inputs["current"])
	}
	current, err := number.Float64()
	if err != nil {
		return workers.ExecuteResult{}, err
	}
	return workers.ExecuteResult{
		Outputs: map[string]any{"next": current + 1},
	}, nil
}

// --- Test 6: duplicate map keys are rejected ---

func TestDuplicateMapKey(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "dup-key",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:       "items",
						Kind:     "map",
						Items:    exprField(exprInput(), "items"),
						Key:      exprField(exprItem(), "id"),
						MaxItems: ptr(100),
						BodyRef:  "body",
					},
				},
				Output: map[string]plan.Binding{"result": bRef("items", "items")},
			},
			"body": {
				Nodes: []plan.Node{
					{ID: "noop", Kind: "execute", Executor: "test/noop",
						Outputs: map[string]plan.OutputDecl{"out": {}}},
				},
				Output: map[string]plan.Binding{"out": bRef("noop", "out")},
			},
		},
	}

	exec := workers.NewFake()
	engine := plan.NewEngine(p, exec)
	input, _ := json.Marshal(map[string]any{
		"items": []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "a"}, // duplicate!
		},
	})
	runID, _ := engine.CreateRun(input)
	err := engine.Run(runID)
	if err == nil {
		t.Fatal("expected error for duplicate keys, got nil")
	}
}

// --- Test 7: maxItems is enforced ---

func TestMaxItemsEnforced(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "max-items",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:       "items",
						Kind:     "map",
						Items:    exprField(exprInput(), "items"),
						MaxItems: ptr(2),
						BodyRef:  "body",
					},
				},
				Output: map[string]plan.Binding{"result": bRef("items", "items")},
			},
			"body": {
				Nodes: []plan.Node{
					{ID: "noop", Kind: "execute", Executor: "test/noop",
						Outputs: map[string]plan.OutputDecl{"out": {}}},
				},
				Output: map[string]plan.Binding{"out": bRef("noop", "out")},
			},
		},
	}

	exec := workers.NewFake()
	engine := plan.NewEngine(p, exec)
	input, _ := json.Marshal(map[string]any{
		"items": []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"}, // exceeds maxItems=2
		},
	})
	runID, _ := engine.CreateRun(input)
	err := engine.Run(runID)
	if err == nil {
		t.Fatal("expected error for exceeding maxItems, got nil")
	}
}

// --- Test 8: dependency ordering — nodes process in topological order ---

func TestDependencyOrdering(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "deps",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					// "second" depends on "first" via input binding.
					{ID: "first", Kind: "execute", Executor: "test/first",
						Outputs: map[string]plan.OutputDecl{"v": {}}},
					{ID: "second", Kind: "execute", Executor: "test/second",
						Inputs:  map[string]plan.Binding{"v": bRef("first", "v")},
						Outputs: map[string]plan.OutputDecl{"v": {}}},
				},
				Output: map[string]plan.Binding{"result": bRef("second", "v")},
			},
		},
	}

	exec := workers.NewFake().
		ByNode("first", workers.ExecuteResult{Outputs: map[string]any{"v": "first-val"}}).
		ByNode("second", workers.ExecuteResult{Outputs: map[string]any{"v": "second-val"}})

	engine := plan.NewEngine(p, exec)
	input, _ := json.Marshal(map[string]any{})
	runID, _ := engine.CreateRun(input)
	if err := engine.Run(runID); err != nil {
		t.Fatalf("Run: %v", err)
	}

	run, _ := engine.Store().GetRun(runID)
	if run.Output != "second-val" {
		t.Fatalf("expected second-val, got %v", run.Output)
	}

	// Verify both executors were called.
	calls := exec.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 executor calls, got %d", len(calls))
	}
}

// --- Test 9: events are generated for state transitions ---

func TestEventGeneration(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "events",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{ID: "step", Kind: "execute", Executor: "test/echo",
						Outputs: map[string]plan.OutputDecl{"result": {}}},
				},
				Output: map[string]plan.Binding{"result": bRef("step", "result")},
			},
		},
	}

	exec := workers.NewFake().ByNode("step", workers.ExecuteResult{
		Outputs: map[string]any{"result": "done"},
	})

	engine := plan.NewEngine(p, exec)
	input, _ := json.Marshal(map[string]any{})
	runID, _ := engine.CreateRun(input)
	_ = engine.Run(runID)

	events, _ := engine.Store().GetEvents(runID, 0, 0)
	if len(events) < 4 {
		t.Fatalf("expected at least 4 events, got %d", len(events))
	}

	// Check for expected event types.
	types := make(map[string]bool)
	for _, e := range events {
		types[e.Type] = true
	}
	expected := []string{"run.created", "run.running", "run.succeeded", "activation.queued", "activation.succeeded"}
	for _, exp := range expected {
		if !types[exp] {
			t.Errorf("missing event type %q", exp)
		}
	}
}

// --- Test 10: plan validation catches missing root subgraph ---

func TestValidationMissingRoot(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "bad",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{}, // empty!
	}
	verrs := plan.Validate(p)
	if len(verrs) == 0 {
		t.Fatal("expected validation errors for missing root subgraph")
	}
}

// --- Test 11: plan validation catches unknown node kind ---

func TestValidationUnknownKind(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "bad-kind",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{ID: "bad", Kind: "nonexistent"},
				},
			},
		},
	}
	verrs := plan.Validate(p)
	found := false
	for _, ve := range verrs {
		if ve.Code == "MUXLIO_UNKNOWN_NODE_KIND" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected MUXLIO_UNKNOWN_NODE_KIND validation error")
	}
}

// --- Test 12: plan validation rejects unbounded loops ---

func TestValidationUnboundedLoop(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "unbounded",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{ID: "loop", Kind: "loop", While: exprLiteral(true),
						BodyRef: "body"},
				},
			},
			"body": {
				Nodes: []plan.Node{
					{ID: "step", Kind: "execute", Executor: "test/x",
						Outputs: map[string]plan.OutputDecl{"v": {}}},
				},
				Output: map[string]plan.Binding{"v": bRef("step", "v")},
			},
		},
	}
	verrs := plan.Validate(p)
	found := false
	for _, ve := range verrs {
		if ve.Code == "MUXLIO_UNBOUNDED_LOOP" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected MUXLIO_UNBOUNDED_LOOP validation error")
	}
}

// TestRetrySkipsSucceededActivations verifies that Engine.Retry re-runs
// failed/cancelled activations while skipping nodes that already succeeded,
// and that the run reaches succeeded on the second attempt.
func TestRetrySkipsSucceededActivations(t *testing.T) {
	// Plan: two sequential execute nodes. The fake executor fails the second
	// node on the first attempt, then succeeds on the second.
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "retry-test",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{ID: "step1", Kind: plan.KindExecute, Executor: "test/echo",
						Outputs: map[string]plan.OutputDecl{"out": {}}},
					{ID: "step2", Kind: plan.KindExecute, Executor: "test/echo",
						Inputs:  map[string]plan.Binding{"in": {Ref: &plan.ValueRef{Node: "step1", Port: "out"}}},
						Outputs: map[string]plan.OutputDecl{"result": {}}},
				},
				Output: map[string]plan.Binding{
					"result": {Ref: &plan.ValueRef{Node: "step2", Port: "result"}},
				},
			},
		},
	}

	fake := workers.NewFake().
		ByNode("step1", workers.ExecuteResult{Outputs: map[string]any{"out": "hello"}}).
		FailOnNode("step2", fmt.Errorf("out of credits"), 1) // fail first call only

	e := plan.NewEngine(p, fake)
	input, _ := json.Marshal(map[string]any{})

	runID, err := e.CreateRun(input)
	if err != nil {
		t.Fatal(err)
	}

	// First run — step2 fails.
	_ = e.Run(runID)
	run, _ := e.Store().GetRun(runID)
	if run.State != "failed" {
		t.Fatalf("expected failed after first run, got %q", run.State)
	}

	// Verify step1 succeeded and is preserved.
	acts, _ := e.Store().ListActivations(runID)
	step1calls := 0
	for _, a := range acts {
		if a.NodeID == "step1" && a.State == "succeeded" {
			step1calls++
		}
	}
	if step1calls != 1 {
		t.Fatalf("expected step1 succeeded once before retry, got %d", step1calls)
	}

	// Retry — step2 now succeeds (FailOnNode maxCalls=1 already consumed).
	if err := e.Retry(runID); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if err := e.Run(runID); err != nil {
		t.Fatalf("Run after retry: %v", err)
	}
	run, _ = e.Store().GetRun(runID)
	if run.State != "succeeded" {
		t.Fatalf("expected succeeded after retry, got %q", run.State)
	}

	// step1 should have been called exactly once total (skipped on retry).
	calls := fake.Calls()
	step1Count := 0
	for _, c := range calls {
		if c.NodeID == "step1" {
			step1Count++
		}
	}
	if step1Count != 1 {
		t.Fatalf("step1 should be called exactly once (skipped on retry), got %d", step1Count)
	}
}
