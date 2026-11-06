package plan

import (
	"encoding/json"
	"testing"
)

func TestBindingPreflight(t *testing.T) {
	for _, tc := range []struct{ name, fragment, code, pointer string }{
		{"empty", `"inputs":{"x":{}}`, "MUXLIO_BAD_BINDING", "/inputs/x"},
		{"ambiguous", `"inputs":{"x":{"ref":{"node":"before","port":"value"},"expr":{"op":"input"}}}`, "MUXLIO_BAD_BINDING", "/inputs/x"},
		{"nested expression", `"inputs":{"x":{"expr":{"op":"object","fields":{"a/b":{"op":"array","elements":[{"op":"ref","node":"missing","port":"x"}]}}}}}`, "MUXLIO_BAD_REF", "/inputs/x/expr/fields/a~1b/elements/0/node"},
		{"project", `"inputs":{"x":{"ref":{"node":"missing","port":"x"}}}`, "MUXLIO_BAD_REF", "/inputs/x/ref/node"},
		{"condition", `"condition":{"op":"eq","left":{"op":"ref","node":"missing","port":"x"},"right":{"op":"literal","value":true}}`, "MUXLIO_BAD_REF", "/condition/left/node"},
		{"self", `"inputs":{"x":{"ref":{"node":"work","port":"x"}}}`, "MUXLIO_DEPENDENCY_CYCLE", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind := "execute"
			if tc.name == "project" {
				kind = "project"
			}
			raw := `{"irVersion":"1.0","name":"preflight","root":"root","subgraphs":{"root":{"nodes":[{"id":"before","kind":"execute","executor":"generic/task"},{"id":"work","kind":"` + kind + `","executor":"generic/task",` + tc.fragment + `}]}}}`
			var p Plan
			if err := json.Unmarshal([]byte(raw), &p); err != nil {
				t.Fatal(err)
			}
			issues := ErrorsOnly(Validate(&p))
			if len(issues) != 1 || issues[0].Code != tc.code {
				t.Fatalf("issues=%+v", issues)
			}
			if tc.pointer != "" && issues[0].Source.Pointer != "/subgraphs/root/nodes/1"+tc.pointer {
				t.Fatalf("pointer=%s", issues[0].Source.Pointer)
			}
		})
	}
}

func TestBindingOutputAndForwardReferences(t *testing.T) {
	var p Plan
	raw := `{"irVersion":"1.0","name":"forward","root":"root","subgraphs":{"root":{"nodes":[{"id":"consumer","kind":"project","inputs":{"x":{"ref":{"node":"producer","port":"x"}}}},{"id":"producer","kind":"execute","executor":"generic/task"}],"output":{"result":{"expr":{"op":"ref","node":"consumer","port":"x"}}}}}}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if issues := ErrorsOnly(Validate(&p)); len(issues) != 0 {
		t.Fatalf("valid dependency graph: %+v", issues)
	}
	sg := p.Subgraphs["root"]
	sg.Output["result"] = Binding{Expr: &Expr{Op: "ref", Node: "missing", Port: "x"}}
	issues := ErrorsOnly(Validate(&p))
	if len(issues) != 1 || issues[0].Source.Pointer != "/subgraphs/root/output/result/expr/node" {
		t.Fatalf("issues=%+v", issues)
	}
}

func TestBindingDeclaredPort(t *testing.T) {
	p := &Plan{IRVersion: "1.0", Name: "ports", Root: "root", Subgraphs: map[string]Subgraph{
		"root": {Nodes: []Node{{ID: "produce", Kind: KindExecute, Executor: "generic/task", Outputs: map[string]OutputDecl{"result": {}}},
			{ID: "consume", Kind: KindExecute, Executor: "generic/task", Inputs: map[string]Binding{"x": {Expr: &Expr{Op: "ref", Node: "produce", Port: "typo"}}}}}},
	}}
	issues := ErrorsOnly(Validate(p))
	if len(issues) != 1 || issues[0].Code != "MUXLIO_BAD_PORT" || issues[0].Source.Pointer != "/subgraphs/root/nodes/1/inputs/x/expr/port" {
		t.Fatalf("issues=%+v", issues)
	}
}

func TestBindingEnclosingDeclaredPort(t *testing.T) {
	var p Plan
	raw := `{"irVersion":"1.0","name":"nested","root":"root","subgraphs":{
	  "root":{"nodes":[
	    {"id":"prepare","kind":"execute","executor":"generic/task","outputs":{"result":{"path":"result.json"}}},
	    {"id":"fanout","kind":"map","items":{"op":"ref","node":"prepare","port":"result"},"bodyRef":"body"}]},
	  "body":{"nodes":[{"id":"consume","kind":"execute","executor":"generic/task","inputs":{"value":{"ref":{"node":"prepare","port":"typo"}}}}]}}}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	issues := ErrorsOnly(Validate(&p))
	if len(issues) != 1 || issues[0].Code != "MUXLIO_BAD_PORT" || issues[0].Source.Pointer != "/subgraphs/body/nodes/0/inputs/value/ref/port" {
		t.Fatalf("issues=%+v", issues)
	}
	p.Subgraphs["body"].Nodes[0].Inputs["value"].Ref.Port = "result"
	if issues := ErrorsOnly(Validate(&p)); len(issues) != 0 {
		t.Fatalf("valid enclosing reference: %+v", issues)
	}
}

func TestBindingBranchScopeAvailability(t *testing.T) {
	for _, target := range []string{"prepare", "choose", "later"} {
		t.Run(target, func(t *testing.T) {
			p := &Plan{IRVersion: "1.0", Name: "branch-scope", Root: "root", Subgraphs: map[string]Subgraph{
				"root": {Nodes: []Node{
					{ID: "prepare", Kind: KindExecute, Executor: "generic/task"},
					{ID: "choose", Kind: KindBranch, Condition: &Expr{Op: "literal", Value: json.RawMessage(`true`)}, ThenRef: "body"},
					{ID: "later", Kind: KindExecute, Executor: "generic/task"},
				}},
				"body": {Output: map[string]Binding{"result": {Ref: &ValueRef{Node: target, Port: "result"}}}},
			}}
			issues := ErrorsOnly(Validate(p))
			if target == "prepare" {
				if len(issues) != 0 {
					t.Fatalf("completed parent output should be visible: %+v", issues)
				}
			} else if len(issues) != 1 || issues[0].Code != "MUXLIO_BAD_REF" || issues[0].Source.Pointer != "/subgraphs/body/output/result/ref/node" {
				t.Fatalf("unavailable parent output must fail preflight: %+v", issues)
			}
		})
	}
}
