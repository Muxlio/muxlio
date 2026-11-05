package workers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIsolationPolicyRequiresExplicitMode(t *testing.T) {
	s := NewScriptExecutor("/adapters", "/tmp/work")
	cmd, isolated, err := s.command(ExecuteRequest{}, "/adapters/test", "/tmp/work")
	if err != nil || cmd == nil || isolated {
		t.Fatalf("default execution should remain native: cmd=%v isolated=%v err=%v", cmd, isolated, err)
	}
}

func TestOCIEngineUsesCompatibleUnprivilegedUserOption(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("OCI isolation is Linux-only")
	}
	for _, tc := range []struct {
		engine string
		want   string
	}{
		{engine: "podman", want: "--userns=keep-id"},
		{engine: "docker", want: "--user"},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			dir := t.TempDir()
			enginePath := filepath.Join(dir, tc.engine)
			if err := os.WriteFile(enginePath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			s := NewScriptExecutor("/adapters", "/tmp/work")
			req := ExecuteRequest{Params: json.RawMessage(`{"isolation":{"mode":"required","runtime":"oci","image":"alpine"}}`)}
			cmd, isolated, err := s.command(req, "/adapters/test", "/tmp/work")
			if err != nil || !isolated || cmd == nil {
				t.Fatalf("OCI command should be isolated: cmd=%v isolated=%v err=%v", cmd, isolated, err)
			}
			got := strings.Join(cmd.Args, " ")
			if !strings.Contains(got, " -i ") || !strings.Contains(got, "/tmp/work:/tmp/work:rw") {
				t.Fatalf("OCI must receive stdin and preserve request scratch paths: %s", got)
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("%s command missing %q: %s", tc.engine, tc.want, got)
			}
			if tc.engine == "docker" && strings.Contains(got, "--userns=keep-id") {
				t.Fatalf("Docker command contains Podman-only option: %s", got)
			}
		})
	}
}

func TestTrustedNativeIsExplicitlyNative(t *testing.T) {
	s := NewScriptExecutor("/adapters", "/tmp/work")
	req := ExecuteRequest{Params: json.RawMessage(`{"isolation":{"mode":"trusted-native"}}`)}
	cmd, isolated, err := s.command(req, "/adapters/test", "/tmp/work")
	if err != nil || cmd == nil || isolated {
		t.Fatalf("trusted-native should run natively: cmd=%v isolated=%v err=%v", cmd, isolated, err)
	}
}

func TestRequiredIsolationDoesNotFallBack(t *testing.T) {
	s := NewScriptExecutor("/adapters", "/tmp/work")
	req := ExecuteRequest{Params: json.RawMessage(`{"isolation":{"mode":"required","runtime":"unknown"}}`)}
	cmd, isolated, err := s.command(req, "/adapters/test", "/tmp/work")
	if cmd != nil || isolated || err == nil {
		t.Fatalf("required isolation must fail closed: cmd=%v isolated=%v err=%v", cmd, isolated, err)
	}
	if runtime.GOOS != "linux" {
		req.Params = json.RawMessage(`{"isolation":{"mode":"required","runtime":"bwrap"}}`)
		_, _, err = s.command(req, "/adapters/test", "/tmp/work")
		if err == nil {
			t.Fatal("required isolation must fail off Linux")
		}
	}
}

func TestMacOSVMDriverIsExplicitAndFailsClosed(t *testing.T) {
	s := NewScriptExecutor("/adapters", "/tmp/work")
	req := ExecuteRequest{Params: json.RawMessage(`{"isolation":{"mode":"required","runtime":"macos-vm"}}`)}
	if _, _, err := s.command(req, "/adapters/test", "/tmp/work"); err == nil || !strings.Contains(err.Error(), "enabled=true") {
		t.Fatalf("macOS VM must require explicit opt-in: %v", err)
	}
	req.Params = json.RawMessage(`{"isolation":{"mode":"required","runtime":"macos-vm","enabled":true}}`)
	_, isolated, err := s.command(req, "/adapters/test", "/tmp/work")
	if err == nil || isolated || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("unimplemented VM driver must fail closed without claiming isolation: isolated=%v err=%v", isolated, err)
	}
}

func TestMacOSVMCompatibilityCheck(t *testing.T) {
	if err := (macOSVMDriver{goos: "darwin"}).Compatibility(); err != nil {
		t.Fatalf("darwin should pass the platform compatibility check: %v", err)
	}
	if err := (macOSVMDriver{goos: "linux"}).Compatibility(); err == nil {
		t.Fatal("non-macOS platform should fail the compatibility check")
	}
}

func TestGuardRequestRequiresCapabilityAndSecretAllowlist(t *testing.T) {
	req := ExecuteRequest{Executor: "agent/test", Secrets: map[string]string{"token": "sensitive"}, Policy: &ExecutionPolicy{Required: true, AllowedCapabilities: []string{"agent/test"}, AllowedSecrets: []string{"token"}}}
	got, err := GuardRequest(req)
	if err != nil || got.Secrets["token"] != "sensitive" || len(got.Secrets) != 1 {
		t.Fatalf("unexpected guarded request: %#v %v", got, err)
	}
	req.Policy.AllowedCapabilities = nil
	if _, err := GuardRequest(req); err == nil {
		t.Fatal("required policy must reject unapproved capability")
	}
	req.Policy.AllowedCapabilities = []string{"agent/test"}
	req.Policy.Required = false
	req.Secrets["other"] = "drop"
	req.Policy.AllowedSecrets = nil
	got, err = GuardRequest(req)
	if err != nil || len(got.Secrets) != 0 {
		t.Fatalf("optional policy should drop unapproved secrets: %#v %v", got, err)
	}
}

func TestRedactSecrets(t *testing.T) {
	if got := RedactSecrets("token=sensitive", map[string]string{"token": "sensitive"}); got != "token=[REDACTED]" {
		t.Fatalf("secret was not redacted: %q", got)
	}
}
