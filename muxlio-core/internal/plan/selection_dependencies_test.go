package plan

import (
	"reflect"
	"sort"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestSelectionDependenciesFollowStructuralOutputs(t *testing.T) {
	ref := func(node string) Binding { return Binding{Ref: &ValueRef{Node: node, Port: "result"}} }
	for _, kind := range []string{KindChild, KindMap, KindBranch, KindLoop} {
		t.Run(kind, func(t *testing.T) {
			producer := Node{ID: "writer", Kind: KindExecute, Executor: "agent/b"}
			container := Node{ID: "generate", Kind: kind, ChildRef: "body", BodyRef: "body", ThenRef: "body", StateRef: "result"}
			projection := Node{ID: "project", Kind: KindProject, Inputs: map[string]Binding{"result": ref("generate"), "noise": ref("noise")}}
			p := &Plan{Root: "root", Subgraphs: map[string]Subgraph{
				"root": {Nodes: []Node{container, {ID: "noise", Kind: KindExecute, Executor: "agent/a"}, projection}},
				"body": {Nodes: []Node{producer, {ID: "unrelated", Kind: KindExecute, Executor: "agent/a"}}, Output: map[string]Binding{"result": ref("writer")}},
			}}
			s := state.New()
			seed := func(node string, scope []string, executor string) {
				id := activationID("run", scope, node)
				if err := s.CreateActivation(&state.Activation{ID: id, RunID: "run", NodeID: node, ScopePath: scope, State: ActSucceeded}); err != nil {
					t.Fatal(err)
				}
				if executor != "" {
					if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: id + "-attempt", RunID: "run", ActivationID: id, Executor: executor, State: "succeeded"}); err != nil {
						t.Fatal(err)
					}
				}
			}
			seed("generate", nil, "")
			seed("project", nil, "")
			seed("noise", nil, "agent/a")
			var scope []string
			switch kind {
			case KindChild:
				scope = []string{"child:body", "invoke:generate"}
			case KindMap:
				scope = []string{"map:generate:item%3Aone"}
			case KindBranch:
				scope = []string{"branch:generate:true"}
			case KindLoop:
				seed("writer", []string{"loop:generate:0"}, "agent/c")
				scope = []string{"loop:generate:1"}
			}
			seed("writer", scope, "agent/b")
			seed("unrelated", scope, "agent/a")
			// The same child graph may be called elsewhere; its agents are not
			// producers for this invocation's result.
			if kind == KindChild {
				seed("writer", []string{"child:body", "invoke:other"}, "agent/c")
			}
			e := NewEngineWithStore(p, workers.NewFake(), s)
			history, err := s.ListExecutionAttempts("run", "")
			if err != nil {
				t.Fatal(err)
			}
			dependencies, err := e.selectionDependencies("run", &Node{ID: "review", Inputs: map[string]Binding{"text": ref("project")}}, nil, history)
			if err != nil {
				t.Fatal(err)
			}
			got := []string{}
			for id := range dependencies {
				got = append(got, id)
			}
			sort.Strings(got)
			want := []string{activationID("run", scope, "writer")}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("producers=%v, want %v", got, want)
			}
		})
	}
}

func TestSelectionDependenciesFollowNestedChildWithEscapedIDs(t *testing.T) {
	ref := func(node string) Binding { return Binding{Ref: &ValueRef{Node: node, Port: "result"}} }
	p := &Plan{Root: "root", Subgraphs: map[string]Subgraph{
		"root":  {Nodes: []Node{{ID: "outer:call", Kind: KindChild, ChildRef: "outer"}}},
		"outer": {Nodes: []Node{{ID: "inner:call", Kind: KindChild, ChildRef: "inner"}}},
		"inner": {Nodes: []Node{{ID: "writer", Kind: KindExecute, Executor: "agent/b"}}},
	}}
	s := state.New()
	scope := []string{"child:outer", "invoke:outer:call", "child:inner", "invoke:inner:call"}
	for _, a := range []state.Activation{
		{ID: activationID("run", nil, "outer:call"), NodeID: "outer:call"},
		{ID: activationID("run", scope[:2], "inner:call"), NodeID: "inner:call", ScopePath: scope[:2]},
		{ID: activationID("run", scope, "writer"), NodeID: "writer", ScopePath: scope},
	} {
		a.RunID = "run"
		a.State = ActSucceeded
		if err := s.CreateActivation(&a); err != nil {
			t.Fatal(err)
		}
	}
	history := []state.ExecutionAttempt{{ActivationID: activationID("run", scope, "writer"), Executor: "agent/b", State: "succeeded"}}
	e := NewEngineWithStore(p, workers.NewFake(), s)
	deps, err := e.selectionDependencies("run", &Node{ID: "review", Inputs: map[string]Binding{"text": ref("outer:call")}}, nil, history)
	if err != nil || !deps[history[0].ActivationID] || len(deps) != 1 {
		t.Fatalf("nested child producers=%v, %v", deps, err)
	}
}

func TestSelectionDependenciesKeepDistinctProjectionPorts(t *testing.T) {
	ref := func(node, port string) Binding { return Binding{Ref: &ValueRef{Node: node, Port: port}} }
	p := &Plan{Root: "root", Subgraphs: map[string]Subgraph{"root": {Nodes: []Node{
		{ID: "writer-a", Kind: KindExecute, Executor: "agent/a"},
		{ID: "writer-b", Kind: KindExecute, Executor: "agent/b"},
		{ID: "project/path", Kind: KindProject, Inputs: map[string]Binding{"value": ref("writer-a", "result")}},
		{ID: "project", Kind: KindProject, Inputs: map[string]Binding{"path/value": ref("writer-b", "result")}},
	}}}}
	s := state.New()
	for _, node := range p.Subgraphs["root"].Nodes {
		if err := s.CreateActivation(&state.Activation{ID: activationID("run", nil, node.ID), RunID: "run", NodeID: node.ID, State: ActSucceeded}); err != nil {
			t.Fatal(err)
		}
	}
	history := []state.ExecutionAttempt{
		{ActivationID: activationID("run", nil, "writer-a"), State: "succeeded", Executor: "agent/a"},
		{ActivationID: activationID("run", nil, "writer-b"), State: "succeeded", Executor: "agent/b"},
	}
	e := NewEngineWithStore(p, workers.NewFake(), s)
	deps, err := e.selectionDependencies("run", &Node{ID: "review", Inputs: map[string]Binding{"a": ref("project/path", "value"), "b": ref("project", "path/value")}}, nil, history)
	if err != nil || len(deps) != 2 || !deps[history[0].ActivationID] || !deps[history[1].ActivationID] {
		t.Fatalf("projection producers collided: %v, %v", deps, err)
	}
}
