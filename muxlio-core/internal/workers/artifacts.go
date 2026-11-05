package workers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/muxlio/muxlio-core/internal/artifacts"
)

// ArtifactRef is a portable content-addressed reference without inline bytes.
type ArtifactRef struct {
	Digest string `json:"digest"`
	Mime   string `json:"mime,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Path   string `json:"path,omitempty"`
}

// StripInlineArtifactBytes removes file bytes from execute requests sent to remote workers.
func StripInlineArtifactBytes(req ExecuteRequest) ExecuteRequest {
	if len(req.Inputs) == 0 {
		return req
	}
	inputs := make(map[string]any, len(req.Inputs))
	for name, value := range req.Inputs {
		inputs[name] = stripArtifactValue(value)
	}
	req.Inputs = inputs
	return req
}

func stripArtifactValue(value any) any {
	m, ok := value.(map[string]any)
	if !ok {
		return value
	}
	digest, _ := m["digest"].(string)
	if digest == "" || !strings.HasPrefix(digest, "sha256:") {
		return value
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if k == "data" {
			continue
		}
		out[k] = v
	}
	return out
}

// ArtifactTransferExecutor downloads named input artifacts and uploads declared outputs.
type ArtifactTransferExecutor struct {
	Inner    Executor
	Client   artifacts.Client
	WorkBase string
}

func (a ArtifactTransferExecutor) Execute(req ExecuteRequest) (ExecuteResult, error) {
	if req.Context != nil && req.Context.Err() != nil {
		return ExecuteResult{}, req.Context.Err()
	}
	var err error
	req.attemptScratch, err = a.workDir(req)
	if err != nil {
		return ExecuteResult{}, err
	}
	if err := a.materializeInputs(&req); err != nil {
		return ExecuteResult{Error: err.Error()}, nil
	}
	// Preparation may outlast the assignment deadline. Check again before
	// invoking an executor that may perform uninterruptible external effects.
	if req.Context != nil && req.Context.Err() != nil {
		return ExecuteResult{}, req.Context.Err()
	}
	result, err := a.Inner.Execute(req)
	if err != nil || result.Error != "" {
		return result, err
	}
	return a.publishOutputs(req, result)
}

func (a ArtifactTransferExecutor) materializeInputs(req *ExecuteRequest) error {
	if len(req.Inputs) == 0 {
		return nil
	}
	workDir, err := a.workDir(*req)
	if err != nil {
		return err
	}
	for name, value := range req.Inputs {
		ref, ok := artifactRefFromValue(value)
		if !ok {
			continue
		}
		fileName := ref.Path
		if fileName == "" {
			fileName = name
		}
		fileName = filepath.Base(fileName)
		destPath := filepath.Join(workDir, fileName)
		f, err := os.Create(destPath)
		if err != nil {
			return fmt.Errorf("muxlio: create input artifact %q: %w", name, err)
		}
		ctx := req.Context
		if ctx == nil {
			ctx = context.Background()
		}
		if err := a.Client.Download(ctx, ref.Digest, ref.Size, f); err != nil {
			_ = f.Close()
			return fmt.Errorf("muxlio: download input artifact %q: %w", name, err)
		}
		if err := f.Close(); err != nil {
			return err
		}
		req.Inputs[name] = map[string]any{
			"digest": ref.Digest,
			"mime":   ref.Mime,
			"size":   ref.Size,
			"path":   destPath,
		}
	}
	return nil
}

func (a ArtifactTransferExecutor) publishOutputs(req ExecuteRequest, result ExecuteResult) (ExecuteResult, error) {
	if len(req.Outputs) == 0 {
		return result, nil
	}
	if result.Outputs == nil {
		result.Outputs = make(map[string]any)
	}
	workDir, err := a.workDir(req)
	if err != nil {
		return ExecuteResult{Error: err.Error()}, nil
	}
	ctx := req.Context
	if ctx == nil {
		ctx = context.Background()
	}
	for port, decl := range req.Outputs {
		// Scalar outputs are declared with a Zod schema and no file path
		// (decl.Path == ""); the adapter returns them as raw JSON values, not
		// files. Pass them through unchanged — only file-artifact outputs
		// (decl.Path != "") are uploaded and rewritten to content-addressed refs.
		if decl.Path == "" {
			continue
		}
		ref, filePath, ok := outputFile(req, result, workDir, port, decl)
		if !ok {
			continue
		}
		if filepath.Clean(ref.Path) != filepath.Clean(filepath.Join(workDir, filepath.Base(decl.Path))) {
			return ExecuteResult{Error: fmt.Sprintf("muxlio: output %q path does not match its declaration", port)}, nil
		}
		f, err := os.Open(filePath)
		if err != nil {
			return ExecuteResult{Error: fmt.Sprintf("muxlio: open output %q: %v", port, err)}, nil
		}
		h := sha256.New()
		size, err := io.Copy(h, f)
		_ = f.Close()
		if err != nil {
			return ExecuteResult{Error: fmt.Sprintf("muxlio: read output %q: %v", port, err)}, nil
		}
		digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
		mime := decl.Mime
		if ref.Mime != "" {
			mime = ref.Mime
		}
		f2, err := os.Open(filePath)
		if err != nil {
			return ExecuteResult{Error: err.Error()}, nil
		}
		uploaded, err := a.Client.Upload(ctx, digest, mime, size, f2)
		_ = f2.Close()
		if err != nil {
			return ExecuteResult{Error: fmt.Sprintf("muxlio: upload output %q: %v", port, err)}, nil
		}
		result.Outputs[port] = map[string]any{
			"digest": uploaded.Digest,
			"mime":   uploaded.Mime,
			"size":   uploaded.Size,
			"path":   decl.Path,
		}
	}
	return result, nil
}

func outputFile(_ ExecuteRequest, result ExecuteResult, workDir, port string, decl OutputDecl) (ArtifactRef, string, bool) {
	if raw, ok := result.Outputs[port]; ok {
		if ref, ok := artifactRefFromValue(raw); ok && ref.Path != "" {
			return ref, ref.Path, true
		}
		if m, ok := raw.(map[string]any); ok {
			if path, _ := m["path"].(string); path != "" {
				if ref, ok := artifactRefFromValue(m); ok {
					return ref, path, true
				}
				return ArtifactRef{Path: path}, path, true
			}
			if data, ok := m["data"].(string); ok {
				name := decl.Path
				if name == "" {
					name = port
				}
				path := filepath.Join(workDir, filepath.Base(name))
				_ = os.WriteFile(path, []byte(data), 0o644)
				return ArtifactRef{Mime: decl.Mime, Path: path}, path, true
			}
		}
	}
	name := decl.Path
	if name == "" {
		name = port
	}
	path := filepath.Join(workDir, filepath.Base(name))
	if _, err := os.Stat(path); err != nil {
		return ArtifactRef{}, "", false
	}
	return ArtifactRef{Mime: decl.Mime, Path: path}, path, true
}

func (a ArtifactTransferExecutor) workDir(req ExecuteRequest) (string, error) {
	if req.attemptScratch != "" {
		return req.attemptScratch, nil
	}
	return newAttemptScratch(req, a.WorkBase)
}

func artifactRefFromValue(value any) (ArtifactRef, bool) {
	m, ok := value.(map[string]any)
	if !ok {
		return ArtifactRef{}, false
	}
	digest, _ := m["digest"].(string)
	if digest == "" || !strings.HasPrefix(digest, "sha256:") {
		return ArtifactRef{}, false
	}
	ref := ArtifactRef{Digest: digest}
	if mime, ok := m["mime"].(string); ok {
		ref.Mime = mime
	}
	if path, ok := m["path"].(string); ok {
		ref.Path = path
	}
	switch size := m["size"].(type) {
	case float64:
		ref.Size = int64(size)
	case int64:
		ref.Size = size
	case int:
		ref.Size = int64(size)
	}
	return ref, true
}

// VerifyOutputArtifacts ensures every declared digest exists in the store.
func VerifyOutputArtifacts(driver artifacts.Driver, outputs map[string]any) error {
	if driver == nil || len(outputs) == 0 {
		return nil
	}
	for port, value := range outputs {
		ref, ok := artifactRefFromValue(value)
		if !ok {
			continue
		}
		stream, storedRef, err := driver.GetStream(context.Background(), ref.Digest)
		if err != nil {
			return fmt.Errorf("muxlio: output artifact %q (%s) is not stored or invalid: %w", port, ref.Digest, err)
		}
		_ = stream.Close()
		if ref.Size > 0 && ref.Size != storedRef.Size {
			return fmt.Errorf("muxlio: output artifact %q size does not match stored content", port)
		}
	}
	return nil
}

// SanitizeExecuteJSON removes inline artifact bytes from a marshalled execute request.
func SanitizeExecuteJSON(raw []byte) ([]byte, error) {
	var req ExecuteRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	req = StripInlineArtifactBytes(req)
	return json.Marshal(req)
}
