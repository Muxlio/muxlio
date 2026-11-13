package workers

import (
	"fmt"
)

// WorkspacePlacementLabel routes execute steps that share a prepared worktree
// to workers that advertise the same absolute workspace path in their labels.
const WorkspacePlacementLabel = "muxlio.dev/workspace"

// PrepareRemoteDispatch returns a copy of req suitable to marshal for remote
// workers. Remote assignments carry execution identity, params, artifact refs,
// and executor metadata — not the coordinator's authorized workspace path.
func PrepareRemoteDispatch(req ExecuteRequest) ExecuteRequest {
	req.Workspace = ""
	return req
}

// ApplyWorkerWorkspace binds a remote assignment to an execution workspace.
// The legacy single-workspace configuration is an exact allowlist. Inputs may
// name that directory through a symlink alias, but cannot authorize a checkout.
func ApplyWorkerWorkspace(req ExecuteRequest, workerWorkspace string) (ExecuteRequest, error) {
	if req.WorkspaceMode == "empty" {
		req.Workspace = ""
		return req, nil
	}
	// A read-only remote poll needs only its durable operation reference. Do
	// not rebind it to an input workspace or copy the worker's default checkout.
	if req.Action == "poll" || req.Action == "reconcile" {
		req.Workspace = ""
		return req, nil
	}
	var authorized string
	if workerWorkspace != "" {
		path, err := CanonicalWorkspace(workerWorkspace)
		if err != nil {
			return req, err
		}
		authorized = path
	}
	if authorized == "" && req.Workspace != "" {
		return req, fmt.Errorf("muxlio: workspace requires an operator-authorized binding")
	}
	if err := CheckWorkspaceInput(req.Inputs, authorized, true); err != nil {
		return req, err
	}
	req.Workspace = authorized
	return req, nil
}

// workspaceFromExecuteInputs reads an absolute workspace path from execute
// inputs when authors wire a workspace path into downstream steps.
func workspaceFromExecuteInputs(inputs map[string]any) string {
	v, ok := inputs["workspace"]
	if !ok {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case map[string]any:
		if d, ok := val["data"].(string); ok {
			return d
		}
	}
	return ""
}
