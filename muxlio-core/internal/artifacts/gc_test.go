package artifacts

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalExpirationIntegrityAndRestore(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Put([]byte("original"), "")
	if err != nil {
		t.Fatal(err)
	}
	path, _ := s.path(b.Digest)
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetStream(context.Background(), b.Digest); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("corruption: %v", err)
	}
	if err := s.Expire(b.Digest); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Get(b.Digest); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	if _, err := reopened.PutStream(context.Background(), strings.NewReader("original"), "", b.Digest); err != nil {
		t.Fatal(err)
	}
	reader, _, err := reopened.GetStream(context.Background(), b.Digest)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil || string(raw) != "original" {
		t.Fatalf("restore %q: %v", raw, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Get(b.Digest); !errors.Is(err, ErrNotFound) {
		t.Fatalf("lost restored data should be missing: %v", err)
	}
}

func TestGCCandidatesProtectReachableAndIgnoreStaging(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Put([]byte("live"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".artifact-upload-pending"), []byte("staged"), 0600); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(time.Hour)
	candidates, err := s.Candidates(map[string]bool{b.Digest: true}, before)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("reachable: %v %v", candidates, err)
	}
	candidates, err = s.Candidates(nil, time.Now().Add(-time.Hour))
	if err != nil || len(candidates) != 0 {
		t.Fatalf("grace: %v %v", candidates, err)
	}
	candidates, err = s.Candidates(nil, before)
	if err != nil || len(candidates) != 1 || candidates[0].Digest != b.Digest {
		t.Fatalf("orphan: %v %v", candidates, err)
	}
}
