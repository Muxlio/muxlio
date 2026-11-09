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
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("output path escapes the work directory")
	}
	full, err := filepath.Abs(filepath.Join(root, clean))
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(full); os.IsNotExist(err) {
		if err := ValidateOutputPath(root, clean); err != nil {
			return "", err
		}
		return full, nil
	} else if err != nil {
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

// ValidateOutputPath checks a declared relative output before execution. It
// rejects lexical traversal and any existing parent symlink that escapes the
// allocated artifact directory while allowing the final file not to exist yet.
func ValidateOutputPath(workDir, name string) error {
	if name == "" || filepath.IsAbs(name) {
		return fmt.Errorf("output path must be relative to the work directory")
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("output path escapes the work directory")
	}
	root, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return err
	}
	current := root
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, e := os.Lstat(current)
		if os.IsNotExist(e) {
			// Once a component is missing, no later path component can exist.
			return nil
		}
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, e := filepath.EvalSymlinks(current)
			if e != nil {
				return e
			}
			rel, e := filepath.Rel(root, resolved)
			if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("output path escapes the work directory")
			}
			current = resolved
		}
	}
	return nil
}
