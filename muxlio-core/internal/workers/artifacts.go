package workers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/state"
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
	// Snapshot publishes provenance only after immutable bytes reach the store.
	Snapshot         func(seq int, snapshot state.ArtifactSnapshot) error
	SnapshotInterval time.Duration
}

func (a ArtifactTransferExecutor) Execute(req ExecuteRequest) (result ExecuteResult, resultErr error) {
	defer func() {
		result, resultErr = sanitizeExecution(result, resultErr, executionSecrets(req))
	}()
	if req.Context != nil && req.Context.Err() != nil {
		return ExecuteResult{}, req.Context.Err()
	}
	var err error
	if resolver, ok := a.Inner.(interface {
		resolveSecrets(ExecuteRequest) (ExecuteRequest, error)
	}); ok {
		req, err = resolver.resolveSecrets(req)
		if err != nil {
			return ExecuteResult{Error: err.Error()}, nil
		}
	}
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
	stopSnapshots := a.startSnapshots(req)
	result, err = a.Inner.Execute(req)
	stopSnapshots()
	if err != nil || result.Error != "" {
		return result, err
	}
	return a.publishOutputs(req, result)
}

// startSnapshots observes declared files only. It copies bytes before hashing
// and uploading so later writes can never change a published content address.
func (a ArtifactTransferExecutor) startSnapshots(req ExecuteRequest) func() {
	if a.Snapshot == nil || len(req.Outputs) == 0 {
		return func() {}
	}
	ctx := req.Context
	if ctx == nil {
		ctx = context.Background()
	}
	stop, done := make(chan struct{}), make(chan struct{})
	interval := a.SnapshotInterval
	if interval <= 0 {
		interval = time.Second
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		last := map[string]string{}
		seq, remaining := 0, int64(state.MaxAttemptSnapshotBytes)
		capture := func() {
			for name, decl := range req.Outputs {
				if decl.Path == "" || seq >= state.MaxAttemptSnapshots || remaining <= 0 || ctx.Err() != nil {
					continue
				}
				path := filepath.Join(req.attemptScratch, filepath.Base(decl.Path))
				info, err := os.Lstat(path)
				if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
					continue
				}
				// os.Root rejects symlinks escaping the attempt directory, including
				// a replacement between Lstat and Open.
				root, err := os.OpenRoot(req.attemptScratch)
				if err != nil {
					continue
				}
				f, err := root.Open(filepath.Base(decl.Path))
				root.Close()
				if err != nil {
					continue
				}
				data, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
				f.Close()
				if err != nil || len(data) > 4<<20 || int64(len(data)) > remaining || containsSecret(data, executionSecrets(req)) {
					continue
				}
				hash := sha256.Sum256(data)
				digest := "sha256:" + hex.EncodeToString(hash[:])
				if last[name] == digest {
					continue
				}
				ref, err := a.Client.Upload(ctx, digest, decl.Mime, int64(len(data)), bytes.NewReader(data))
				if err != nil {
					continue
				}
				seq++
				remaining -= ref.Size
				if a.Snapshot(seq, state.ArtifactSnapshot{Name: name, Digest: ref.Digest, Mime: ref.Mime, Size: ref.Size}) == nil {
					last[name] = digest
				}
			}
		}
		for {
			select {
			case <-ticker.C:
				capture()
			case <-stop:
				capture()
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() { close(stop); <-done }
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
		// Stage the exact scanned bytes so concurrent adapter writes cannot swap
		// credentials into the upload after validation. Do not cap artifact size.
		staged, err := os.CreateTemp(workDir, ".publish-*")
		if err != nil {
			f.Close()
			return ExecuteResult{}, err
		}
		defer os.Remove(staged.Name())
		defer staged.Close()
		h := sha256.New()
		scanner := &credentialWriter{dst: io.MultiWriter(staged, h), secrets: executionSecrets(req)}
		size, err := io.Copy(scanner, f)
		_ = f.Close()
		if err != nil {
			return ExecuteResult{Error: fmt.Sprintf("muxlio: read output %q: %v", port, err)}, nil
		}
		digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
		mime := decl.Mime
		if ref.Mime != "" {
			mime = ref.Mime
		}
		if _, err := staged.Seek(0, io.SeekStart); err != nil {
			return ExecuteResult{}, err
		}
		uploaded, err := a.Client.Upload(ctx, digest, mime, size, staged)
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
