package workers

import (
	"encoding/json"
	"errors"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
)

// writeManifest writes an adapter descriptor JSON file next to a fake
// executable stub and returns the stub's path.
func writeManifest(t *testing.T, dir, name string, m adapterManifest) string {
	t.Helper()
	// Write a minimal executable stub so CanExecute does not reject the path.
	stub := filepath.Join(dir, name)
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stub+".json", raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return stub
}

func TestCompatibilityRejectsBeforeProcessLaunch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ExecuteRequest)
		want   string
	}{
		{"contract", func(r *ExecuteRequest) { r.ContractVersion = "99" }, "contract version"},
		{"executor_version", func(r *ExecuteRequest) { r.ExecutorVersion = "99" }, "version"},
		{"reconcile", func(r *ExecuteRequest) { r.Action = "reconcile" }, "reconcile"},
		{"idempotency", func(r *ExecuteRequest) { r.IdempotencyKey = "stable" }, "idempotencyKey"},
		{"session", func(r *ExecuteRequest) { r.SessionID = "existing" }, "session"},
		{"legacy_session", func(r *ExecuteRequest) { r.Params = json.RawMessage(`{"sessionId":"existing"}`) }, "session"},
		{"remote", func(r *ExecuteRequest) { r.RemoteTask = &contract.RemoteTaskReference{ID: "task"} }, "remoteTask"},
		{"typed", func(r *ExecuteRequest) { r.RequiredCapabilities = map[string]string{"typedResult": "1"} }, "typedResult"},
		{"progress_version", func(r *ExecuteRequest) { r.RequiredCapabilities = map[string]string{"progress": "2"} }, "progress"},
		{"artifacts", func(r *ExecuteRequest) { r.Outputs = map[string]OutputDecl{"report": {Path: "report"}} }, "artifacts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testScript(t, `touch launched; printf '{}'`)
			m := adapterManifest{ManifestVersion: "1", ContractVersion: "1", Executor: "test", ExecutorVersion: "1", Capabilities: map[string]bool{"progress": true}}
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s.scriptPath("test")+".json", raw, 0600); err != nil {
				t.Fatal(err)
			}
			req := ExecuteRequest{Executor: "test"}
			tc.change(&req)
			result, err := s.Execute(req)
			var detail *diagnostic.Diagnostic
			if !errors.As(err, &detail) || detail.Code != "MUXLIO_INCOMPATIBLE_REQUIREMENTS" || detail.Expected == nil || detail.Actual == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			entries, err := os.ReadDir(s.workBase)
			if err != nil || len(entries) != 0 {
				t.Fatalf("incompatible execution created scratch: %v %v", entries, err)
			}
		})
	}
}

func TestScriptRejectsMalformedContractResult(t *testing.T) {
	for _, raw := range []string{`null`, `{} {}`, `{"usage":{"input":-1}}`, `{"executionDurationMs":-1}`} {
		t.Run(raw, func(t *testing.T) {
			s := testScript(t, "printf '%s' '"+raw+"'")
			result, err := s.Execute(ExecuteRequest{Executor: "test"})
			if err != nil || result.Error == "" || result.Usage != nil {
				t.Fatalf("malformed result accepted: %+v, %v", result, err)
			}
		})
	}
}

func TestMatchDispatchChecksContract(t *testing.T) {
	req := []byte(`{"executor":"test","contractVersion":"1","requiredCapabilities":{"progress":"1"}}`)
	caps := []state.WorkerCapability{{Name: "test"}}
	if MatchDispatch("test", req, caps, nil) {
		t.Fatal("binary name alone implied optional support")
	}
	caps[0].Contract = &adapterManifest{ManifestVersion: "1", ContractVersion: "1", Executor: "test", Capabilities: map[string]bool{"progress": true}}
	if !MatchDispatch("test", req, caps, nil) {
		t.Fatal("compatible worker rejected")
	}
	caps[0].Contract.CapabilityVersions = map[string]string{"progress": "2"}
	if MatchDispatch("test", req, caps, nil) {
		t.Fatal("incompatible capability revision assigned")
	}
	caps[0].Contract.Executor = "different"
	if MatchDispatch("test", req, caps, nil) {
		t.Fatal("wrong executor assigned")
	}
}

func TestLoadAdapterManifestMissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "agent-test")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := loadAdapterManifest(stub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil manifest")
	}
	if m.Supports(adapterCapabilityArtifacts) {
		t.Error("missing descriptor must not claim any capability")
	}
}

func TestLoadAdapterManifestParsesCapabilities(t *testing.T) {
	dir := t.TempDir()
	stub := writeManifest(t, dir, "agent-test", adapterManifest{
		ManifestVersion: "1",
		Executor:        "agent/test",
		Capabilities:    map[string]bool{adapterCapabilityArtifacts: true, adapterCapabilityProgress: true},
	})
	m, err := loadAdapterManifest(stub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !m.Supports(adapterCapabilityArtifacts) {
		t.Error("expected artifacts capability")
	}
	if !m.Supports(adapterCapabilityProgress) {
		t.Error("expected progress capability")
	}
	if m.Supports(adapterCapabilityCancel) {
		t.Error("cancel must not be supported when absent")
	}
}

func TestLoadAdapterManifestRejectsUnknownVersion(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "agent-test")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stub+".json", []byte(`{"manifestVersion":"99"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadAdapterManifest(stub)
	if err == nil {
		t.Fatal("expected error for unknown manifest version")
	}
	if !strings.Contains(err.Error(), "unsupported version") {
		t.Errorf("error = %v", err)
	}
}

func TestLoadAdapterManifestRejectsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "agent-test")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stub+".json", []byte(`{not valid}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadAdapterManifest(stub)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestCheckAdapterCapabilitiesScalarOutputsAlwaysAllowed(t *testing.T) {
	// Scalar outputs (no Path) are allowed even without the artifacts capability.
	empty := &adapterManifest{}
	req := ExecuteRequest{
		Executor: "agent/test",
		Outputs: map[string]OutputDecl{
			"result": {Mime: "application/json", Path: ""},
		},
	}
	if err := CheckAdapterCapabilities(req, empty); err != nil {
		t.Errorf("scalar output must not require artifacts capability: %v", err)
	}
}

func TestCheckAdapterCapabilitiesArtifactOutputRequiresCapability(t *testing.T) {
	// An adapter without the artifacts capability must be rejected for artifact ports.
	empty := &adapterManifest{}
	req := ExecuteRequest{
		Executor: "agent/test",
		Outputs: map[string]OutputDecl{
			"notes": {Mime: "text/markdown", Path: "notes.md"},
		},
	}
	err := CheckAdapterCapabilities(req, empty)
	if err == nil {
		t.Fatal("expected error for artifact output with no artifacts capability")
	}
	if !strings.Contains(err.Error(), adapterCapabilityArtifacts) {
		t.Errorf("error must mention capability name: %v", err)
	}
	if !strings.Contains(err.Error(), "notes") {
		t.Errorf("error must mention the port name: %v", err)
	}
}

func TestCheckAdapterCapabilitiesArtifactOutputAllowedWhenDeclared(t *testing.T) {
	m := &adapterManifest{Capabilities: map[string]bool{adapterCapabilityArtifacts: true}}
	req := ExecuteRequest{
		Executor: "agent/test",
		Outputs: map[string]OutputDecl{
			"report": {Mime: "text/markdown", Path: "report.md"},
		},
	}
	if err := CheckAdapterCapabilities(req, m); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckAdapterCapabilitiesNoOutputsAlwaysPasses(t *testing.T) {
	empty := &adapterManifest{}
	req := ExecuteRequest{Executor: "agent/test"}
	if err := CheckAdapterCapabilities(req, empty); err != nil {
		t.Errorf("no outputs must never fail: %v", err)
	}
}
