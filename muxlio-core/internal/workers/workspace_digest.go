package workers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const WorkspaceDigestLabelPrefix = "muxlio.dev/workspace-digest/"

var workspaceDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func ValidateWorkspaceDigest(digest string) error {
	if digest != "" && !workspaceDigestPattern.MatchString(digest) {
		return fmt.Errorf("workspaceDigest must be a SHA-256 content digest")
	}
	return nil
}

// WorkspaceDigest addresses a prepared tree independently of its host path.
// v1 hashes ordered JSON records of relative paths, types, owner executable
// bits and file digests/link targets. Git administration files are excluded,
// exactly as in attempt snapshots. No Git repository is required.
func WorkspaceDigest(path string) (string, error) {
	source, err := CanonicalWorkspace(path)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return "", err
	}
	defer root.Close()
	hash := sha256.New()
	io.WriteString(hash, "muxlio-workspace-v1\n")
	encoder := json.NewEncoder(hash)
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil || rel == "." {
			return err
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// JSON repairs invalid UTF-8 strings, which would give distinct Unix
		// names the same identity. Reject them instead of hashing lossy paths.
		if !utf8.ValidString(rel) {
			return fmt.Errorf("workspace rejects non-UTF-8 path %q", rel)
		}
		record := struct {
			Path       string `json:"path"`
			Kind       string `json:"kind"`
			Executable bool   `json:"executable,omitempty"`
			Content    string `json:"content,omitempty"`
		}{Path: filepath.ToSlash(rel)}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			record.Kind = "symlink"
			link, err := root.Readlink(rel)
			if err != nil {
				return err
			}
			if !utf8.ValidString(link) {
				return fmt.Errorf("workspace rejects non-UTF-8 symlink target %q", rel)
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(rel), link))
			if filepath.IsAbs(link) || resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) {
				return fmt.Errorf("workspace rejects symlink outside workspace %q", rel)
			}
			if _, err := root.Stat(rel); err != nil {
				return err
			}
			record.Content = link
		case entry.IsDir():
			record.Kind = "directory"
		case info.Mode().IsRegular():
			record.Kind, record.Executable = "file", info.Mode().Perm()&0o100 != 0
			file, err := root.Open(rel)
			if err != nil {
				return err
			}
			content := sha256.New()
			_, copyErr := io.Copy(content, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			record.Content = hex.EncodeToString(content.Sum(nil))
		default:
			return fmt.Errorf("workspace rejects non-regular file %q", rel)
		}
		return encoder.Encode(record)
	})
	if err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func checkPreparedWorkspace(path, expected string) error {
	if expected == "" {
		return nil
	}
	if err := ValidateWorkspaceDigest(expected); err != nil {
		return err
	}
	actual, err := WorkspaceDigest(path)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("workspace content revision mismatch: expected %s, found %s; provision the matching prepared workspace", expected, actual)
	}
	return nil
}
