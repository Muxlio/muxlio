package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestAdapterNameToCapability(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"agent-claude", "agent/claude"},
		{"agent-codex", "agent/codex"},
		{"agent-cursor", "agent/cursor"},
		{"agent-gemini-cli", "agent/gemini-cli"},
		{"command-go-test", "command/go-test"},
		{"agent-kiro", "agent/kiro"},
		{"standalone", "standalone"}, // no dash → unchanged
	}
	for _, c := range cases {
		got := adapterNameToCapability(c.in)
		if got != c.want {
			t.Errorf("adapterNameToCapability(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDiscoverCapabilities(t *testing.T) {
	dir := t.TempDir()
	// Executable adapters.
	write := func(name string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("agent-claude", 0o755)
	write("agent-codex", 0o755)
	write("agent-cursor", 0o755)
	write("agent-gemini-cli", 0o755)
	write("agent-kiro", 0o755)
	write("command-go-test", 0o755)
	// Non-executable — must be skipped.
	write("README.md", 0o644)
	// Hidden — must be skipped.
	write(".gitkeep", 0o755)
	// Subdir — must be skipped.
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	caps, err := discoverCapabilities(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(caps))
	for i, c := range caps {
		got[i] = c.Name
	}
	sort.Strings(got)
	want := append([]string(nil), BundledExecutorCapabilities...)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discoverCapabilities = %v, want %v", got, want)
	}
}

func TestDiscoverCapabilities_Empty(t *testing.T) {
	dir := t.TempDir()
	caps, err := discoverCapabilities(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(caps) != 0 {
		t.Errorf("expected no capabilities, got %v", caps)
	}
}

func TestDiscoverCapabilities_Missing(t *testing.T) {
	_, err := discoverCapabilities("/nonexistent/path/adapters")
	if err == nil {
		t.Error("expected error for missing directory, got nil")
	}
}

// Verify round-trip: adapterNameToCapability is the inverse of scriptPath naming.
func TestCapabilityRoundTrip(t *testing.T) {
	caps := BundledExecutorCapabilities
	for _, name := range caps {
		c := workers.RemoteCapability{Name: name}
		// scriptPath converts "/" → "-"
		filename := strings.ReplaceAll(c.Name, "/", "-")
		got := adapterNameToCapability(filename)
		if got != c.Name {
			t.Errorf("round-trip %q → %q → %q, want %q", c.Name, filename, got, c.Name)
		}
	}
}
