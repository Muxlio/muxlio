package artifacts

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestLocalStreamRoundTripAndDigestCheck(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.PutStream(context.Background(), strings.NewReader("streamed payload"), "text/plain", "")
	if err != nil {
		t.Fatal(err)
	}
	reader, got, err := store.GetStream(context.Background(), ref.Digest)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "streamed payload" || got.Size != int64(len(data)) {
		t.Fatalf("got %q size %d", data, got.Size)
	}
	if _, err := store.PutStream(context.Background(), strings.NewReader("different"), "text/plain", ref.Digest); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("expected integrity error, got %v", err)
	}
}

func TestLocalStreamMissingAndCorrupt(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetStream(context.Background(), "sha256:"+strings.Repeat("0", 64)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	ref, err := store.PutStream(context.Background(), strings.NewReader("original"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.path(ref.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetStream(context.Background(), ref.Digest); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("expected integrity error, got %v", err)
	}
}

func TestLocalStreamRejectsCorruptExistingObjectAndNonCanonicalDigest(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.PutStream(context.Background(), strings.NewReader("payload"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	path, _ := store.path(ref.Digest)
	if err := os.WriteFile(path, []byte("broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutStream(context.Background(), strings.NewReader("payload"), "", ""); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("expected corrupt existing object error, got %v", err)
	}
	upper := "sha256:" + strings.ToUpper(ref.Digest[7:])
	if _, _, err := store.GetStream(context.Background(), upper); !errors.Is(err, ErrInvalidDigest) {
		t.Fatalf("expected invalid digest error, got %v", err)
	}
}

func TestStorePutIsolatedFromCallerMutation(t *testing.T) {
	s := New()
	data := []byte("original")
	b, err := s.Put(data, "")
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 'X'
	got, err := s.Get(b.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Data) != "original" {
		t.Fatalf("mutation leaked: %q", got.Data)
	}
	b.Data[0] = 'Y'
	got, err = s.Get(b.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Data) != "original" {
		t.Fatalf("mutation through Put result leaked: %q", got.Data)
	}
	got.Data[0] = 'Z'
	got, err = s.Get(b.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Data) != "original" {
		t.Fatalf("mutation through Get result leaked: %q", got.Data)
	}
}

func TestLocalStreamReturnedReaderHonorsCancellation(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.PutStream(context.Background(), strings.NewReader("payload"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	reader, _, err := store.GetStream(ctx, ref.Digest)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	cancel()
	if _, err := io.ReadAll(reader); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled read, got %v", err)
	}
}
