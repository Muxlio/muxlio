package plan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

const CollectionItemValidationFailed = "MUXLIO_COLLECTION_ITEM_VALIDATION_FAILED"

var collectionName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

type AppendableCollection struct {
	Name               string          `json:"name"`
	ItemSchema         json.RawMessage `json:"itemSchema"`
	IdleTimeoutSeconds float64         `json:"idleTimeoutSeconds"`
}

func (p *Plan) collectionNodes() []Node {
	root := p.Root
	if root == "" {
		root = "root"
	}
	return p.Subgraphs[root].Nodes
}

// CollectionDeclarations lists appendable collections in pinned root graph order.
func (p *Plan) CollectionDeclarations() []AppendableCollection {
	collections := []AppendableCollection{}
	for _, node := range p.collectionNodes() {
		if node.Appendable != nil {
			collections = append(collections, *node.Appendable)
		}
	}
	return collections
}

// CollectionNode finds a run-owned collection in the pinned root graph.
func (p *Plan) CollectionNode(name string) (Node, bool) {
	for _, node := range p.collectionNodes() {
		if node.Appendable != nil && node.Appendable.Name == name {
			return node, true
		}
	}
	return Node{}, false
}

func (p *Plan) validateCollections() (errs []ValidationError) {
	root := p.Root
	if root == "" {
		root = "root"
	}
	seen := map[string]bool{}
	for sg, graph := range p.Subgraphs {
		for _, node := range graph.Nodes {
			a := node.Appendable
			if a == nil {
				continue
			}
			add := func(message string) {
				errs = append(errs, ValidationError{Code: "MUXLIO_BAD_COLLECTION", Severity: "error", Phase: "plan", Subgraph: sg, NodeID: node.ID, Message: message})
			}
			if sg != root || node.Kind != KindMap {
				add("appendable collections require a map in the root subgraph")
			}
			if !collectionName.MatchString(a.Name) || seen[a.Name] {
				add("appendable collections require a unique URL-safe name")
			}
			seen[a.Name] = true
			if node.MaxItems == nil || *node.MaxItems <= 0 || (node.Concurrency != nil && *node.Concurrency != 1) {
				add("appendable collections require a positive maxItems and sequential execution")
			}
			if !validDuration(&a.IdleTimeoutSeconds) || time.Duration(a.IdleTimeoutSeconds*float64(time.Second)) <= 0 {
				add("appendable collections require a positive idleTimeoutSeconds within duration range")
			}
			if !collectionPath(node.Items, "input") || !collectionPath(node.Key, "item") {
				add("appendable collections require items from run input and a string key from the item")
			}
			if len(a.ItemSchema) == 0 {
				add("appendable collections require an itemSchema")
			} else {
				var failure *ValueValidationFailure
				if err := validateJSONValue(nil, a.ItemSchema, "item", CollectionItemValidationFailed); errors.As(err, &failure) && failure.Code == InvalidSchema {
					add(failure.Message)
				}
			}
		}
	}
	return errs
}

func collectionPath(expr *Expr, root string) bool {
	for expr != nil && (expr.Op == "field" || expr.Op == "index") {
		expr = expr.Expr
	}
	return expr != nil && expr.Op == root
}

// CollectionSeed evaluates the initial items against the immutable run input.
func (node Node) CollectionSeed(input any) (state.CollectionSpec, error) {
	if node.Appendable == nil || node.MaxItems == nil {
		return state.CollectionSpec{}, fmt.Errorf("muxlio: node %q has no bounded appendable collection", node.ID)
	}
	spec := state.CollectionSpec{
		Seed:        state.RunCollection{Name: node.Appendable.Name, Items: []state.CollectionItem{}},
		MaxItems:    *node.MaxItems,
		IdleTimeout: time.Duration(node.Appendable.IdleTimeoutSeconds * float64(time.Second)),
	}
	value, err := node.Items.Eval(&EvalContext{Input: input})
	if err != nil {
		return spec, err
	}
	items, ok := value.([]any)
	if !ok {
		return spec, fmt.Errorf("muxlio: collection %q input must be an array", spec.Seed.Name)
	}
	if len(items) > spec.MaxItems {
		return spec, fmt.Errorf("muxlio: collection %q exceeds maxItems %d", spec.Seed.Name, spec.MaxItems)
	}
	seen := map[string]bool{}
	for _, value := range items {
		item, err := node.CollectionItem(value)
		if err != nil {
			return spec, err
		}
		if seen[item.ID] {
			return spec, fmt.Errorf("muxlio: collection %q has duplicate item ID %q", spec.Seed.Name, item.ID)
		}
		seen[item.ID] = true
		spec.Seed.Items = append(spec.Seed.Items, item)
	}
	return spec, nil
}

// CollectionItem derives the same stable ID for initial and appended values.
func (node Node) CollectionItem(value any) (state.CollectionItem, error) {
	key, err := node.Key.Eval(&EvalContext{Item: value})
	id, ok := key.(string)
	if err != nil || !ok || strings.TrimSpace(id) == "" {
		return state.CollectionItem{}, fmt.Errorf("muxlio: collection %q item key must be a nonempty string", node.Appendable.Name)
	}
	raw, err := json.Marshal(value)
	return state.CollectionItem{ID: id, Value: raw}, err
}

func (node Node) ValidateCollectionItem(value any) error {
	return validateJSONValue(value, node.Appendable.ItemSchema, "item", CollectionItemValidationFailed)
}

func (p *Plan) CollectionSpec(node Node, run *state.Run) (state.CollectionSpec, error) {
	spec, err := node.CollectionSeed(run.Input)
	if p.DeadlineSeconds != nil && *p.DeadlineSeconds > 0 {
		spec.RunDeadline = run.CreatedAt.Add(time.Duration(*p.DeadlineSeconds * float64(time.Second)))
	}
	return spec, err
}

func (e *Engine) executeAppendableMap(runID string, node *Node, scope []string, ctx *EvalContext) (map[string]any, error) {
	if len(scope) != 0 {
		return nil, fmt.Errorf("muxlio: collection %q must execute at run scope", node.Appendable.Name)
	}
	run, err := e.store.GetRun(runID)
	if err != nil {
		return nil, err
	}
	spec, err := e.plan.CollectionSpec(*node, run)
	if err != nil {
		return nil, err
	}
	results := make([]any, 0)
	waiting := false
	actID := activationID(runID, scope, node.ID)
	for {
		if err := e.checkpoint(); err != nil {
			return nil, err
		}
		run, err := e.store.GetRun(runID)
		if err != nil {
			return nil, err
		}
		switch run.State {
		case RunPaused:
			return nil, errRunPaused
		case RunCancelled:
			return nil, context.Canceled
		}

		drained := len(results)
		collection, _, err := e.store.UpdateRunCollection(runID, spec, state.CollectionAction{Drained: &drained})
		if err != nil {
			return nil, err
		}
		if len(collection.Items) == drained && collection.ClosedReason == "" {
			if !waiting {
				if err := e.store.SetActivationState(actID, ActWaiting); err != nil {
					return nil, err
				}
				waiting = true
			}
			// Resuming a paused run can change its state to running while
			// this activation is still waiting for an item.
			if run.State == RunRunning {
				if _, err := e.store.TransitionRunState(runID, []string{RunRunning}, RunWaiting); err != nil {
					return nil, err
				}
			}
			if err := e.wait(100 * time.Millisecond); err != nil {
				return nil, err
			}
			continue
		}

		if waiting {
			if _, err := e.store.TransitionRunState(runID, []string{RunWaiting}, RunRunning); err != nil {
				return nil, err
			}
			if err := e.store.SetActivationState(actID, ActRunning); err != nil {
				return nil, err
			}
			waiting = false
		}
		for _, item := range collection.Items[drained:] {
			var value any
			decoder := json.NewDecoder(bytes.NewReader(item.Value))
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil {
				return nil, err
			}
			result, err := e.executeMapItem(runID, node, scope, ctx, item.ID, value)
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		}
		if collection.ClosedReason != "" {
			return e.finishMap(runID, node, scope, results, 1)
		}
	}
}
