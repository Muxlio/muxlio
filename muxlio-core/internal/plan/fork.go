package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/muxlio/muxlio-core/internal/state"
)

// ForkRun creates an independent history without executing any steps. The engine's
// plan selects the new version; nil input inherits the source's original inputs.
// Reuse candidates are source activation IDs, never external-effect checkpoints.
func (e *Engine) ForkRun(sourceID string, input json.RawMessage, reuse []string) (string, error) {
	source, err := e.store.GetRun(sourceID)
	if err != nil {
		return "", err
	}
	if !state.IsTerminalState(source.State) {
		return "", fmt.Errorf("muxlio: fork source must be terminal")
	}
	if e.plan == nil {
		var p Plan
		if err := json.Unmarshal(source.PlanJSON, &p); err != nil {
			return "", err
		}
		e.plan = &p
	}
	if input == nil {
		input, err = json.Marshal(source.Input)
		if err != nil {
			return "", err
		}
	}
	for _, id := range reuse {
		a, err := e.store.GetActivation(id)
		if err != nil {
			return "", err
		}
		if a.RunID != sourceID || a.State != ActSucceeded {
			return "", fmt.Errorf("muxlio: reuse candidate %q must be a successful source activation", id)
		}
	}
	return e.createRun(input, &state.ForkProvenance{SourceRunID: sourceID, SourcePlanDigest: source.PlanDigest, ReuseActivations: append([]string(nil), reuse...)})
}

func reusableNode(n *Node) bool {
	// Sessions and remote tasks mutate state even when the prompt is labelled pure.
	return n.Retry != nil && n.Retry.Effect == "pure" && n.Session == nil && n.SessionID == "" && n.RemoteTask == nil && len(n.RequiredCapabilities) == 0 && len(n.ConcurrencyKeys) == 0 && (n.WorkspaceMode == "" || n.WorkspaceMode == "empty")
}

// fingerprint includes all resolved dependencies, including ordering dependencies,
// the complete node contract, the pinned plan, and the structural scope. Including
// the whole plan deliberately invalidates reuse after even unrelated plan changes.
func (e *Engine) dependencyFingerprint(n *Node, scope []string, ctx *EvalContext, inputs map[string]any) (string, error) {
	raw, err := json.Marshal([]any{"fork-v2", e.plan.Digest, n, scope, ctx.subgraph, ctx.Input, ctx.Item, ctx.State, ctx.Outputs, inputs})
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(h[:]), nil
}

// forkOutput runs before any dispatch, reconciliation, or session operations.
// Each reuse records an explicit source and dependency fingerprint in history.
func (e *Engine) forkOutput(runID string, n *Node, scope []string, ctx *EvalContext, inputs map[string]any) (map[string]any, bool, error) {
	run, err := e.store.GetRun(runID)
	if err != nil {
		return nil, false, err
	}
	if !reusableNode(n) || workspaceFromInputs(inputs) != "" {
		if run.Fork != nil {
			return nil, false, fmt.Errorf("muxlio: fork blocked execution of %q: only explicitly pure, stateless steps may execute", n.ID)
		}
		return nil, false, nil
	}
	// The default workspace is mutable state outside the plan fingerprint.
	// Such steps may execute in a fork, but must never reuse prior outputs.
	if n.WorkspaceMode != "empty" {
		return nil, false, nil
	}
	fingerprint, err := e.dependencyFingerprint(n, scope, ctx, inputs)
	if err != nil {
		return nil, false, err
	}
	actID := activationID(runID, scope, n.ID)
	if err := e.store.AppendEvent(runID, "activation.fingerprint", actID, map[string]any{"fingerprint": fingerprint}); err != nil {
		return nil, false, err
	}
	if run.Fork == nil {
		return nil, false, nil
	}
	sourceID := activationID(run.Fork.SourceRunID, scope, n.ID)
	selected := false
	for _, id := range run.Fork.ReuseActivations {
		if id == sourceID {
			selected = true
		}
	}
	if !selected {
		return nil, false, nil
	}
	a, err := e.store.GetActivation(sourceID)
	if err != nil {
		return nil, false, err
	}
	if a.State != ActSucceeded {
		return nil, false, nil
	}
	// File ports must refer to immutable content, never a mutable filesystem path.
	for port, decl := range n.Outputs {
		if decl.Path == "" {
			continue
		}
		raw, err := json.Marshal(a.Outputs[port])
		if err != nil {
			return nil, false, err
		}
		var ref struct {
			Digest string `json:"digest"`
		}
		if json.Unmarshal(raw, &ref) != nil || len(ref.Digest) != 71 || ref.Digest[:7] != "sha256:" {
			return nil, false, nil
		}
		if _, err := hex.DecodeString(ref.Digest[7:]); err != nil {
			return nil, false, nil
		}
	}
	events, err := e.store.GetEvents(run.Fork.SourceRunID, 0, 0)
	if err != nil {
		return nil, false, err
	}
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Type != "activation.fingerprint" || event.SubjectID != sourceID {
			continue
		}
		if event.Payload["fingerprint"] == fingerprint {
			err := e.store.AppendEvent(runID, "activation.output_reused", actID, map[string]any{"sourceRunId": run.Fork.SourceRunID, "sourceActivationId": sourceID, "fingerprint": fingerprint})
			return a.Outputs, true, err
		}
		break
	}
	return nil, false, nil
}
