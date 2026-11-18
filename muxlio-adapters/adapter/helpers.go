package adapter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/muxlio/muxlio-adapters/contract"
)

// WorkingDir returns the directory an agent should run in. If an input port
// named "workspace" carries an absolute path to an existing directory, that
// directory is used (per-step isolation, e.g. a git worktree whose path was
// produced by an earlier step). Otherwise the authorized workspace
// (req.Workspace) is used and validated via WorkspaceRoot.
func WorkingDir(req *Request) (string, error) {
	if v, ok := req.Inputs["workspace"]; ok {
		var raw string
		switch val := v.(type) {
		case string:
			raw = val
		case map[string]any:
			if s, ok := val["data"].(string); ok && s != "" {
				raw = s
			} else if s, ok := val["path"].(string); ok && s != "" {
				raw = s
			}
		}
		if filepath.IsAbs(raw) {
			if resolved, err := filepath.EvalSymlinks(raw); err == nil {
				if info, err := os.Stat(resolved); err == nil && info.IsDir() {
					return resolved, nil
				}
			}
		}
	}
	return WorkspaceRoot(req)
}

// CollectOutputs reads each declared output file from the run work directory.
//
// Scalar ports (decl.Path == "") must contain JSON. Rejected text is retained
// for inspection and explicitly flagged in outputErrors. File-artifact
// ports (decl.Path != "") are returned as {mime, path, data} maps for upload.
// Absent or unreadable JSON ports are reported in outputErrors; absent or
// unreadable artifacts are reported in missing.
func CollectOutputs(req *Request) (map[string]any, []string, map[string]string) {
	outputErrors := make(map[string]string)
	outputs := make(map[string]any)
	var missing []string
	for port, decl := range req.Outputs {
		name := decl.Path
		if name == "" {
			name = port
		}
		fullPath, err := ResolveOutput(req.WorkDir, name)
		if err != nil {
			if decl.Path == "" {
				outputErrors[port] = err.Error()
			} else {
				missing = append(missing, port)
			}
			continue
		}
		data, err := ReadOutput(fullPath)
		if err != nil {
			if decl.Path == "" {
				outputErrors[port] = err.Error()
			} else {
				missing = append(missing, port)
			}
			continue
		}
		if decl.Path == "" {
			// Scalar output: return the parsed JSON value so downstream control
			// flow (booleans, strings, numbers) works against the raw value.
			var parsed any
			if err := contract.DecodeJSON(data, &parsed); err == nil {
				outputs[port] = parsed
			} else {
				outputs[port] = string(data)
				outputErrors[port] = fmt.Sprintf("result port must contain JSON: %v", err)
			}
		} else {
			outputs[port] = map[string]any{
				"mime": decl.Mime,
				"path": fullPath,
				"data": string(data),
			}
		}
	}
	return outputs, missing, outputErrors
}

// InputContent returns inline text for an input port value. It handles raw
// strings, maps carrying a "data" string (inline artifact bytes), and maps
// carrying a "path" to a readable file (a materialized artifact downloaded by
// the coordinator into workDir). Paths outside workDir, including through
// symlinks, and files over MaxCaptureBytes are not read. It returns ("", false)
// when there is nothing to inline.
func InputContent(workDir string, value any) (string, bool) {
	switch v := value.(type) {
	case string:
		if v != "" {
			return v, true
		}
	case map[string]any:
		if data, ok := v["data"].(string); ok && data != "" {
			return data, true
		}
		if path, ok := v["path"].(string); ok && path != "" {
			if b, err := readConfined(workDir, path); err == nil {
				return string(b), true
			}
		}
	}
	return "", false
}

func readConfined(workDir, path string) ([]byte, error) {
	if workDir == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("input path must be absolute within the work directory")
	}
	root, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("input path escapes the work directory")
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	file, err := dir.Open(rel)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxCaptureBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxCaptureBytes {
		return nil, fmt.Errorf("input exceeds %d byte limit", MaxCaptureBytes)
	}
	return data, nil
}

// ComposePrompt builds the common agent prompt from a request: the task
// description, a workspace rule, an expanded "feature" input (object or string),
// bounded inlined inputs (or file references for oversized values), and the
// declared output contract. Oversized inputs are materialized under
// WorkDir/handoff before formatting. Callers must treat a non-nil error as a
// failed invocation (do not start the agent).
func ComposePrompt(req *Request) (string, error) {
	return composePrompt(req, true)
}

// ComposeRemotePrompt is the fileless counterpart for A2A and similar agents.
// It never fabricates local paths; oversized inputs fail closed.
func ComposeRemotePrompt(req *Request) (string, error) {
	return composePrompt(req, false)
}

func composePrompt(req *Request, localFiles bool) (string, error) {
	if req == nil {
		return "", fmt.Errorf("request is required")
	}
	if err := materializeInputHandoff(req, localFiles); err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString(req.Task)
	if localFiles {
		sb.WriteString("\n\nWorkspace rule: keep all generated notes, plans, and report files inside the run scratch directory at ")
		sb.WriteString(req.WorkDir)
		sb.WriteString(" using the exact absolute paths listed below; make intended source changes in the current repository working directory. Do not create incidental files there.")
	}

	if fv, ok := req.Inputs["feature"]; ok {
		featureText, err := formatFeature(req.WorkDir, fv, localFiles)
		if err != nil {
			return "", err
		}
		sb.WriteString(featureText)
	}

	sb.WriteString(FormatInputSections(req.WorkDir, req.Inputs))
	sb.WriteString(DescribeOutputs(req, localFiles))
	if req.NodeID != "" {
		sb.WriteString(fmt.Sprintf("\n\nNode ID: %s", req.NodeID))
	}
	return sb.String(), nil
}

// DescribeOutputs formats declared output ports for agent prompts. When
// localPaths is true, absolute workDir paths are included; otherwise only port
// names and MIME/schema contracts are described (for remote/fileless agents).
// Port names are sorted for stable prompts. An empty or absent schema means the
// author intentionally permits arbitrary data for that port.
func DescribeOutputs(req *Request, localPaths bool) string {
	if req == nil || len(req.Outputs) == 0 {
		return ""
	}
	var scalarPorts, artifactPorts []string
	for port, decl := range req.Outputs {
		if decl.Path == "" {
			scalarPorts = append(scalarPorts, port)
		} else {
			artifactPorts = append(artifactPorts, port)
		}
	}
	sort.Strings(scalarPorts)
	sort.Strings(artifactPorts)
	var sb strings.Builder
	sb.WriteString("\n\nOutput contract: each output port name identifies a destination, not a field inside the JSON value. Write the value matching the authored schema (when present) directly; do not wrap it in a new object unless that wrapper is part of the schema. An empty or absent schema permits arbitrary JSON for that port.")
	if len(scalarPorts) > 0 {
		if localPaths {
			sb.WriteString("\n\nScalar outputs — write each as a file containing ONLY the raw JSON literal (no prose, no markdown, no surrounding text):")
		} else {
			sb.WriteString("\n\nScalar outputs — return each as a named artifact whose content is ONLY the raw JSON literal:")
		}
		for _, port := range scalarPorts {
			decl := req.Outputs[port]
			if localPaths {
				sb.WriteString(fmt.Sprintf("\n  port %q → %s/%s", port, req.WorkDir, port))
			} else {
				sb.WriteString(fmt.Sprintf("\n  port %q", port))
			}
			switch decl.Mime {
			case "", "application/json":
				sb.WriteString("  — JSON value")
			default:
				sb.WriteString(fmt.Sprintf("  (mime: %s)", decl.Mime))
			}
			appendOutputSchema(&sb, decl.Schema)
		}
	}
	if len(artifactPorts) > 0 {
		if localPaths {
			sb.WriteString("\n\nFile artifact outputs — write each file to its exact absolute path:")
		} else {
			sb.WriteString("\n\nFile artifact outputs — return each as a named artifact with the declared MIME type:")
		}
		for _, port := range artifactPorts {
			decl := req.Outputs[port]
			if localPaths {
				sb.WriteString(fmt.Sprintf("\n  port %q → %s/%s  (mime: %s)", port, req.WorkDir, decl.Path, decl.Mime))
			} else {
				sb.WriteString(fmt.Sprintf("\n  port %q  (mime: %s)", port, decl.Mime))
			}
			appendOutputSchema(&sb, decl.Schema)
		}
	}
	return sb.String()
}

func appendOutputSchema(sb *strings.Builder, schema json.RawMessage) {
	trimmed := bytes.TrimSpace(schema)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		sb.WriteString("\n    schema: ")
		sb.Write(trimmed)
		return
	}
	sb.WriteString("\n    schema: ")
	sb.Write(compact.Bytes())
}
