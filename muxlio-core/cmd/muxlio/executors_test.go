package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestBundledAdapterBasenamesRoundTrip(t *testing.T) {
	for _, base := range bundledAdapterBasenames() {
		got := adapterNameToCapability(base)
		found := false
		for _, cap := range BundledExecutorCapabilities {
			if cap == got {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("adapter basename %q maps to %q, not in BundledExecutorCapabilities", base, got)
		}
	}
	if len(bundledAdapterBasenames()) != len(BundledExecutorCapabilities) {
		t.Fatalf("adapter basenames count %d != capabilities count %d",
			len(bundledAdapterBasenames()), len(BundledExecutorCapabilities))
	}
}

func TestBundledExecutorCapabilitiesIncludeShippedAgents(t *testing.T) {
	required := []string{
		"agent/claude",
		"agent/codex",
		"agent/cursor",
		"agent/gemini-cli",
		"agent/kiro",
		"command/go-test",
		"command",
	}
	for _, want := range required {
		found := false
		for _, cap := range BundledExecutorCapabilities {
			if cap == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("BundledExecutorCapabilities missing %q: %v", want, BundledExecutorCapabilities)
		}
	}
}

func TestDiscoverCapabilitiesMatchesBundledSet(t *testing.T) {
	dir := t.TempDir()
	for _, base := range bundledAdapterBasenames() {
		if err := os.WriteFile(dir+"/"+base, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
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
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("discoverCapabilities = %v, want %v", got, want)
	}
}

func TestServiceDocDocumentsBundledExecutors(t *testing.T) {
	path := filepath.Join("..", "..", "..", "muxlio-docs", "service.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read service.md: %v", err)
	}
	doc := string(raw)
	for _, exec := range BundledExecutorCapabilities {
		if !strings.Contains(doc, exec) {
			t.Fatalf("muxlio-docs/service.md missing executor %q", exec)
		}
	}
	base := strings.ReplaceAll("agent/claude", "/", "-")
	if !strings.Contains(doc, base) {
		t.Fatalf("muxlio-docs/service.md missing adapter binary name %q", base)
	}
}
