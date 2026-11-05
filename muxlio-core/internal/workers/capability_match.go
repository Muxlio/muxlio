package workers

import (
	"encoding/json"

	"github.com/muxlio/muxlio-core/internal/state"
)

// IsolationLabelPrefix advertises operator-enabled, enforced worker backends.
// Workers without this capability must never receive isolation-required tasks.
const IsolationLabelPrefix = "muxlio.dev/isolation/"

type dispatchRequirements struct {
	WorkspaceMode   string            `json:"workspaceMode"`
	ConcurrencyKeys []string          `json:"concurrencyKeys"`
	ExecutorVersion string            `json:"executorVersion"`
	PlacementLabels map[string]string `json:"placementLabels"`
	Params          json.RawMessage   `json:"params"`
}

// MatchDispatch reports whether worker can claim an execute envelope.
func MatchDispatch(executor string, requestJSON []byte, caps []state.WorkerCapability, labels map[string]string) bool {
	var cap *state.WorkerCapability
	for i := range caps {
		if caps[i].Name == executor {
			cap = &caps[i]
			break
		}
	}
	if cap == nil {
		return false
	}
	var req dispatchRequirements
	if len(requestJSON) > 0 {
		if json.Unmarshal(requestJSON, &req) != nil {
			return false
		}
	}
	if ValidateWorkspaceContract(req.WorkspaceMode, req.ConcurrencyKeys) != nil {
		return false
	}
	var params struct {
		Isolation *isolationPolicy `json:"isolation"`
	}
	if len(req.Params) > 0 && json.Unmarshal(req.Params, &params) != nil {
		return false
	}
	if req.WorkspaceMode == "read-only" && (params.Isolation == nil || params.Isolation.Mode != "required") {
		return false
	}
	if p := params.Isolation; p != nil && p.Mode != "trusted-native" {
		if p.Mode != "required" || (p.Runtime != "bwrap" && p.Runtime != "oci") || labels[IsolationLabelPrefix+p.Runtime] != "true" {
			return false
		}
		if p.Runtime == "oci" && p.Image == "" {
			return false
		}
	}
	if req.ExecutorVersion != "" && cap.Version != req.ExecutorVersion {
		return false
	}
	if len(req.PlacementLabels) > 0 {
		if labels == nil {
			return false
		}
		for k, v := range req.PlacementLabels {
			if labels[k] != v {
				return false
			}
		}
	}
	return true
}
