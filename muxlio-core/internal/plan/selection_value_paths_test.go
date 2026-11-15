package plan

import (
	"reflect"
	"sort"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func selectionRef(node string) *Expr               { return &Expr{Op: "ref", Node: node, Port: "result"} }
func selectionField(expr *Expr, name string) *Expr { return &Expr{Op: "field", Expr: expr, Name: name} }

func seedSelectionValue(t *testing.T, s state.Store, node string, scope []string, executor string) string {
	t.Helper()
	id := activationID("run", scope, node)
	if err := s.CreateActivation(&state.Activation{ID: id, RunID: "run", NodeID: node, ScopePath: scope, State: ActSucceeded}); err != nil {
		t.Fatal(err)
	}
	if executor != "" {
		if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: id + "-attempt", RunID: "run", ActivationID: id, Executor: executor, State: "succeeded"}); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func assertSelectionValue(t *testing.T, e *Engine, scope []string, expr *Expr, want ...string) {
	t.Helper()
	history, err := e.store.ListExecutionAttempts("run", "")
	if err != nil {
		t.Fatal(err)
	}
	deps, err := e.selectionDependencies("run", &Node{ID: "review", Inputs: map[string]Binding{"value": {Expr: expr}}}, scope, history)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for id := range deps {
		got = append(got, id)
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) || len(got) > 0 && !reflect.DeepEqual(got, want) {
		t.Fatalf("value producers=%v, want %v", got, want)
	}
}

func TestSelectionDependenciesFollowNestedFieldsAndIndices(t *testing.T) {
	p := &Plan{Subgraphs: map[string]Subgraph{"root": {Nodes: []Node{
		{ID: "a", Kind: KindExecute}, {ID: "b", Kind: KindExecute}, {ID: "c", Kind: KindExecute},
		{ID: "project", Kind: KindProject, Inputs: map[string]Binding{"result": {Expr: &Expr{Op: "object", Fields: map[string]*Expr{
			"values": {Op: "array", Elements: []*Expr{selectionRef("a"), selectionRef("b")}}, "0": selectionRef("c"),
		}}}}},
	}}}}
	s := state.New()
	a := seedSelectionValue(t, s, "a", nil, "agent/a")
	b := seedSelectionValue(t, s, "b", nil, "agent/b")
	c := seedSelectionValue(t, s, "c", nil, "agent/c")
	seedSelectionValue(t, s, "project", nil, "")
	e := NewEngineWithStore(p, workers.NewFake(), s)
	value := &Expr{Op: "index", Index: 1, Expr: selectionField(selectionRef("project"), "values")}
	assertSelectionValue(t, e, nil, value, b)
	assertSelectionValue(t, e, nil, selectionField(selectionRef("project"), "0"), c)
	assertSelectionValue(t, e, nil, &Expr{Op: "array", Elements: []*Expr{value, {Op: "index", Index: 0, Expr: selectionField(selectionRef("project"), "values")}}}, a, b)
}

func TestSelectionDependenciesFollowStateInsideAndAfterLoops(t *testing.T) {
	stateField := func(name string) *Expr { return selectionField(&Expr{Op: "state"}, name) }
	p := &Plan{Root: "root", Subgraphs: map[string]Subgraph{
		"root": {Nodes: []Node{{ID: "a", Kind: KindExecute}, {ID: "b", Kind: KindExecute}, {ID: "iterate", Kind: KindLoop, BodyRef: "body", StateRef: "result", InitState: &Expr{Op: "object", Fields: map[string]*Expr{"text": selectionRef("b"), "audit": selectionRef("a")}}}}},
		"body": {Nodes: []Node{
			{ID: "advance", Kind: KindProject, Inputs: map[string]Binding{"result": {Expr: &Expr{Op: "object", Fields: map[string]*Expr{"text": stateField("text"), "audit": stateField("audit")}}}}},
			{ID: "choose", Kind: KindBranch, ThenRef: "leaf"}, {ID: "batch", Kind: KindMap, BodyRef: "leaf"},
		}, Output: map[string]Binding{"result": {Expr: selectionRef("advance")}}},
		"leaf": {},
	}}
	s := state.New()
	a := seedSelectionValue(t, s, "a", nil, "agent/a")
	b := seedSelectionValue(t, s, "b", nil, "agent/b")
	seedSelectionValue(t, s, "iterate", nil, "")
	for _, frame := range []string{"loop:iterate:0", "loop:iterate:1", "loop:iterate:2"} {
		seedSelectionValue(t, s, "advance", []string{frame}, "")
	}
	e := NewEngineWithStore(p, workers.NewFake(), s)
	assertSelectionValue(t, e, nil, selectionField(selectionRef("iterate"), "text"), b)
	assertSelectionValue(t, e, nil, selectionField(selectionRef("iterate"), "audit"), a)
	assertSelectionValue(t, e, []string{"loop:iterate:0"}, stateField("text"), b)
	assertSelectionValue(t, e, []string{"loop:iterate:2", "branch:choose:true"}, stateField("text"), b)
	assertSelectionValue(t, e, []string{"loop:iterate:2", "map:batch:0"}, stateField("text"))
}

func TestSelectionDependenciesFollowStateAfterAnOverwrite(t *testing.T) {
	stateExpr := selectionField(&Expr{Op: "state"}, "text")
	p := &Plan{Root: "root", Subgraphs: map[string]Subgraph{
		"root":  {Nodes: []Node{{ID: "initial", Kind: KindExecute}, {ID: "iterate", Kind: KindLoop, BodyRef: "body", StateRef: "result", InitState: &Expr{Op: "object", Fields: map[string]*Expr{"text": selectionRef("initial")}}}}},
		"body":  {Nodes: []Node{{ID: "choose", Kind: KindBranch, ThenRef: "write", ElseRef: "carry"}}, Output: map[string]Binding{"result": {Expr: selectionRef("choose")}}},
		"write": {Nodes: []Node{{ID: "replacement", Kind: KindExecute}}, Output: map[string]Binding{"result": {Expr: &Expr{Op: "object", Fields: map[string]*Expr{"text": selectionRef("replacement")}}}}},
		"carry": {Output: map[string]Binding{"result": {Expr: &Expr{Op: "object", Fields: map[string]*Expr{"text": stateExpr}}}}},
	}}
	s := state.New()
	seedSelectionValue(t, s, "initial", nil, "agent/a")
	seedSelectionValue(t, s, "iterate", nil, "")
	seedSelectionValue(t, s, "choose", []string{"loop:iterate:0"}, "")
	seedSelectionValue(t, s, "choose", []string{"loop:iterate:1"}, "")
	b := seedSelectionValue(t, s, "replacement", []string{"loop:iterate:0", "branch:choose:true"}, "agent/b")
	if err := s.AppendEvent("run", state.EventBranchSelected, activationID("run", []string{"loop:iterate:1"}, "choose"), map[string]any{"branch": false}); err != nil {
		t.Fatal(err)
	}
	e := NewEngineWithStore(p, workers.NewFake(), s)
	assertSelectionValue(t, e, nil, selectionField(selectionRef("iterate"), "text"), b)
}

func TestSelectionDependenciesFollowExpressionOnlyLoopBodies(t *testing.T) {
	p := &Plan{Root: "root", Subgraphs: map[string]Subgraph{
		"root": {Nodes: []Node{{ID: "initial", Kind: KindExecute}, {ID: "replacement", Kind: KindExecute}, {ID: "iterate", Kind: KindLoop, BodyRef: "body", StateRef: "result", InitState: selectionRef("initial")}}},
		"body": {Output: map[string]Binding{"result": {Expr: selectionRef("replacement")}}},
	}}
	s := state.New()
	seedSelectionValue(t, s, "initial", nil, "agent/a")
	b := seedSelectionValue(t, s, "replacement", nil, "agent/b")
	id := seedSelectionValue(t, s, "iterate", nil, "")
	if err := s.AppendEvent("run", state.EventLoopIterated, id, map[string]any{"iteration": 2}); err != nil {
		t.Fatal(err)
	}
	e := NewEngineWithStore(p, workers.NewFake(), s)
	assertSelectionValue(t, e, nil, selectionRef("iterate"), b)
}
