package adapter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Per-input and aggregate budgets for prompt inlining. Oversized values are
// materialized under workDir/handoff/ (local agents) or rejected for fileless
// remotes. These are presentation bounds, not a new accounting family.
const (
	MaxInlineInputBytes           = 32 << 10
	MaxInlineInputsAggregateBytes = 128 << 10
)

// MaterializeInputHandoff rewrites oversized or binary input values into
// confined files under workDir/handoff and leaves small text/JSON values for
// exact inline presentation. Existing confined artifact paths are verified
// readable; inaccessible paths fail closed before an agent is invoked.
func MaterializeInputHandoff(req *Request) error {
	if req == nil {
		return nil
	}
	return materializeInputHandoff(req, true)
}

func materializeInputHandoff(req *Request, localFiles bool) error {
	if req == nil || len(req.Inputs) == 0 {
		return nil
	}
	names := sortedInputNames(req.Inputs)
	var aggregate int
	for _, name := range names {
		if name == "feature" {
			// formatFeature owns feature presentation and materialization so an
			// oversized value is still referenced in the prompt.
			continue
		}
		val := req.Inputs[name]
		next, size, err := handoffValue(req.WorkDir, name, val, localFiles, aggregate)
		if err != nil {
			return err
		}
		req.Inputs[name] = next
		aggregate += size
	}
	return nil
}

func sortedInputNames(inputs map[string]any) []string {
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func handoffValue(workDir, name string, val any, localFiles bool, aggregate int) (any, int, error) {
	if name == "workspace" {
		// Workspace selects the working directory; never dump its contents.
		return val, 0, nil
	}
	if presented, size, ok, err := tryExistingArtifact(workDir, name, val, localFiles, aggregate); err != nil {
		return nil, 0, err
	} else if ok {
		return presented, size, nil
	}
	inline, size, exact := exactInline(val)
	if exact && size <= MaxInlineInputBytes && aggregate+size <= MaxInlineInputsAggregateBytes {
		return val, size, nil
	}
	if !localFiles || workDir == "" {
		return nil, 0, fmt.Errorf("input %q exceeds inline budget (%d bytes) and no local work directory is available to materialize it", name, size)
	}
	path, mime, size, err := writeInputFile(workDir, name, val, inline, exact)
	if err != nil {
		return nil, 0, err
	}
	if _, err := confinedPath(workDir, path); err != nil {
		return nil, 0, fmt.Errorf("input %q materialized path is not accessible: %w", name, err)
	}
	if size <= MaxCaptureBytes {
		if _, err := readConfined(workDir, path); err != nil {
			return nil, 0, fmt.Errorf("input %q materialized path is not readable: %w", name, err)
		}
	}
	return map[string]any{
		"path":        path,
		"mime":        mime,
		"size":        size,
		"notInlined":  true,
		"instruction": "content was not inlined; read this file",
	}, 0, nil
}

func tryExistingArtifact(workDir, name string, val any, localFiles bool, aggregate int) (any, int, bool, error) {
	object, ok := val.(map[string]any)
	if !ok {
		return nil, 0, false, nil
	}
	if _, artifact := object["mime"]; !artifact {
		return nil, 0, false, nil
	}
	path, _ := object["path"].(string)
	if path == "" {
		// Inline artifact bytes under "data" fall through to exactInline.
		return nil, 0, false, nil
	}
	if !localFiles {
		if data, ok := object["data"].(string); ok && data != "" {
			size := len(data)
			if size <= MaxInlineInputBytes && aggregate+size <= MaxInlineInputsAggregateBytes {
				return val, size, true, nil
			}
			return nil, 0, false, fmt.Errorf("input %q exceeds inline budget for a fileless agent", name)
		}
		return nil, 0, false, fmt.Errorf("input %q references a local path that a fileless agent cannot read", name)
	}
	if workDir == "" {
		return nil, 0, false, fmt.Errorf("input %q path reference requires a work directory", name)
	}
	data, err := readConfined(workDir, path)
	if err != nil {
		// File may exceed capture limit but still be a valid handoff target.
		if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
			if _, confErr := confinedPath(workDir, path); confErr != nil {
				return nil, 0, false, fmt.Errorf("input %q path is not accessible inside the work directory: %w", name, confErr)
			}
			size := int(info.Size())
			if size <= MaxInlineInputBytes && aggregate+size <= MaxInlineInputsAggregateBytes {
				return nil, 0, false, fmt.Errorf("input %q path is not readable: %w", name, err)
			}
			mime, _ := object["mime"].(string)
			if mime == "" {
				mime = mimeForName(path)
			}
			out := map[string]any{"path": path, "mime": mime, "size": size, "notInlined": true, "instruction": "content was not inlined; read this file"}
			for _, key := range []string{"digest"} {
				if v := object[key]; v != nil {
					out[key] = v
				}
			}
			return out, 0, true, nil
		}
		return nil, 0, false, fmt.Errorf("input %q path is not accessible: %w", name, err)
	}
	size := len(data)
	if size <= MaxInlineInputBytes && aggregate+size <= MaxInlineInputsAggregateBytes {
		return val, size, true, nil
	}
	mime, _ := object["mime"].(string)
	if mime == "" {
		mime = mimeForName(path)
	}
	out := map[string]any{"path": path, "mime": mime, "size": size, "notInlined": true, "instruction": "content was not inlined; read this file"}
	for _, key := range []string{"digest"} {
		if v := object[key]; v != nil {
			out[key] = v
		}
	}
	return out, 0, true, nil
}

func exactInline(val any) (string, int, bool) {
	switch v := val.(type) {
	case string:
		if !utf8.ValidString(v) {
			return "", len(v), false
		}
		return v, len(v), true
	case nil:
		return "null", 4, true
	case bool, float64, json.Number:
		encoded, err := json.Marshal(v)
		if err != nil {
			return "", 0, false
		}
		return string(encoded), len(encoded), true
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return "", 0, false
		}
		if !utf8.Valid(encoded) {
			return "", len(encoded), false
		}
		return string(encoded), len(encoded), true
	}
}

func writeInputFile(workDir, name string, raw any, inline string, exact bool) (path, mime string, size int, err error) {
	if err := validateInputPortName(name); err != nil {
		return "", "", 0, err
	}
	// Keep handoff materialization off the artifact download tree (inputs/) so
	// an oversized port cannot replace a downloaded artifact's bytes.
	dir := filepath.Join(workDir, "handoff")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", 0, fmt.Errorf("prepare handoff directory: %w", err)
	}
	ext := ".json"
	mime = "application/json"
	var body []byte
	if exact {
		if _, isString := raw.(string); isString {
			ext = ".txt"
			mime = "text/plain; charset=utf-8"
			body = []byte(inline)
		} else {
			pretty, marshalErr := json.MarshalIndent(raw, "", "  ")
			if marshalErr != nil {
				return "", "", 0, marshalErr
			}
			body = pretty
		}
	} else {
		ext = ".bin"
		mime = "application/octet-stream"
		switch v := raw.(type) {
		case string:
			body = []byte(v)
		case map[string]any:
			if data, ok := v["data"].(string); ok {
				body = []byte(data)
			} else {
				body, err = json.Marshal(v)
				if err != nil {
					return "", "", 0, err
				}
				ext = ".json"
				mime = "application/json"
			}
		default:
			body, err = json.Marshal(v)
			if err != nil {
				return "", "", 0, err
			}
			ext = ".json"
			mime = "application/json"
		}
	}
	path, err = createExclusiveHandoffFile(dir, name, ext, body)
	if err != nil {
		return "", "", 0, fmt.Errorf("materialize input %q: %w", name, err)
	}
	return path, mime, len(body), nil
}

func createExclusiveHandoffFile(dir, name, ext string, body []byte) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		suffix := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano()+int64(attempt))
		path := filepath.Join(dir, name+"-"+suffix+ext)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", err
		}
		_, writeErr := f.Write(body)
		closeErr := f.Close()
		if writeErr != nil {
			_ = os.Remove(path)
			return "", writeErr
		}
		if closeErr != nil {
			_ = os.Remove(path)
			return "", closeErr
		}
		return path, nil
	}
	return "", fmt.Errorf("exhausted exclusive handoff filename attempts")
}

func validateInputPortName(name string) error {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") || name == "." || name == ".." {
		return fmt.Errorf("input port name %q is not safe for file materialization", name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("input port name %q contains control characters", name)
		}
	}
	return nil
}

func mimeForName(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	case ".json":
		return "application/json"
	default:
		return "application/octet-stream"
	}
}

func confinedPath(workDir, path string) (string, error) {
	if workDir == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("input path must be absolute within the work directory")
	}
	root, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("input path escapes the work directory")
	}
	return resolved, nil
}

// FormatInputSections renders sorted input ports for prompts after handoff.
func FormatInputSections(workDir string, inputs map[string]any) string {
	if len(inputs) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, name := range sortedInputNames(inputs) {
		if name == "feature" || name == "workspace" {
			continue
		}
		sb.WriteString(formatOneInput(workDir, name, inputs[name]))
	}
	return sb.String()
}

func formatOneInput(workDir, name string, val any) string {
	if object, ok := val.(map[string]any); ok {
		if notInlined, _ := object["notInlined"].(bool); notInlined {
			path, _ := object["path"].(string)
			mime, _ := object["mime"].(string)
			size := object["size"]
			return fmt.Sprintf("\n\n--- %s ---\nNOT inlined. Read the full value from this absolute path.\npath: %s\nmime: %s\nsize: %v\nThe file contains the complete input; do not invent a summary.", name, path, mime, size)
		}
	}
	object, structured := val.(map[string]any)
	_, artifact := object["mime"]
	// A domain object may have a path or data field without being an
	// artifact. Keep all its fields instead of reading that path.
	if !structured || artifact {
		if content, ok := InputContent(workDir, val); ok {
			return fmt.Sprintf("\n\n--- %s ---\n%s", name, content)
		}
	}
	encoded, err := json.MarshalIndent(val, "", "  ")
	if err != nil {
		return ""
	}
	return fmt.Sprintf("\n\n--- %s ---\n%s", name, encoded)
}

func formatFeature(workDir string, feature any, localFiles bool) (string, error) {
	if object, ok := feature.(map[string]any); ok {
		if notInlined, _ := object["notInlined"].(bool); notInlined {
			return formatOneInput(workDir, "feature", feature), nil
		}
	}
	switch v := feature.(type) {
	case map[string]any:
		var sb strings.Builder
		sb.WriteString("\n")
		if title, _ := v["title"].(string); title != "" {
			sb.WriteString(fmt.Sprintf("\nFeature title: %s", title))
		}
		if desc, _ := v["feature"].(string); desc != "" {
			sb.WriteString(fmt.Sprintf("\nFeature: %s", desc))
		}
		if files, ok := v["files"].([]any); ok && len(files) > 0 {
			sb.WriteString("\nFiles to read first:")
			for _, f := range files {
				if s, ok := f.(string); ok {
					sb.WriteString(fmt.Sprintf("\n  - %s", s))
				}
			}
		}
		if spec, _ := v["spec"].(string); spec != "" {
			sb.WriteString(fmt.Sprintf("\n\nSpec:\n%s", spec))
		}
		if ctx, _ := v["context"].(string); ctx != "" {
			sb.WriteString(fmt.Sprintf("\n\nContext:\n%s", ctx))
		}
		if id, _ := v["id"].(string); id != "" {
			sb.WriteString(fmt.Sprintf("\n\nFeature ID: %s", id))
		}
		text := sb.String()
		if len(text) <= MaxInlineInputBytes {
			return text, nil
		}
		// Oversized feature object: materialize and point at the file.
		next, _, err := handoffValue(workDir, "feature", v, localFiles, 0)
		if err != nil {
			return "", err
		}
		return formatOneInput(workDir, "feature", next), nil
	case string:
		if len(v) <= MaxInlineInputBytes {
			return fmt.Sprintf("\n\nFeature input:\n%s", v), nil
		}
		next, _, err := handoffValue(workDir, "feature", v, localFiles, 0)
		if err != nil {
			return "", err
		}
		return formatOneInput(workDir, "feature", next), nil
	default:
		return "", nil
	}
}
