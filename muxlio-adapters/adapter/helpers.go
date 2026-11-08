package adapter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
// the coordinator). It returns ("", false) when there is nothing to inline.
func InputContent(value any) (string, bool) {
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
			if b, err := os.ReadFile(path); err == nil {
				return string(b), true
			}
		}
	}
	return "", false
}

// ComposePrompt builds the common agent prompt from a request: the task
// description, a workspace rule, an expanded "feature" input (object or string),
// the inlined content of other input ports (e.g. notes, review feedback,
// verification reports), and the list of declared output files with their exact
// absolute paths.
func ComposePrompt(req *Request) string {
	var sb strings.Builder
	sb.WriteString(req.Task)
	sb.WriteString("\n\nWorkspace rule: keep all generated notes, plans, and report files inside the run scratch directory at ")
	sb.WriteString(req.WorkDir)
	sb.WriteString(" using the exact absolute paths listed below; make intended source changes in the current repository working directory. Do not create incidental files there.")

	// Expand the "feature" input (object or string).
	if fv, ok := req.Inputs["feature"]; ok {
		switch feature := fv.(type) {
		case map[string]any:
			sb.WriteString("\n")
			if title, _ := feature["title"].(string); title != "" {
				sb.WriteString(fmt.Sprintf("\nFeature title: %s", title))
			}
			if desc, _ := feature["feature"].(string); desc != "" {
				sb.WriteString(fmt.Sprintf("\nFeature: %s", desc))
			}
			if files, ok := feature["files"].([]any); ok && len(files) > 0 {
				sb.WriteString("\nFiles to read first:")
				for _, f := range files {
					if s, ok := f.(string); ok {
						sb.WriteString(fmt.Sprintf("\n  - %s", s))
					}
				}
			}
			if spec, _ := feature["spec"].(string); spec != "" {
				sb.WriteString(fmt.Sprintf("\n\nSpec:\n%s", spec))
			}
			if ctx, _ := feature["context"].(string); ctx != "" {
				sb.WriteString(fmt.Sprintf("\n\nContext:\n%s", ctx))
			}
			if id, _ := feature["id"].(string); id != "" {
				sb.WriteString(fmt.Sprintf("\n\nFeature ID: %s", id))
			}
		case string:
			sb.WriteString(fmt.Sprintf("\n\nFeature input:\n%s", feature))
		}
	}

	// Include every input port, inlining artifacts and serializing structured
	// JSON values. "feature" is expanded above and "workspace" selects the
	// working directory, so both are excluded here.
	for name, val := range req.Inputs {
		if name == "feature" || name == "workspace" {
			continue
		}
		var content string
		var inline bool
		object, structured := val.(map[string]any)
		_, artifact := object["mime"]
		// A domain object may have a path or data field without being an
		// artifact. Keep all its fields instead of reading that path.
		if !structured || artifact {
			content, inline = InputContent(val)
		}
		if !inline {
			encoded, err := json.MarshalIndent(val, "", "  ")
			if err != nil {
				continue
			}
			content = string(encoded)
		}
		sb.WriteString(fmt.Sprintf("\n\n--- %s ---\n%s", name, content))
	}

	// Declared outputs — split into scalars and file artifacts so the agent
	// knows exactly what to write in each case.
	var scalarPorts, artifactPorts []string
	for port, decl := range req.Outputs {
		if decl.Path == "" {
			scalarPorts = append(scalarPorts, port)
		} else {
			artifactPorts = append(artifactPorts, port)
		}
	}
	if len(scalarPorts) > 0 {
		sb.WriteString("\n\nScalar outputs — write each as a file containing ONLY the raw JSON literal (no prose, no markdown, no surrounding text):")
		for _, port := range scalarPorts {
			decl := req.Outputs[port]
			sb.WriteString(fmt.Sprintf("\n  %s/%s", req.WorkDir, port))
			switch decl.Mime {
			case "application/json":
				sb.WriteString("  — JSON value, e.g. true, false, 42, or \"some string\"")
			default:
				sb.WriteString(fmt.Sprintf("  (mime: %s)", decl.Mime))
			}
		}
	}
	if len(artifactPorts) > 0 {
		sb.WriteString("\n\nFile artifact outputs — write each file to its exact absolute path:")
		for _, port := range artifactPorts {
			decl := req.Outputs[port]
			sb.WriteString(fmt.Sprintf("\n  %s/%s  (mime: %s)", req.WorkDir, decl.Path, decl.Mime))
		}
	}

	sb.WriteString(fmt.Sprintf("\n\nNode ID: %s", req.NodeID))
	return sb.String()
}
