package workers

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// newAttemptScratch allocates once per invocation, including repeated execution
// of the same durable attempt. Transfer and script layers must use the same path.
func newAttemptScratch(req ExecuteRequest, base string) (string, error) {
	parent := base
	if base == "" {
		parent = os.TempDir()
	} else if req.RunID != "" && req.ActivationID != "" && req.AttemptID != "" {
		parent = filepath.Join(base, "..", "runs", safePathPart(req.RunID), safePathPart(req.ActivationID), safePathPart(req.AttemptID))
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(parent, "execution-")
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(dir)
}

// ValidateWorkspaceContract validates resource declarations before admission.
func ValidateWorkspaceContract(mode string, keys []string) error {
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("exclusive concurrency key must not be empty")
		}
	}
	switch mode {
	case "", "snapshot", "empty", "read-only", "shared-write":
	default:
		return fmt.Errorf("unknown workspace mode %q", mode)
	}
	return nil
}

// prepareAttemptWorkspace never exposes the source as writable by default.
// Snapshots contain independent files, require no Git repository, and preserve
// relative symlinks only when their targets stay within the source tree.
func prepareAttemptWorkspace(req ExecuteRequest, scratch string) (ExecuteRequest, error) {
	if err := ValidateWorkspaceContract(req.WorkspaceMode, req.ConcurrencyKeys); err != nil {
		return req, err
	}
	switch req.WorkspaceMode {
	case "", "snapshot", "empty":
		dst := filepath.Join(scratch, "workspace")
		if err := os.Mkdir(dst, 0o700); err != nil {
			return req, err
		}
		if req.WorkspaceMode != "empty" && req.Workspace != "" {
			source, err := filepath.EvalSymlinks(req.Workspace)
			if err != nil {
				return req, err
			}
			resolvedScratch, err := filepath.EvalSymlinks(scratch)
			if err != nil {
				return req, err
			}
			rel, err := filepath.Rel(source, resolvedScratch)
			if err != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
				return req, fmt.Errorf("attempt scratch must be outside workspace source")
			}
			root, err := os.OpenRoot(source)
			if err != nil {
				return req, err
			}
			defer root.Close()
			err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				rel, err := filepath.Rel(source, path)
				if err != nil {
					return err
				}
				if rel == "." {
					return nil
				}
				// Git metadata can refer to a mutable repository outside a worktree.
				if entry.Name() == ".git" {
					if entry.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if entry.Type()&os.ModeSymlink != 0 {
					link, err := root.Readlink(rel)
					if err != nil {
						return err
					}
					resolved := filepath.Clean(filepath.Join(filepath.Dir(rel), link))
					if filepath.IsAbs(link) || resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) {
						return fmt.Errorf("workspace snapshot rejects symlink outside workspace %q", rel)
					}
					// Root.Stat follows the complete chain while enforcing source
					// containment, including links in intermediate directories.
					if _, err := root.Stat(rel); err != nil {
						return fmt.Errorf("workspace snapshot rejects symlink %q: %w", rel, err)
					}
					return os.Symlink(link, filepath.Join(dst, rel))
				}
				info, err := entry.Info()
				if err != nil {
					return err
				}
				target := filepath.Join(dst, rel)
				if entry.IsDir() {
					return os.Mkdir(target, 0o700)
				}
				if !info.Mode().IsRegular() {
					return fmt.Errorf("workspace snapshot rejects non-regular file %q", rel)
				}
				input, err := root.Open(rel)
				if err != nil {
					return err
				}
				output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600|info.Mode().Perm()&0o100)
				if err != nil {
					input.Close()
					return err
				}
				_, copyErr := io.Copy(output, input)
				input.Close()
				closeErr := output.Close()
				if copyErr != nil {
					return copyErr
				}
				return closeErr
			})
			if err != nil {
				return req, err
			}
		}
		req.Workspace, req.WorkspaceMode = dst, "snapshot"
		// Adapters may consume the workspace input directly. Rebind a copy so
		// no input can redirect them back into the shared source tree.
		inputs := make(map[string]any, len(req.Inputs)+1)
		for key, value := range req.Inputs {
			inputs[key] = value
		}
		inputs["workspace"] = dst
		req.Inputs = inputs
	case "read-only":
		var params struct {
			Isolation *isolationPolicy `json:"isolation"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return req, err
		}
		if params.Isolation == nil || params.Isolation.Mode != "required" {
			return req, fmt.Errorf("read-only workspace requires enforced isolation")
		}
	case "shared-write":
		if req.Workspace == "" {
			return req, fmt.Errorf("shared-write workspace requires an authorized workspace")
		}
	default:
		return req, fmt.Errorf("unknown workspace mode %q", req.WorkspaceMode)
	}
	return req, nil
}

// ValidateDispatchWorkspace requires resolved operator authority before durable
// admission. Explicit keys cannot substitute for the implicit workspace lock.
func ValidateDispatchWorkspace(req ExecuteRequest) error {
	if err := ValidateWorkspaceContract(req.WorkspaceMode, req.ConcurrencyKeys); err != nil {
		return err
	}
	if req.WorkspaceMode == "shared-write" && req.Action != "poll" && req.Action != "reconcile" && (req.WorkspaceID == "" || req.WorkspaceWorkerID == "") {
		return fmt.Errorf("shared-write workspace requires a resolved workspace identity and owner")
	}
	return nil
}
