package workers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorkspaceBinding is operator authority, never a client-supplied path. A
// workspace has one owner; other machines must not substitute their checkout.
type WorkspaceBinding struct {
	// Digest pins prepared contents, independent of the worker-local path.
	Digest   string   `json:"digest,omitempty"`
	WorkerID string   `json:"workerId"`
	Path     string   `json:"path"`
	Aliases  []string `json:"aliases,omitempty"`
	// Keep the directory identity as well as its path. Recreating a checkout
	// at the same pathname must not retarget a running worker's authority.
	directoryInfo os.FileInfo
}

type WorkspaceRegistry map[string]WorkspaceBinding

const WorkspaceIdentityLabelPrefix = "muxlio.dev/workspace-id/"

// CheckWorkspaceInput checks the conventional workspace port against operator
// authority. Remote client paths cannot be resolved on the coordinator host.
func CheckWorkspaceInput(inputs map[string]any, authorized string, local bool) error {
	input := workspaceFromExecuteInputs(inputs)
	if input == "" {
		if value, present := inputs["workspace"]; present && value != "" {
			return fmt.Errorf("workspace input must be an authorized absolute directory path")
		}
		return nil
	}
	if local {
		resolved, err := CanonicalWorkspace(input)
		if err != nil {
			return err
		}
		input = resolved
	}
	if input != authorized {
		return fmt.Errorf("input workspace %q is not authorized; select the project by workspaceId", input)
	}
	return nil
}

func (r WorkspaceRegistry) Validate() error {
	names := map[string]bool{}
	for id, binding := range r {
		if err := ValidateWorkspaceDigest(binding.Digest); err != nil {
			return err
		}
		for _, name := range append([]string{id}, binding.Aliases...) {
			if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "/\\") || names[name] {
				return fmt.Errorf("invalid or duplicate workspace identity %q", name)
			}
			names[name] = true
		}
		if binding.WorkerID == "" || !filepath.IsAbs(binding.Path) {
			return fmt.Errorf("workspace %q requires a workerId and absolute path", id)
		}
	}
	return nil
}

func (r WorkspaceRegistry) Resolve(id string) (string, WorkspaceBinding, error) {
	if err := r.Validate(); err != nil {
		return "", WorkspaceBinding{}, err
	}
	for canonical, binding := range r {
		if id == canonical {
			return canonical, binding, nil
		}
		for _, alias := range binding.Aliases {
			if alias == id {
				return canonical, binding, nil
			}
		}
	}
	return "", WorkspaceBinding{}, fmt.Errorf("workspace %q is not authorized; request an operator-configured workspace ID", id)
}

// CanonicalWorkspace resolves the entire symlink chain and requires a directory.
func CanonicalWorkspace(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("workspace must be an absolute path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("workspace %q is unavailable: %w", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace %q is not a directory", path)
	}
	return resolved, nil
}

// LocalBindings pins real paths at startup. Revalidation before execution
// detects a replaced symlink, removed worktree, or a changed owner.
func (r WorkspaceRegistry) LocalBindings(workerID string) (WorkspaceRegistry, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	local := WorkspaceRegistry{}
	paths := map[string]string{}
	for id, binding := range r {
		if binding.WorkerID != workerID {
			continue
		}
		path, err := CanonicalWorkspace(binding.Path)
		if err != nil {
			return nil, fmt.Errorf("workspace %q: %w", id, err)
		}
		if err := checkPreparedWorkspace(path, binding.Digest); err != nil {
			return nil, fmt.Errorf("workspace %q: %w", id, err)
		}
		binding.Path = path
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if binding.directoryInfo != nil && !os.SameFile(binding.directoryInfo, info) {
			return nil, fmt.Errorf("workspace %q binding changed", id)
		}
		binding.directoryInfo = info
		if previous, exists := paths[path]; exists {
			return nil, fmt.Errorf("workspaces %q and %q resolve to the same directory; configure aliases on one identity", previous, id)
		}
		paths[path] = id
		local[id] = binding
	}
	return local, nil
}

func ApplyAuthorizedWorkspace(req ExecuteRequest, workerID string, registry WorkspaceRegistry, fallback string) (ExecuteRequest, error) {
	if req.WorkspaceMode == "empty" || req.Action == "poll" || req.Action == "reconcile" {
		req.Workspace = ""
		return req, nil
	}
	if req.WorkspaceDigest != "" && req.WorkspaceID == "" {
		return req, fmt.Errorf("workspaceDigest requires an explicit workspaceId")
	}
	if req.WorkspaceID == "" {
		// Compatibility for the operator's single-workspace flag. This is
		// still an exact allowlist; an input cannot select another checkout.
		if binding, ok := registry["default"]; len(registry) == 1 && ok && fallback != "" && binding.WorkerID == workerID {
			req.WorkspaceID, req.WorkspaceWorkerID = "default", workerID
		} else {
			if len(registry) > 0 {
				return req, fmt.Errorf("workspaceId is required")
			}
			return ApplyWorkerWorkspace(req, fallback)
		}
	}
	id, binding, err := registry.Resolve(req.WorkspaceID)
	if err != nil {
		return req, err
	}
	if binding.WorkerID != workerID || req.WorkspaceWorkerID != workerID {
		return req, fmt.Errorf("workspace %q is not owned by worker %q", id, workerID)
	}
	if req.WorkspaceDigest != binding.Digest {
		return req, fmt.Errorf("workspace %q prepared digest does not match the execution request", id)
	}
	path, err := CanonicalWorkspace(binding.Path)
	if err != nil {
		return req, err
	}
	if path != binding.Path {
		return req, fmt.Errorf("workspace %q binding changed", id)
	}
	info, err := os.Stat(path)
	if err != nil {
		return req, err
	}
	if binding.directoryInfo != nil && !os.SameFile(binding.directoryInfo, info) {
		return req, fmt.Errorf("workspace %q binding changed", id)
	}
	if err := checkPreparedWorkspace(path, req.WorkspaceDigest); err != nil {
		return req, err
	}
	if err := CheckWorkspaceInput(req.Inputs, path, true); err != nil {
		return req, err
	}
	req.WorkspaceID, req.Workspace = id, path
	// Inputs are data, not authority. Adapters consuming this conventional port
	// receive the same worker-local binding as the execution envelope.
	inputs := make(map[string]any, len(req.Inputs)+1)
	for k, v := range req.Inputs {
		inputs[k] = v
	}
	inputs["workspace"] = path
	req.Inputs = inputs
	return req, nil
}
