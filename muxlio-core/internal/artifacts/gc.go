package artifacts

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrExpired means GC deliberately removed an artifact. It is distinct from
// an unknown address and from bytes that fail digest verification.
var ErrExpired = errors.New("artifact expired by retention")

type Candidate struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// Candidates inventories only canonical CAS objects, never transfer staging
// files or tombstones. The caller must hold the runtime maintenance lock.
func (s *Local) Candidates(reachable map[string]bool, before time.Time) ([]Candidate, error) {
	result := []Candidate{}
	err := filepath.WalkDir(s.root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 62 {
			return nil
		}
		digest := "sha256:" + parts[0] + parts[1]
		if _, err := s.path(digest); err != nil {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && !reachable[digest] && info.ModTime().Before(before) {
			result = append(result, Candidate{Digest: digest, Size: info.Size()})
		}
		return nil
	})
	sort.Slice(result, func(i, j int) bool { return result[i].Digest < result[j].Digest })
	return result, err
}

func (s *Local) expired(digest string) bool {
	_, err := os.Stat(filepath.Join(s.root, ".expired", digest[7:]))
	return err == nil
}

// Expire records intent durably before unlinking bytes, so an interrupted GC
// can be retried without confusing an intentional removal with data loss.
func (s *Local) Expire(digest string) error {
	path, err := s.path(digest)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.root, ".expired")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, digest[7:]), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// Sync directory entries as well as the marker contents before deleting
	// bytes. Otherwise a power loss could lose the newly created tombstone.
	if err := syncDirectory(dir); err != nil {
		return err
	}
	if err := syncDirectory(s.root); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (s *Local) clearExpired(digest string) error {
	// Publish the restored object's directory entry durably before forgetting
	// expiration. Then persist marker removal so later data loss is not reported
	// as an old intentional expiration after a restart.
	path, err := s.path(digest)
	if err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	dir := filepath.Join(s.root, ".expired")
	err = os.Remove(filepath.Join(dir, digest[7:]))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncDirectory(dir)
}
