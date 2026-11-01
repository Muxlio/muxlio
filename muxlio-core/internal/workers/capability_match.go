package workers

import (
	"encoding/json"

	"github.com/muxlio/muxlio-core/internal/state"
)

type dispatchRequirements struct {
	ExecutorVersion string            `json:"executorVersion"`
	PlacementLabels map[string]string `json:"placementLabels"`
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
		_ = json.Unmarshal(requestJSON, &req)
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
