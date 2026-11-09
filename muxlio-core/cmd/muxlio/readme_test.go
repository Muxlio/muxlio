package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDeveloperReferenceDocumentsProductBasics guards the reference against drift from the CLI
// and shipped executor set (see init.go default capabilities).
func TestDeveloperReferenceDocumentsProductBasics(t *testing.T) {
	readmePath := filepath.Join("..", "..", "..", "muxlio-docs", "architecture", "developer-reference.md")
	raw, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read developer reference: %v", err)
	}
	doc := string(raw)

	for _, cmd := range []string{
		"muxlio build",
		"muxlio service start",
		"muxlio service stop",
		"muxlio service restart",
		"muxlio submit",
		"muxlio worker start",
	} {
		if !strings.Contains(doc, cmd) {
			t.Fatalf("developer reference missing %q", cmd)
		}
	}

	for _, exec := range BundledExecutorCapabilities {
		if !strings.Contains(doc, exec) {
			t.Fatalf("developer reference missing executor %q", exec)
		}
	}

	if strings.Contains(doc, "HTTP API has **no authentication**") {
		t.Fatal("developer reference still claims the API has no authentication")
	}
	if !strings.Contains(doc, "127.0.0.1:7600") {
		t.Fatal("developer reference missing default coordinator address")
	}
	if !strings.Contains(doc, "auto-detects capabilities") {
		t.Fatal("developer reference CLI reference missing worker auto-detect note")
	}
	if !strings.Contains(doc, "compiled Plan IR JSON") {
		t.Fatal("developer reference missing submit compiled-plan requirement")
	}

	for _, claim := range []string{
		"Fresh setup uses this checkout as the workspace",
		"repeated setup preserves the configured workspace",
		"The module must default-export an object with a `plan` property",
		"workflow code and its side effects run during compilation",
		"default `~/.muxlio/adapters`",
		"For a release install, pass `--adapters ~/.muxlio/libexec/muxlio/adapters`",
	} {
		if !strings.Contains(doc, claim) {
			t.Errorf("developer reference missing current behavior %q", claim)
		}
	}
	if strings.Contains(doc, "default `~/.muxlio/libexec/muxlio/adapters`. Pass a comma-separated") {
		t.Fatal("developer reference incorrectly documents the release adapter path as the standalone worker default")
	}
}
