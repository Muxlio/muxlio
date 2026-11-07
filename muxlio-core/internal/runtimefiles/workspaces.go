// Package runtimefiles owns filesystem names shared by runtime execution and cleanup.
package runtimefiles

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func PathPart(value string) string {
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return fmt.Sprintf("~%x", sha256.Sum256([]byte(value)))
		}
	}
	if value == "" {
		return "unknown"
	}
	return value
}

type WorkspaceCandidate struct {
	RunID       string `json:"runId"`
	Directory   string `json:"directory"`
	Bytes       int64  `json:"bytes"`
	Fingerprint string `json:"fingerprint"`
}

// WorkspacePreview never follows symlinks. Unknown run directories are not candidates.
func WorkspacePreview(root, runID string) (*WorkspaceCandidate, error) {
	name := PathPart(runID)
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil
	}
	candidate := &WorkspaceCandidate{RunID: runID, Directory: name}
	hash := sha256.New()
	err = filepath.WalkDir(path, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%q %d %d %d\n", rel, info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			candidate.Bytes += info.Size()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	candidate.Fingerprint = fmt.Sprintf("%x", hash.Sum(nil))
	return candidate, nil
}

// RemoveWorkspace is confined to the validated root, including under symlink races.
func RemoveWorkspace(root string, candidate WorkspaceCandidate) error {
	if candidate.Directory != PathPart(candidate.RunID) || strings.ContainsAny(candidate.Directory, "/\\") {
		return fmt.Errorf("invalid workspace candidate")
	}
	before, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("workspace root is not a directory")
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	opened, err := dir.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(before, opened) {
		return fmt.Errorf("workspace root changed")
	}
	info, err := dir.Lstat(candidate.Directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("workspace candidate changed")
	}
	// Tool caches can contain read-only directories. Make only directories in
	// this approved workspace traversable and removable; never follow symlinks.
	err = fs.WalkDir(dir.FS(), candidate.Directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		file, err := dir.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0700 != 0700 {
			return file.Chmod(info.Mode().Perm() | 0700)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return dir.RemoveAll(candidate.Directory)
}
