package workers

import (
	"fmt"
	"os"
	"path/filepath"
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
// When the request inputs carry an absolute workspace path that exists on this
// host (e.g. a prepared worktree), that path is used. When inputs require a
// workspace that is missing on this worker, execution fails instead of falling
// back to the worker default checkout.
func ApplyWorkerWorkspace(req ExecuteRequest, workerWorkspace string) (ExecuteRequest, error) {
	if ws := workspaceFromExecuteInputs(req.Inputs); ws != "" {
		if resolved := ignoreMissingWorkspace(ws); resolved != "" {
			req.Workspace = resolved
			return req, nil
		}
		return req, fmt.Errorf("muxlio: execute input workspace %q is not accessible on this worker", ws)
	}
	if workerWorkspace != "" {
		if abs, err := filepath.Abs(workerWorkspace); err == nil {
			req.Workspace = abs
		} else {
			req.Workspace = workerWorkspace
		}
		return req, nil
	}
	req.Workspace = ignoreMissingWorkspace(req.Workspace)
	return req, nil
}

func ignoreMissingWorkspace(workspace string) string {
	if workspace == "" {
		return ""
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return ""
	}
	return workspace
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
