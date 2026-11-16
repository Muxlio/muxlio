package plan

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/muxlio/muxlio-core/internal/state"
)

// Selection follows output bindings through completed structural activations.
// Only successful attempts that actually produced the dependency are included;
// unrelated work inside the same child or untaken branches do not count.
func (e *Engine) selectionDependencies(runID string, node *Node, scope []string, history []state.ExecutionAttempt) (map[string]bool, error) {
	if len(node.Inputs) == 0 && node.Items == nil && node.Key == nil && node.Condition == nil && node.While == nil && node.InitState == nil {
		return map[string]bool{}, nil
	}
	activations, err := e.store.ListActivations(runID)
	if err != nil {
		return nil, err
	}
	r := selectionDependencyResolver{plan: e.plan, runID: runID, activations: map[string]state.Activation{}, successes: map[string]bool{}, scopes: map[string][]string{}, producers: map[string]bool{}, visited: map[selectionDependencyKey]bool{}}
	r.loadEvents = func() ([]state.Event, error) { return e.store.GetEvents(runID, 0, 0) }

	for _, a := range activations {
		if a.State != ActSucceeded {
			continue
		}
		r.activations[a.ID] = a
		r.scopes[activationID(runID, a.ScopePath, "")] = a.ScopePath
	}
	for _, a := range history {
		if a.State == "succeeded" {
			r.successes[a.ActivationID] = true
		}
	}
	for _, binding := range node.Inputs {
		r.binding(binding, scope, nil)
	}
	for _, expr := range []*Expr{node.Items, node.Key, node.Condition, node.While, node.InitState} {
		r.expr(expr, scope, nil)
	}
	return r.producers, r.err
}

type selectionDependencyResolver struct {
	plan        *Plan
	runID       string
	activations map[string]state.Activation
	successes   map[string]bool
	scopes      map[string][]string
	producers   map[string]bool
	visited     map[selectionDependencyKey]bool
	loadEvents  func() ([]state.Event, error)
	events      map[selectionEventKey]state.Event
	err         error
}

type selectionEventKey struct{ subject, kind string }

// Expression-only bodies create no nested activations; their existing control
// events preserve which branch or how many loop iterations actually ran.
func (r *selectionDependencyResolver) controlEvent(subject, kind string) (state.Event, bool) {
	if r.events == nil {
		r.events = map[selectionEventKey]state.Event{}
		if r.loadEvents == nil {
			return state.Event{}, false
		}
		events, err := r.loadEvents()
		if err != nil {
			r.err = err
			return state.Event{}, false
		}
		for _, event := range events {
			if event.Type == state.EventLoopIterated || event.Type == state.EventBranchSelected {
				r.events[selectionEventKey{event.SubjectID, event.Type}] = event
			}
		}
	}
	event, ok := r.events[selectionEventKey{subject, kind}]
	return event, ok
}

// Path segments distinguish object fields (f:) from array indices (i:).
type selectionValuePath []string

type selectionDependencyKey struct{ activation, port, path string }

func selectionPathKey(path selectionValuePath) string {
	if len(path) == 0 {
		return ""
	}
	raw, _ := json.Marshal(path)
	return string(raw)
}

func (r *selectionDependencyResolver) resolve(nodeID, port string, scope []string, path selectionValuePath) {
	for depth := len(scope); depth >= 0; depth-- {
		id := activationID(r.runID, scope[:depth], nodeID)
		if r.successes[id] {
			r.producers[id] = true
			return
		}
		activation, ok := r.activations[id]
		if !ok {
			continue
		}
		key := selectionDependencyKey{activation: id, port: port, path: selectionPathKey(path)}
		if r.visited[key] {
			return
		}
		r.visited[key] = true
		graph, ok := r.graphAtScope(activation.ScopePath)
		if !ok {
			return
		}
		for _, node := range graph.Nodes {
			if node.ID == nodeID {
				r.structural(node, activation.ScopePath, port, path)
				return
			}
		}
		return
	}
}

func (r *selectionDependencyResolver) structural(node Node, scope []string, port string, path selectionValuePath) {
	switch node.Kind {
	case KindChild:
		childScope := append(append([]string{}, scope...), "child:"+node.ChildRef, "invoke:"+node.ID)
		r.output(node.ChildRef, childScope, "", path)
	case KindProject, KindJoin:
		if port != "" {
			r.binding(node.Inputs[port], scope, path)
			return
		}
		if len(path) > 0 {
			if strings.HasPrefix(path[0], "f:") {
				r.binding(node.Inputs[strings.TrimPrefix(path[0], "f:")], scope, path[1:])
			}
			return
		}
		for _, binding := range node.Inputs {
			r.binding(binding, scope, nil)
		}
	case KindMap:
		prefix := "map:" + identityComponent(node.ID) + ":"
		nested := r.nestedScopes(scope, prefix)
		if len(path) > 0 {
			if !strings.HasPrefix(path[0], "i:") {
				return
			}
			// Default keys are the original item indices. Custom keys do not
			// encode their position, so keep their possible producers together.
			for _, itemScope := range nested {
				if node.Key == nil && itemScope[len(scope)] != prefix+strings.TrimPrefix(path[0], "i:") {
					continue
				}
				r.output(node.BodyRef, itemScope, "", path[1:])
			}
			return
		}
		for _, itemScope := range nested {
			r.output(node.BodyRef, itemScope, "", nil)
		}
	case KindBranch:
		prefix := "branch:" + node.ID + ":"
		scopes := r.nestedScopes(scope, prefix)
		if len(scopes) == 0 {
			if event, known := r.controlEvent(activationID(r.runID, scope, node.ID), state.EventBranchSelected); known {
				if branch, ok := event.Payload["branch"].(bool); ok {
					scopes = append(scopes, append(append([]string{}, scope...), prefix+strconv.FormatBool(branch)))
				}
			}
		}
		for _, nested := range scopes {
			switch nested[len(scope)] {
			case prefix + "true":
				r.output(node.ThenRef, nested, "", path)
			case prefix + "false":
				r.output(node.ElseRef, nested, "", path)
			}
		}
	case KindLoop:
		prefix := "loop:" + node.ID + ":"
		last := -1
		var finalScope []string
		for _, nested := range r.nestedScopes(scope, prefix) {
			iteration, err := strconv.Atoi(strings.TrimPrefix(nested[len(scope)], prefix))
			if err == nil && iteration > last {
				last = iteration
				finalScope = nested
			}
		}
		// A body containing only output expressions creates no activations.
		// Its durable iteration event still distinguishes a completed iteration
		// from a loop that returned its initial value without running the body.
		if finalScope == nil {
			if event, known := r.controlEvent(activationID(r.runID, scope, node.ID), state.EventLoopIterated); known {
				iterations, err := loopCheckpointIteration(event.Payload["iteration"])
				if err != nil {
					r.err = err
					return
				}
				if iterations > 0 {
					finalScope = append(append([]string{}, scope...), prefix+strconv.Itoa(iterations-1))
				}
			}
		}

		if finalScope != nil {
			r.output(node.BodyRef, finalScope, node.StateRef, path)
		} else {
			r.expr(node.InitState, scope, path)
		}
	}
}

// output applies the interpreter's single-port unwrapping rules. The selected
// value path is retained across child results, projected ports and loop state.
func (r *selectionDependencyResolver) output(graphName string, scope []string, port string, path selectionValuePath) {
	graph, ok := r.plan.Subgraphs[graphName]
	if !ok {
		return
	}
	if len(graph.Output) > 0 {
		if port != "" {
			r.binding(graph.Output[port], scope, path)
			return
		}
		if len(graph.Output) == 1 {
			for _, binding := range graph.Output {
				r.binding(binding, scope, path)
			}
		} else if len(path) > 0 {
			if strings.HasPrefix(path[0], "f:") {
				r.binding(graph.Output[strings.TrimPrefix(path[0], "f:")], scope, path[1:])
			}
		} else {
			for _, binding := range graph.Output {
				r.binding(binding, scope, nil)
			}
		}
		return
	}
	order, err := topoSort(graph.Nodes)
	if err != nil || len(order) == 0 {
		return
	}
	last := order[len(order)-1]
	if port != "" {
		r.resolve(last.ID, port, scope, path)
		return
	}
	// Persisted output ports are authoritative, including dynamic executors.
	ports := map[string]bool{}
	if a, ok := r.activations[activationID(r.runID, scope, last.ID)]; ok {
		for name := range a.Outputs {
			ports[name] = true
		}
	}
	// Older history and focused contract fixtures may omit saved outputs.
	if len(ports) == 0 {
		for name := range last.Outputs {
			ports[name] = true
		}
		switch last.Kind {
		case KindProject, KindJoin:
			for name := range last.Inputs {
				ports[name] = true
			}
		case KindChild, KindBranch, KindLoop:
			ports["result"] = true
		case KindMap:
			ports["items"] = true
		}
	}
	if len(ports) == 1 {
		for name := range ports {
			r.resolve(last.ID, name, scope, path)
		}
	} else if len(path) > 0 {
		if strings.HasPrefix(path[0], "f:") {
			r.resolve(last.ID, strings.TrimPrefix(path[0], "f:"), scope, path[1:])
		}
	} else {
		r.resolve(last.ID, "", scope, nil)
	}
}

func (r *selectionDependencyResolver) binding(binding Binding, scope []string, path selectionValuePath) {
	if binding.Ref != nil {
		r.resolve(binding.Ref.Node, binding.Ref.Port, scope, path)
	}
	r.expr(binding.Expr, scope, path)
}

func (r *selectionDependencyResolver) expr(expr *Expr, scope []string, path selectionValuePath) {
	if expr == nil {
		return
	}
	switch expr.Op {
	case "ref":
		r.resolve(expr.Node, expr.Port, scope, path)
	case "field":
		r.expr(expr.Expr, scope, append(selectionValuePath{"f:" + expr.Name}, path...))
	case "index":
		r.expr(expr.Expr, scope, append(selectionValuePath{"i:" + strconv.Itoa(expr.Index)}, path...))
	case "object":
		if len(path) > 0 {
			if strings.HasPrefix(path[0], "f:") {
				r.expr(expr.Fields[strings.TrimPrefix(path[0], "f:")], scope, path[1:])
			}
		} else {
			for _, field := range expr.Fields {
				r.expr(field, scope, nil)
			}
		}
	case "array":
		if len(path) > 0 {
			if strings.HasPrefix(path[0], "i:") {
				index, err := strconv.Atoi(strings.TrimPrefix(path[0], "i:"))
				if err == nil && index >= 0 && index < len(expr.Elements) {
					r.expr(expr.Elements[index], scope, path[1:])
				}
			}
		} else {
			for _, item := range expr.Elements {
				r.expr(item, scope, nil)
			}
		}
	case "state":
		r.loopState(scope, path)
	case "item":
		r.mapItem(scope, path)
	default:
		// Scalar computations depend on each operand, regardless of any later
		// selection. Literals and root inputs have no executor producer.
		r.expr(expr.Expr, scope, nil)
		r.expr(expr.Left, scope, nil)
		r.expr(expr.Right, scope, nil)
		for _, field := range expr.Fields {
			r.expr(field, scope, nil)
		}
		for _, item := range expr.Elements {
			r.expr(item, scope, nil)
		}
	}
}

// A body's state is the previous iteration's output, or the loop's initial
// value in iteration zero. Branches and children inherit it; maps clear it.
func (r *selectionDependencyResolver) loopState(scope []string, path selectionValuePath) {
	for depth := len(scope) - 1; depth >= 0; depth-- {
		graph, ok := r.graphAtScope(scope[:depth])
		if !ok {
			continue
		}
		frame := scope[depth]
		for _, node := range graph.Nodes {
			if node.Kind == KindMap && strings.HasPrefix(frame, "map:"+identityComponent(node.ID)+":") {
				return
			}
			if node.Kind != KindLoop {
				continue
			}
			prefix := "loop:" + node.ID + ":"
			if !strings.HasPrefix(frame, prefix) {
				continue
			}
			iteration, err := strconv.Atoi(strings.TrimPrefix(frame, prefix))
			if err != nil || iteration < 0 {
				continue
			}
			if iteration == 0 {
				r.expr(node.InitState, scope[:depth], path)
			} else {
				previous := append(append([]string{}, scope[:depth]...), prefix+strconv.Itoa(iteration-1))
				r.output(node.BodyRef, previous, node.StateRef, path)
			}
			return
		}
	}
}

// The current item is an element of the nearest enclosing map's collection.
// Branches, children and loops inherit it. Default keys are item indices;
// custom keys do not encode a position, so they keep every element together.
func (r *selectionDependencyResolver) mapItem(scope []string, path selectionValuePath) {
	for depth := len(scope) - 1; depth >= 0; depth-- {
		graph, ok := r.graphAtScope(scope[:depth])
		if !ok {
			continue
		}
		frame := scope[depth]
		for _, node := range graph.Nodes {
			prefix := "map:" + identityComponent(node.ID) + ":"
			if node.Kind != KindMap || !strings.HasPrefix(frame, prefix) {
				continue
			}
			if node.Key == nil {
				if index, err := strconv.Atoi(strings.TrimPrefix(frame, prefix)); err == nil && index >= 0 {
					r.expr(node.Items, scope[:depth], append(selectionValuePath{"i:" + strconv.Itoa(index)}, path...))
					return
				}
			}
			r.expr(node.Items, scope[:depth], nil)
			return
		}
	}
}

func (r *selectionDependencyResolver) nestedScopes(parent []string, prefix string) [][]string {
	seen := map[string]bool{}
	var scopes [][]string
	for _, scope := range r.scopes {
		if len(scope) <= len(parent) {
			continue
		}
		same := true
		for i := range parent {
			if parent[i] != scope[i] {
				same = false
				break
			}
		}
		if !same || !strings.HasPrefix(scope[len(parent)], prefix) {
			continue
		}
		nested := scope[:len(parent)+1]
		key := activationID(r.runID, nested, "")
		if !seen[key] {
			seen[key] = true
			scopes = append(scopes, nested)
		}
	}
	return scopes
}

func (r *selectionDependencyResolver) graphAtScope(scope []string) (Subgraph, bool) {
	if r.plan == nil {
		return Subgraph{}, false
	}
	root := r.plan.Root
	if root == "" {
		root = "root"
	}
	graph, ok := r.plan.Subgraphs[root]
	if !ok {
		return Subgraph{}, false
	}
	for i := 0; i < len(scope); i++ {
		ref := ""
		for _, node := range graph.Nodes {
			switch node.Kind {
			case KindChild:
				if i+1 < len(scope) && scope[i] == "child:"+node.ChildRef && scope[i+1] == "invoke:"+node.ID {
					ref = node.ChildRef
					i++
				}
			case KindMap:
				if strings.HasPrefix(scope[i], "map:"+identityComponent(node.ID)+":") {
					ref = node.BodyRef
				}
			case KindBranch:
				if scope[i] == "branch:"+node.ID+":true" {
					ref = node.ThenRef
				}
				if scope[i] == "branch:"+node.ID+":false" {
					ref = node.ElseRef
				}
			case KindLoop:
				if strings.HasPrefix(scope[i], "loop:"+node.ID+":") {
					if _, err := strconv.Atoi(strings.TrimPrefix(scope[i], "loop:"+node.ID+":")); err == nil {
						ref = node.BodyRef
					}
				}
			}
			if ref != "" {
				break
			}
		}
		graph, ok = r.plan.Subgraphs[ref]
		if ref == "" || !ok {
			return Subgraph{}, false
		}
	}
	return graph, true
}
