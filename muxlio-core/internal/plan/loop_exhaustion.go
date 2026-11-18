package plan

import (
	"encoding/json"
	"fmt"

	"github.com/muxlio/muxlio-core/internal/state"
)

// RejectExhaustedLoopResume fails closed when a paused run's waiting loop is
// exhausted under unchanged bounds. Ordinary mid-loop pause/resume is unaffected.
func RejectExhaustedLoopResume(store state.Store, runID string) error {
	run, err := store.GetRun(runID)
	if err != nil {
		return err
	}
	if len(run.PlanJSON) == 0 {
		return nil
	}
	var pinned Plan
	if err := json.Unmarshal(run.PlanJSON, &pinned); err != nil {
		return fmt.Errorf("muxlio: resume: pinned plan is corrupt: %w", err)
	}
	acts, err := store.ListActivations(runID)
	if err != nil {
		return err
	}
	return rejectExhaustedLoopContinuation(&pinned, acts, run.Input)
}

func rejectExhaustedLoopContinuation(p *Plan, acts []state.Activation, runInput any) error {
	if p == nil {
		return nil
	}
	outputs := activationEvalOutputs(acts)
	for _, act := range acts {
		if act.State == "succeeded" || act.State == "cancelled" {
			continue
		}
		prog, saved, ok := loopProgressFromOutputs(act.Outputs)
		if !ok {
			continue
		}
		exhausted, _ := prog["exhausted"].(bool)
		if !exhausted {
			continue
		}
		iteration, err := loopCheckpointIteration(prog["iteration"])
		if err != nil {
			return err
		}
		nodeID := act.NodeID
		if nodeID == "" {
			nodeID = act.ID
		}
		node := findPlanNode(p, nodeID)
		if node == nil || node.Kind != KindLoop {
			return fmt.Errorf("%w: activation %q", ErrLoopExhaustedUnchanged, act.ID)
		}
		maxIter := 0
		if node.MaxIterations != nil {
			maxIter = *node.MaxIterations
		}
		if maxIter != 0 && iteration < maxIter {
			continue
		}
		// Match executeLoop: an exhausted checkpoint whose while is now false
		// can complete without new work under unchanged bounds.
		if node.While != nil {
			val, evalErr := node.While.Eval(&EvalContext{Input: runInput, State: saved, Outputs: outputs})
			if evalErr != nil {
				return fmt.Errorf("%w: loop %q while evaluation failed: %v", ErrLoopExhaustedUnchanged, node.ID, evalErr)
			}
			still, ok := val.(bool)
			if !ok {
				return fmt.Errorf("%w: loop %q while must be boolean", ErrLoopExhaustedUnchanged, node.ID)
			}
			if !still {
				continue
			}
		}
		return fmt.Errorf("%w: loop %q at iteration %d", ErrLoopExhaustedUnchanged, node.ID, iteration)
	}
	return nil
}

func activationEvalOutputs(acts []state.Activation) map[string]map[string]any {
	out := make(map[string]map[string]any)
	for _, a := range acts {
		if a.State != ActSucceeded || a.Outputs == nil || a.NodeID == "" {
			continue
		}
		out[a.NodeID] = a.Outputs
	}
	return out
}

func findPlanNode(p *Plan, nodeID string) *Node {
	if p == nil {
		return nil
	}
	for _, sg := range p.Subgraphs {
		for i := range sg.Nodes {
			if sg.Nodes[i].ID == nodeID {
				return &sg.Nodes[i]
			}
		}
	}
	// Activation IDs are run-scoped; match the trailing node id segment.
	for _, sg := range p.Subgraphs {
		for i := range sg.Nodes {
			if sg.Nodes[i].ID != "" && (nodeID == sg.Nodes[i].ID || hasSuffixNodeID(nodeID, sg.Nodes[i].ID)) {
				return &sg.Nodes[i]
			}
		}
	}
	return nil
}

func hasSuffixNodeID(activationID, nodeID string) bool {
	if len(activationID) <= len(nodeID) {
		return activationID == nodeID
	}
	return activationID[len(activationID)-len(nodeID)-1] == ':' && activationID[len(activationID)-len(nodeID):] == nodeID
}
