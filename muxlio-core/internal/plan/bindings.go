package plan

import (
	"fmt"
	"sort"

	"github.com/muxlio/muxlio-core/internal/diagnostic"
)

// Validate bindings before any executor can run, using the same dependency
// graph as execution. Source order is not execution order: forward references
// are valid when the graph is acyclic.
func validateBindings(p *Plan, name string, sg Subgraph) []ValidationError {
	var errs []ValidationError
	visible := visibleOuterNodeDefinitions(p, name, map[string]bool{})
	for _, n := range sg.Nodes {
		visible[n.ID] = n
	}
	if _, err := topoSort(sg.Nodes); err != nil {
		errs = append(errs, ValidationError{Code: "MUXLIO_DEPENDENCY_CYCLE", Severity: "error", Phase: "plan", Subgraph: name, Message: err.Error(), Fix: "Remove circular node references."})
	}
	issue := func(node, path, code, message, fix string) {
		errs = append(errs, ValidationError{Code: code, Severity: "error", Phase: "plan", Subgraph: name, NodeID: node, Source: &diagnostic.Source{Pointer: path}, Message: message, Fix: fix})
	}
	ref := func(node, path, target, port string) {
		producer, exists := visible[target]
		if !exists {
			issue(node, path, "MUXLIO_BAD_REF", fmt.Sprintf("binding references unknown node %q", target), "Reference an existing node in this scope or an enclosing scope.")
			errs[len(errs)-1].Expected = "an existing node reference"
			errs[len(errs)-1].Actual = target
			return
		}
		// Declared execute ports are a static contract. Undeclared/dynamic
		// executor outputs and control-flow projections remain runtime values.
		if producer.Kind == KindExecute && len(producer.Outputs) > 0 {
			if _, ok := producer.Outputs[port]; !ok {
				issue(node, path[:len(path)-len("node")]+"port", "MUXLIO_BAD_PORT", fmt.Sprintf("binding references undeclared output port %q on node %q", port, target), "Use a declared output port or add it to the producing task's outputs.")
			}
		}
	}
	var expr func(string, string, *Expr)
	expr = func(node, path string, e *Expr) {
		if e == nil {
			return
		}
		if e.Op == "ref" {
			ref(node, path+"/node", e.Node, e.Port)
		}
		expr(node, path+"/expr", e.Expr)
		expr(node, path+"/left", e.Left)
		expr(node, path+"/right", e.Right)
		for _, key := range sortedKeys(e.Fields) {
			expr(node, path+"/fields/"+pointerPart(key), e.Fields[key])
		}
		for i, value := range e.Elements {
			expr(node, fmt.Sprintf("%s/elements/%d", path, i), value)
		}
	}
	bindings := func(node, path string, values map[string]Binding) {
		for _, key := range sortedKeys(values) {
			b := values[key]
			pointer := path + "/" + pointerPart(key)
			if (b.Ref == nil) == (b.Expr == nil) {
				issue(node, pointer, "MUXLIO_BAD_BINDING", "binding must contain exactly one of ref or expr", "Use ref for a node output or expr for a computed or literal value.")
				continue
			}
			if b.Ref != nil {
				ref(node, pointer+"/ref/node", b.Ref.Node, b.Ref.Port)
			}
			expr(node, pointer+"/expr", b.Expr)
		}
	}
	base := "/subgraphs/" + pointerPart(name)
	for i, n := range sg.Nodes {
		path := fmt.Sprintf("%s/nodes/%d", base, i)
		bindings(n.ID, path+"/inputs", n.Inputs)
		for _, entry := range []struct {
			name  string
			value *Expr
		}{
			{"items", n.Items}, {"key", n.Key}, {"condition", n.Condition}, {"while", n.While}, {"initState", n.InitState},
		} {
			expr(n.ID, path+"/"+entry.name, entry.value)
		}
	}
	bindings("", base+"/output", sg.Output)
	return errs
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
