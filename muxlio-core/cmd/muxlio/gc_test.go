package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func TestGCCommandReviewPinAndRuntimeLock(t *testing.T) {
	root := t.TempDir()
	db, err := sqlite.Open(filepath.Join(root, "data", "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	local, err := artifacts.NewLocal(filepath.Join(root, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := local.Put([]byte("orphan"), "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "artifacts", b.Digest[7:9], b.Digest[9:])
	old := time.Now().Add(-1000 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	invoke := func(args ...string) (int, []byte) {
		t.Helper()
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		original := os.Stdout
		os.Stdout = writer
		defer func() { os.Stdout = original }()
		code := cmdGC(args, root)
		writer.Close()
		raw, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		return code, raw
	}
	if code, _ := invoke("--pin-artifact", b.Digest); code != 0 {
		t.Fatal("pin failed", code)
	}
	code, raw := invoke("--dry-run")
	if code != 0 {
		t.Fatal(code, string(raw))
	}
	var preview sqlite.GCReport
	if err := json.Unmarshal(raw, &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Artifacts) != 0 {
		t.Fatal("pin did not protect artifact")
	}
	if code, _ := invoke("--unpin-artifact", b.Digest); code != 0 {
		t.Fatal("unpin failed", code)
	}
	code, raw = invoke()
	if code != 0 {
		t.Fatal(code, string(raw))
	}
	if err := json.Unmarshal(raw, &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Artifacts) != 1 || preview.Applied || !local.Has(b.Digest) {
		t.Fatal("preview changed content", string(raw))
	}
	if code, _ := invoke("--dry-run", "--apply", preview.Token); code == 0 {
		t.Fatal("dry run accepted destructive flag")
	}
	if code, _ := invoke("--apply", "unreviewed"); code == 0 {
		t.Fatal("unreviewed cleanup accepted")
	}
	if !local.Has(b.Digest) {
		t.Fatal("invalid approval removed bytes")
	}
	lock, err := os.OpenFile(filepath.Join(root, "service.lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if code, _ := invoke("--apply", preview.Token); code == 0 {
		t.Fatal("cleanup raced running coordinator")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	lock.Close()
	if code, raw := invoke("--apply", preview.Token); code != 0 {
		t.Fatal(code, string(raw))
	}
	if local.Has(b.Digest) {
		t.Fatal("approved cleanup did not remove artifact")
	}
}
