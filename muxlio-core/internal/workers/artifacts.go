package workers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
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
				rel, err := declaredOutputPath(decl.Path)
				if err != nil {
					continue
				}
				// Keep both inspection and reads rooted in the attempt directory,
				// including concurrent symlink replacements.
				root, err := os.OpenRoot(req.attemptScratch)
				if err != nil {
					continue
				}
				info, err := root.Lstat(rel)
				if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
					root.Close()
					continue
				}
				f, err := root.Open(rel)
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
		ref, ok, err := artifactRefFromValue(value)
		if err != nil {
			return fmt.Errorf("muxlio: artifact %q: %w", name, err)
		}
		if !ok {
			continue
		}
		fileName := ref.Path
		if fileName == "" {
			fileName = name
		}
		rel := filepath.Base(fileName)
		if declared, pathErr := declaredOutputPath(fileName); pathErr == nil && strings.Contains(declared, string(filepath.Separator)) {
			rel = declared
		}
		destPath := filepath.Join(workDir, rel)
		if dir := filepath.Dir(destPath); dir != workDir {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("muxlio: prepare input artifact %q: %w", name, err)
			}
		}
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
		ref, filePath, ok, err := outputFile(req, result, workDir, port, decl)
		if err != nil {
			return ExecuteResult{Error: fmt.Sprintf("muxlio: materialize output %q: %v", port, err)}, nil
		}
		if !ok {
			continue
		}
		rel, err := declaredOutputPath(decl.Path)
		if err != nil {
			return ExecuteResult{Error: fmt.Sprintf("muxlio: output %q has invalid declared path", port)}, nil
		}
		if filepath.Clean(ref.Path) != filepath.Clean(filepath.Join(workDir, rel)) || filepath.Clean(filePath) != filepath.Clean(filepath.Join(workDir, rel)) {
			return ExecuteResult{Error: fmt.Sprintf("muxlio: output %q path does not match its declaration", port)}, nil
		}
		root, err := os.OpenRoot(workDir)
		if err != nil {
			return ExecuteResult{Error: fmt.Sprintf("muxlio: open output workspace: %v", err)}, nil
		}
		f, err := root.Open(rel)
		if err != nil {
			_ = root.Close()
			return ExecuteResult{Error: fmt.Sprintf("muxlio: open output %q: %v", port, err)}, nil
		}
		// Stage the exact scanned bytes so concurrent adapter writes cannot swap
		// credentials into the upload after validation. Bound staging as well as
		// the transport so oversized outputs cannot exhaust the worker disk.
		staged, err := os.CreateTemp(workDir, ".publish-*")
		if err != nil {
			f.Close()
			_ = root.Close()
			return ExecuteResult{}, err
		}
		defer os.Remove(staged.Name())
		defer staged.Close()
		h := sha256.New()
		scanner := &credentialWriter{dst: io.MultiWriter(staged, h), secrets: executionSecrets(req)}
		size, err := io.Copy(scanner, io.LimitReader(f, artifacts.MaxTransferBytes+1))
		_ = f.Close()
		_ = root.Close()
		if err != nil {
			return ExecuteResult{Error: fmt.Sprintf("muxlio: read output %q: %v", port, err)}, nil
		}
		if size > artifacts.MaxTransferBytes {
			return ExecuteResult{Error: fmt.Sprintf("muxlio: output %q exceeds artifact transfer limit", port)}, nil
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

func declaredOutputPath(name string) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", fmt.Errorf("path must be relative")
	}
	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes workspace")
	}
	return clean, nil
}

func outputFile(_ ExecuteRequest, result ExecuteResult, workDir, port string, decl OutputDecl) (ArtifactRef, string, bool, error) {
	if raw, ok := result.Outputs[port]; ok {
		ref, ok, err := artifactRefFromValue(raw)
		if err != nil {
			return ArtifactRef{}, "", false, err
		}
		if ok && ref.Path != "" {
			return ref, ref.Path, true, nil
		}
		if m, ok := raw.(map[string]any); ok {
			if path, _ := m["path"].(string); path != "" {
				return ArtifactRef{Path: path}, path, true, nil
			}
			if data, ok := m["data"].(string); ok {
				name := decl.Path
				if name == "" {
					name = port
				}
				rel, err := declaredOutputPath(name)
				if err != nil {
					return ArtifactRef{}, "", false, err
				}
				root, err := os.OpenRoot(workDir)
				if err != nil {
					return ArtifactRef{}, "", false, err
				}
				defer root.Close()
				// Rooted operations enforce containment during directory creation
				// and truncation, even if the executor replaces a symlink.
				if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
					return ArtifactRef{}, "", false, err
				}
				if err := root.WriteFile(rel, []byte(data), 0o644); err != nil {
					return ArtifactRef{}, "", false, err
				}
				path := filepath.Join(workDir, rel)
				return ArtifactRef{Mime: decl.Mime, Path: path}, path, true, nil
			}
		}
	}
	name := decl.Path
	if name == "" {
		name = port
	}
	rel, err := declaredOutputPath(name)
	if err != nil {
		return ArtifactRef{}, "", false, nil
	}
	path := filepath.Join(workDir, rel)
	if _, err := os.Stat(path); err != nil {
		return ArtifactRef{}, "", false, nil
	}
	return ArtifactRef{Mime: decl.Mime, Path: path}, path, true, nil
}

func (a ArtifactTransferExecutor) workDir(req ExecuteRequest) (string, error) {
	if req.attemptScratch != "" {
		return req.attemptScratch, nil
	}
	return newAttemptScratch(req, a.WorkBase)
}

func artifactRefFromValue(value any) (ArtifactRef, bool, error) {
	m, ok := value.(map[string]any)
	if !ok {
		return ArtifactRef{}, false, nil
	}
	digest, _ := m["digest"].(string)
	if digest == "" || !strings.HasPrefix(digest, "sha256:") {
		return ArtifactRef{}, false, nil
	}
	ref := ArtifactRef{Digest: digest}
	if mime, ok := m["mime"].(string); ok {
		ref.Mime = mime
	}
	if path, ok := m["path"].(string); ok {
		ref.Path = path
	}
	if size, present := m["size"]; present {
		n, err := artifactSize(size)
		if err != nil {
			return ArtifactRef{}, true, err
		}
		ref.Size = n
	}
	return ref, true, nil
}

// artifactSize rejects invalid sizes rather than turning them into an omitted
// integrity constraint. JSON numbers arrive unchanged from execution inputs.
func artifactSize(value any) (int64, error) {
	var n int64
	switch size := value.(type) {
	case json.Number:
		var err error
		n, err = integralArtifactSize(size)
		if err != nil {
			return 0, fmt.Errorf("invalid artifact size: %w", err)
		}
	case float64:
		if math.IsNaN(size) || size < 0 || size >= 0x1p63 || math.Trunc(size) != size {
			return 0, fmt.Errorf("invalid artifact size %v", size)
		}
		n = int64(size)
	case int64:
		n = size
	case int:
		n = int64(size)
	default:
		return 0, fmt.Errorf("invalid artifact size %v", value)
	}
	if n < 0 {
		return 0, fmt.Errorf("invalid artifact size %v", value)
	}
	return n, nil
}

// integralArtifactSize converts decimal JSON exactly, keeping the exponent
// separate so enormous exponents cannot cause magnitude-sized allocations.
func integralArtifactSize(number json.Number) (int64, error) {
	text := string(number)
	if !json.Valid([]byte(text)) || len(text) == 0 || (text[0] != '-' && (text[0] < '0' || text[0] > '9')) {
		return 0, fmt.Errorf("invalid JSON number %q", text)
	}
	negative := strings.HasPrefix(text, "-")
	text = strings.TrimPrefix(text, "-")
	exponent := new(big.Int)
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		if _, ok := exponent.SetString(text[i+1:], 10); !ok {
			return 0, fmt.Errorf("invalid JSON exponent")
		}
		text = text[:i]
	}
	if i := strings.IndexByte(text, '.'); i >= 0 {
		exponent.Sub(exponent, big.NewInt(int64(len(text)-i-1)))
		text = text[:i] + text[i+1:]
	}
	text = strings.TrimLeft(text, "0")
	if text == "" {
		return 0, nil
	}
	coefficient := strings.TrimRight(text, "0")
	exponent.Add(exponent, big.NewInt(int64(len(text)-len(coefficient))))
	if negative || exponent.Sign() < 0 || len(coefficient) > 19 || exponent.Cmp(big.NewInt(int64(19-len(coefficient)))) > 0 {
		return 0, fmt.Errorf("artifact size is not a nonnegative int64")
	}
	return strconv.ParseInt(coefficient+strings.Repeat("0", int(exponent.Int64())), 10, 64)
}

// VerifyOutputArtifacts ensures every declared digest exists in the store.
func VerifyOutputArtifacts(driver artifacts.Driver, outputs map[string]any) error {
	if driver == nil || len(outputs) == 0 {
		return nil
	}
	for port, value := range outputs {
		ref, ok, err := artifactRefFromValue(value)
		if err != nil {
			return fmt.Errorf("muxlio: output artifact %q: %w", port, err)
		}
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
	if err := contract.DecodeJSON(raw, &req); err != nil {
		return nil, err
	}
	req = StripInlineArtifactBytes(req)
	return json.Marshal(req)
}
