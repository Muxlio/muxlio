package adapter

import (
	"strings"
	"testing"
)

func TestParseManifestNilReturnsEmptyManifest(t *testing.T) {
	m, err := ParseManifest(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil manifest for nil input")
	}
	if m.Supports(CapabilityCancel) {
		t.Error("empty manifest must not support any capability")
	}
}

func TestParseManifestEmptyBytesReturnsEmptyManifest(t *testing.T) {
	m, err := ParseManifest([]byte{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil || m.Supports(CapabilityProgress) {
		t.Error("empty manifest must not support any capability")
	}
}

func TestParseManifestMinimalDescriptor(t *testing.T) {
	raw := []byte(`{"executor":"agent/claude","executorVersion":"1.0.0"}`)
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Executor != "agent/claude" {
		t.Errorf("executor = %q", m.Executor)
	}
	if m.Supports(CapabilityArtifacts) {
		t.Error("minimal manifest must not claim artifacts capability")
	}
}

func TestParseManifestWithCapabilities(t *testing.T) {
	raw := []byte(`{
		"manifestVersion":"1",
		"executor":"agent/claude",
		"executorVersion":"2.0.0",
		"capabilities":{"artifacts":true,"progress":true}
	}`)
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !m.Supports(CapabilityArtifacts) {
		t.Error("expected artifacts capability")
	}
	if !m.Supports(CapabilityProgress) {
		t.Error("expected progress capability")
	}
	if m.Supports(CapabilityCancel) {
		t.Error("cancel must not be supported when absent from descriptor")
	}
	if m.Supports(CapabilitySession) {
		t.Error("session must not be supported when absent from descriptor")
	}
	if m.Supports(CapabilityRemoteTask) {
		t.Error("remoteTask must not be supported when absent from descriptor")
	}
}

func TestParseManifestRequiresHostExecution(t *testing.T) {
	raw := []byte(`{"requiresHostExecution":true}`)
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !m.RequiresHostExecution {
		t.Error("expected requiresHostExecution = true")
	}
}

func TestParseManifestRejectsUnknownVersion(t *testing.T) {
	raw := []byte(`{"manifestVersion":"99"}`)
	_, err := ParseManifest(raw)
	if err == nil {
		t.Fatal("expected error for unknown manifest version")
	}
	if !strings.Contains(err.Error(), "unsupported version") {
		t.Errorf("error = %v, want 'unsupported version'", err)
	}
}

func TestParseManifestRejectsInvalidJSON(t *testing.T) {
	_, err := ParseManifest([]byte(`{not json}`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestManifestNilReceiverSupportsFalse(t *testing.T) {
	var m *AdapterManifest
	for _, cap := range []string{CapabilityCancel, CapabilityProgress, CapabilityArtifacts, CapabilitySession, CapabilityRemoteTask} {
		if m.Supports(cap) {
			t.Errorf("nil manifest must not support %q", cap)
		}
	}
}

func TestUnsupportedCapabilityErrorFormatting(t *testing.T) {
	err := &UnsupportedCapabilityError{Capability: CapabilitySession, Executor: "agent/codex"}
	got := err.Error()
	if !strings.Contains(got, "agent/codex") || !strings.Contains(got, CapabilitySession) {
		t.Errorf("error format = %q", got)
	}
}

func TestUnsupportedCapabilityErrorNoExecutor(t *testing.T) {
	err := &UnsupportedCapabilityError{Capability: CapabilityRemoteTask}
	got := err.Error()
	if !strings.Contains(got, CapabilityRemoteTask) {
		t.Errorf("error format = %q", got)
	}
}
