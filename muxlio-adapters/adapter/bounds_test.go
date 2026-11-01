package adapter

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoundedCaptureDrainsNoisyProvider(t *testing.T) {
	var capture BoundedBuffer
	input := strings.Repeat("x", MaxCaptureBytes+1024)
	n, err := io.Copy(&capture, strings.NewReader(input))
	if err != nil || n != int64(len(input)) || capture.Len() != MaxCaptureBytes {
		t.Fatalf("copied=%d retained=%d err=%v", n, capture.Len(), err)
	}
	_, _ = capture.WriteString("more output")
	if capture.Len() != MaxCaptureBytes {
		t.Fatal("capture exceeded bound")
	}
}

func TestReadOutputRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(MaxCaptureBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err = ReadOutput(path); err == nil {
		t.Fatal("oversized output accepted")
	}
	if err = os.WriteFile(path, []byte("small output"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := ReadOutput(path)
	if err != nil || string(data) != "small output" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}
