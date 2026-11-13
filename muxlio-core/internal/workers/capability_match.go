package workers

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/sandbox"
	"github.com/muxlio/muxlio-core/internal/state"
)

// IsolationLabelPrefix advertises operator-enabled, enforced worker backends.
// Workers without this capability must never receive isolation-required tasks.
const IsolationLabelPrefix = "muxlio.dev/isolation/"

// DispatchMismatch is a stable reason a worker cannot claim a request.
type DispatchMismatch struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Capability string `json:"capability,omitempty"`
}

func (e *DispatchMismatch) Error() string { return e.Message }

func mismatch(code, message string) *DispatchMismatch {
	return &DispatchMismatch{Code: code, Message: message}
}

// MatchDispatch reports whether worker can claim an execute envelope.
func MatchDispatch(executor string, requestJSON []byte, caps []state.WorkerCapability, labels map[string]string) bool {
	return ExplainDispatch(executor, requestJSON, caps, labels) == nil
}

// ExplainDispatch uses the dispatch contract, without executing or probing an adapter.
// It returns the first blocking constraint; correcting it may expose another.
func ExplainDispatch(executor string, requestJSON []byte, caps []state.WorkerCapability, labels map[string]string) *DispatchMismatch {
	var cap *state.WorkerCapability
	for i := range caps {
		if caps[i].Name == executor {
			cap = &caps[i]
			break
		}
	}
	if cap == nil {
		return mismatch("executor_not_registered", "Register the executor on this worker.")
	}
	if (cap.Readiness != nil && (!cap.Readiness.Ready || len(cap.Readiness.Checks) == 0 || len(cap.Readiness.Failed()) > 0)) || (cap.Contract != nil && cap.Contract.ProbeVersion != "" && cap.Readiness == nil) {
		return mismatch("executor_not_ready", "Resolve failed or missing worker readiness checks.")
	}
	var req ExecuteRequest
	if len(requestJSON) > 0 {
		if err := contract.DecodeJSON(requestJSON, &req); err != nil {
			return mismatch("invalid_execution_request", err.Error())
		}
	}
	req.Executor = executor
	descriptor := cap.Contract
	if descriptor == nil {
		descriptor = &adapterManifest{ExecutorVersion: cap.Version}
	}
	if req.Environment != nil {
		if err := req.Environment.CheckRunners(cap.Readiness.RunnerVersions()); err != nil {
			return mismatch("environment_runner_mismatch", err.Error())
		}
	}
	if req.ExecutorVersion != "" && cap.Version != req.ExecutorVersion {
		return mismatch("executor_version_mismatch", "Select a worker with the pinned executor version.")
	}
	if err := CheckAdapterCapabilities(req, descriptor); err != nil {
		var unsupported *contract.UnsupportedCapabilityError
		if errors.As(err, &unsupported) {
			return &DispatchMismatch{Code: "missing_required_capability", Message: err.Error(), Capability: unsupported.Capability}
		}
		return mismatch("adapter_contract_mismatch", err.Error())
	}
	if err := ValidatePermissions(req); err != nil {
		return mismatch("incompatible_execution_policy", err.Error())
	}
	if err := ValidateDispatchWorkspace(req); err != nil {
		return mismatch("invalid_workspace_constraints", err.Error())
	}
	var params struct {
		Isolation *isolationPolicy `json:"isolation"`
	}
	if len(req.Params) > 0 && json.Unmarshal(req.Params, &params) != nil {
		return mismatch("invalid_execution_request", "Invalid execution params.")
	}
	if req.WorkspaceMode == "read-only" && (params.Isolation == nil || params.Isolation.Mode != "required") {
		return mismatch("isolation_required", "Read-only workspaces require enforced isolation.")
	}
	if p := params.Isolation; p != nil && p.Mode != "trusted-native" {
		if p.Mode != "required" || !sandbox.Supported(p.Runtime) || labels[IsolationLabelPrefix+p.Runtime] != "true" {
			return mismatch("isolation_runtime_unavailable", "Enable the requested isolation runtime on this worker.")
		}
		if (sandbox.NeedsImage(p.Runtime) && p.Image == "") || (sandbox.NeedsOptIn(p.Runtime) && !p.Enabled) {
			return mismatch("invalid_isolation_configuration", "Supply the isolation runtime's required image and opt-in.")
		}
	}
	for k, v := range req.PlacementLabels {
		if labels == nil || labels[k] != v {
			return mismatch("placement_label_mismatch", "Select a worker with the required placement labels.")
		}
	}
	return nil
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
