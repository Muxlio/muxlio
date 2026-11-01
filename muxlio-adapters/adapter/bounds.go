package adapter

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const MaxCaptureBytes = 4 << 20

// BoundedBuffer drains arbitrary provider output while retaining a fixed
// prefix. Write reports consumed bytes so noisy children do not block.
type BoundedBuffer struct{ buffer bytes.Buffer }

func (b *BoundedBuffer) Len() int       { return b.buffer.Len() }
func (b *BoundedBuffer) String() string { return b.buffer.String() }

func (b *BoundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := MaxCaptureBytes - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buffer.Write(p)
	}
	return n, nil
}
func (b *BoundedBuffer) WriteString(s string) (int, error) { return b.Write([]byte(s)) }

func ReadOutput(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxCaptureBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxCaptureBytes {
		return nil, fmt.Errorf("adapter output exceeds %d byte limit", MaxCaptureBytes)
	}
	return data, nil
}

// ResolveOutput confines declared output paths to the worker's work directory,
// including paths that escape through symlinks.
func ResolveOutput(workDir, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", fmt.Errorf("output path must be relative to the work directory")
	}
	root, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	full, err := filepath.Abs(filepath.Join(root, name))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("output path escapes the work directory")
	}
	return resolved, nil
}
