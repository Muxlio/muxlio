// Package artifacts provides content-addressed artifact stores.
package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

var (
	ErrNotFound      = errors.New("artifact not found")
	ErrIntegrity     = errors.New("artifact integrity check failed")
	ErrInvalidDigest = errors.New("invalid artifact digest")
)

// Driver stores immutable, content-addressed artifacts. Implementations stream
// payloads; uploads are hashed as they arrive and reads are verified before
// their reader is returned. I/O and context errors are propagated to callers.
type Driver interface {
	PutStream(ctx context.Context, src io.Reader, mime, expectedDigest string) (Ref, error)
	GetStream(ctx context.Context, digest string) (io.ReadCloser, Ref, error)
	Has(digest string) bool
}

// Default creates the filesystem-backed content-addressed driver.
func Default(root string) (Driver, error) { return NewLocal(root) }

// Store is the in-memory content-addressed artifact store.
type Store struct {
	mu    sync.RWMutex
	blobs map[string]*Blob
}

// Local is a filesystem-backed SHA-256 content-addressed store.
type Local struct {
	root string
	mu   sync.Mutex
}

// NewLocal creates a local artifact store rooted at root.
func NewLocal(root string) (*Local, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("muxlio: create artifact dir: %w", err)
	}
	return &Local{root: root}, nil
}

func (s *Local) path(digest string) (string, error) {
	if len(digest) != 71 || digest[:7] != "sha256:" {
		return "", fmt.Errorf("muxlio: %w %q", ErrInvalidDigest, digest)
	}
	// Digests have one canonical textual representation. Accepting upper-case
	// hex here would allow aliases for the same content address.
	for _, c := range digest[7:] {
		if c >= 'A' && c <= 'F' {
			return "", fmt.Errorf("muxlio: %w %q", ErrInvalidDigest, digest)
		}
	}
	if _, err := hex.DecodeString(digest[7:]); err != nil {
		return "", fmt.Errorf("muxlio: %w %q", ErrInvalidDigest, digest)
	}
	return filepath.Join(s.root, digest[7:9], digest[9:]), nil
}

// PutStream incrementally stores an artifact. If expectedDigest is nonempty,
// it must be sha256:<hex> and match the uploaded bytes or the object is rejected.
func (s *Local) PutStream(ctx context.Context, src io.Reader, mime, expectedDigest string) (Ref, error) {
	if expectedDigest != "" {
		if _, err := s.path(expectedDigest); err != nil {
			return Ref{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Ref{}, err
	}
	tmp, err := os.CreateTemp(s.root, ".artifact-upload-*")
	if err != nil {
		return Ref{}, fmt.Errorf("muxlio: create artifact staging file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	h := sha256.New()
	n, err := io.Copy(tmp, io.TeeReader(&contextReader{ctx: ctx, r: src}, h))
	if err != nil {
		tmp.Close()
		return Ref{}, fmt.Errorf("muxlio: read artifact upload: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return Ref{}, fmt.Errorf("muxlio: sync artifact upload: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Ref{}, fmt.Errorf("muxlio: close artifact upload: %w", err)
	}
	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if expectedDigest != "" && digest != expectedDigest {
		return Ref{}, fmt.Errorf("muxlio: uploaded digest %q does not match expected %q: %w", digest, expectedDigest, ErrIntegrity)
	}
	path, _ := s.path(digest)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Ref{}, fmt.Errorf("muxlio: create artifact directory: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		// Do not claim a successful write if an earlier damaged object already
		// occupies the content address.
		existing, err := os.Open(path)
		if err != nil {
			return Ref{}, fmt.Errorf("muxlio: open existing artifact: %w", err)
		}
		existingHash := sha256.New()
		_, readErr := io.Copy(existingHash, existing)
		closeErr := existing.Close()
		if readErr != nil {
			return Ref{}, fmt.Errorf("muxlio: verify existing artifact: %w", readErr)
		}
		if closeErr != nil {
			return Ref{}, fmt.Errorf("muxlio: close existing artifact: %w", closeErr)
		}
		if "sha256:"+hex.EncodeToString(existingHash.Sum(nil)) != digest {
			return Ref{}, fmt.Errorf("muxlio: existing artifact %q: %w", digest, ErrIntegrity)
		}
		if err := s.clearExpired(digest); err != nil {
			return Ref{}, err
		}
		return Ref{Digest: digest, Mime: mime, Size: n}, nil
	} else if !os.IsNotExist(err) {
		return Ref{}, fmt.Errorf("muxlio: inspect artifact: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return Ref{}, fmt.Errorf("muxlio: publish artifact: %w", err)
	}
	if err := s.clearExpired(digest); err != nil {
		return Ref{}, err
	}
	return Ref{Digest: digest, Mime: mime, Size: n}, nil
}

// GetStream verifies the full object before returning a reader, so corrupt
// content is never exposed to a consumer.
func (s *Local) GetStream(ctx context.Context, digest string) (io.ReadCloser, Ref, error) {
	path, err := s.path(digest)
	if err != nil {
		return nil, Ref{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			if s.expired(digest) {
				return nil, Ref{}, fmt.Errorf("muxlio: artifact %q: %w", digest, ErrExpired)
			}
			return nil, Ref{}, fmt.Errorf("muxlio: artifact %q: %w", digest, ErrNotFound)
		}
		return nil, Ref{}, fmt.Errorf("muxlio: open artifact %q: %w", digest, err)
	}
	verified, err := os.CreateTemp(s.root, ".artifact-download-*")
	if err != nil {
		_ = f.Close()
		return nil, Ref{}, fmt.Errorf("muxlio: create artifact verification snapshot: %w", err)
	}
	verifiedName := verified.Name()
	cleanup := func() { _ = f.Close(); _ = verified.Close(); _ = os.Remove(verifiedName) }
	h := sha256.New()
	n, readErr := io.Copy(io.MultiWriter(verified, h), &contextReader{ctx: ctx, r: f})
	_ = f.Close()
	if readErr != nil {
		cleanup()
		return nil, Ref{}, fmt.Errorf("muxlio: read artifact %q: %w", digest, readErr)
	}
	if "sha256:"+hex.EncodeToString(h.Sum(nil)) != digest {
		cleanup()
		return nil, Ref{}, fmt.Errorf("muxlio: artifact %q: %w", digest, ErrIntegrity)
	}
	if err := ctx.Err(); err != nil {
		cleanup()
		return nil, Ref{}, err
	}
	if err := verified.Sync(); err != nil {
		cleanup()
		return nil, Ref{}, fmt.Errorf("muxlio: sync artifact verification snapshot: %w", err)
	}
	if _, err := verified.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, Ref{}, fmt.Errorf("muxlio: rewind artifact %q: %w", digest, err)
	}
	return &snapshotReadCloser{contextReadCloser: contextReadCloser{ctx: ctx, ReadCloser: verified}, path: verifiedName}, Ref{Digest: digest, Size: n}, nil
}

type snapshotReadCloser struct {
	contextReadCloser
	path string
}

func (r *snapshotReadCloser) Close() error {
	err := r.ReadCloser.Close()
	removeErr := os.Remove(r.path)
	if err != nil {
		return err
	}
	return removeErr
}

type contextReadCloser struct {
	ctx context.Context
	io.ReadCloser
}

func (r *contextReadCloser) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.ReadCloser.Read(p)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func (s *Local) Put(data []byte, mime string) (*Blob, error) {
	ref, err := s.PutStream(context.Background(), bytes.NewReader(data), mime, "")
	if err != nil {
		return nil, err
	}
	return &Blob{Digest: ref.Digest, Data: append([]byte(nil), data...), Mime: ref.Mime, Size: ref.Size}, nil
}

func (s *Local) Get(digest string) (*Blob, error) {
	path, err := s.path(digest)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if s.expired(digest) {
			return nil, ErrExpired
		}
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("muxlio: artifact %q: %w", digest, err)
	}
	sum := sha256.Sum256(b)
	if "sha256:"+hex.EncodeToString(sum[:]) != digest {
		return nil, fmt.Errorf("muxlio: artifact %q: %w", digest, ErrIntegrity)
	}
	return &Blob{Digest: digest, Data: b, Size: int64(len(b))}, nil
}

func (s *Local) Has(digest string) bool { _, err := s.Get(digest); return err == nil }

// Blob is a content-addressed artifact with metadata.
type Blob struct {
	Digest string
	Data   []byte
	Mime   string
	Size   int64
}

// New creates an in-memory artifact store.
func New() *Store {
	return &Store{blobs: make(map[string]*Blob)}
}

// Put stores a blob and returns its digest.
func (s *Store) Put(data []byte, mime string) (*Blob, error) {
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.blobs[digest]; ok {
		return existing, nil // dedup by digest
	}
	blob := &Blob{
		Digest: digest,
		Data:   data,
		Mime:   mime,
		Size:   int64(len(data)),
	}
	s.blobs[digest] = blob
	return blob, nil
}

// Get retrieves a blob by digest.
func (s *Store) Get(digest string) (*Blob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	blob, ok := s.blobs[digest]
	if !ok {
		return nil, fmt.Errorf("muxlio: artifact %q not found", digest)
	}
	return blob, nil
}

// Has reports whether a digest exists in the store.
func (s *Store) Has(digest string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.blobs[digest]
	return ok
}

// List returns all stored blobs.
func (s *Store) List() []*Blob {
	s.mu.RLock()
	defer s.mu.RUnlock()
	blobs := make([]*Blob, 0, len(s.blobs))
	for _, b := range s.blobs {
		blobs = append(blobs, b)
	}
	return blobs
}

// Ref is a typed reference to an artifact.
type Ref struct {
	Digest string `json:"digest"`
	Mime   string `json:"mime"`
	Size   int64  `json:"size"`
}
