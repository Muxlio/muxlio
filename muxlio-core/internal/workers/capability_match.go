package workers

import (
	"encoding/json"
	"fmt"

	"github.com/muxlio/muxlio-core/internal/sandbox"
	"github.com/muxlio/muxlio-core/internal/state"
)

// IsolationLabelPrefix advertises operator-enabled, enforced worker backends.
// Workers without this capability must never receive isolation-required tasks.
const IsolationLabelPrefix = "muxlio.dev/isolation/"

// MatchDispatch reports whether worker can claim an execute envelope.
func MatchDispatch(executor string, requestJSON []byte, caps []state.WorkerCapability, labels map[string]string) bool {
	var cap *state.WorkerCapability
	for i := range caps {
		if caps[i].Name == executor {
			cap = &caps[i]
			break
		}
	}
	if cap == nil || (cap.Readiness != nil && (!cap.Readiness.Ready || len(cap.Readiness.Checks) == 0 || len(cap.Readiness.Failed()) > 0)) || (cap.Contract != nil && cap.Contract.ProbeVersion != "" && cap.Readiness == nil) {
		return false
	}
	var req ExecuteRequest
	if len(requestJSON) > 0 {
		if json.Unmarshal(requestJSON, &req) != nil {
			return false
		}
	}
	// Legacy workers can satisfy the baseline only. Optional requirements need
	// an explicit descriptor, and the same check runs again at local dispatch.
	req.Executor = executor
	descriptor := cap.Contract
	if descriptor == nil {
		descriptor = &adapterManifest{ExecutorVersion: cap.Version}
	}
	if req.Environment != nil && req.Environment.CheckRunners(cap.Readiness.RunnerVersions()) != nil {
		return false
	}
	if CheckAdapterCapabilities(req, descriptor) != nil {
		return false
	}
	if ValidatePermissions(req) != nil || ValidateWorkspaceContract(req.WorkspaceMode, req.ConcurrencyKeys) != nil {
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
		if p.Mode != "required" || !sandbox.Supported(p.Runtime) || labels[IsolationLabelPrefix+p.Runtime] != "true" {
			return false
		}
		if (sandbox.NeedsImage(p.Runtime) && p.Image == "") || (sandbox.NeedsOptIn(p.Runtime) && !p.Enabled) {
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

// IsolationLabels advertises only operator-enabled backends with a local launcher.
func IsolationLabels(backends []string) (map[string]string, error) {
	labels := make(map[string]string)
	for _, backend := range backends {
		if err := sandbox.CheckAvailable(backend); err != nil {
			return nil, fmt.Errorf("isolationBackends: %s: %w", backend, err)
		}
		labels[IsolationLabelPrefix+backend] = "true"
	}
	return labels, nil
}
