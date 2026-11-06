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

func TestRequiredIsolationNeverLaunchesAdapterWithoutBackend(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // None of the supported launchers is installed.
	marker := filepath.Join(t.TempDir(), "launched")
	s := testScript(t, "touch '"+marker+"'; printf '{}' ")
	for _, params := range []string{
		`{"isolation":{"mode":"required"}}`,
		`{"isolation":{"mode":"required","runtime":"native-trusted"}}`,
		`{"isolation":{"mode":"required","runtime":"unavailable"}}`,
		`{"isolation":{"mode":"required","runtime":"bwrap"}}`,
		`{"isolation":{"mode":"required","runtime":"oci","image":"approved-image"}}`,
		`{"isolation":{"mode":"required","runtime":"docker-vm","image":"approved-image","enabled":true}}`,
		`{"isolation":{}}`,
	} {
		result, err := s.Execute(ExecuteRequest{Executor: "test", Params: json.RawMessage(params)})
		if err != nil || result.Error == "" {
			t.Fatalf("required attempt did not fail closed: %+v %v", result, err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("adapter was launched: %v", err)
	}
}

func TestOptionalVMRequiresOptInAndImage(t *testing.T) {
	s := NewScriptExecutor("/adapters", "/tmp/work")
	req := ExecuteRequest{Params: json.RawMessage(`{"isolation":{"mode":"required","runtime":"macos-vm"}}`)}
	if _, _, err := s.command(req, "/adapters/test", "/tmp/work"); err == nil || !strings.Contains(err.Error(), "enabled=true") {
		t.Fatalf("macOS VM must require explicit opt-in: %v", err)
	}
	req.Params = json.RawMessage(`{"isolation":{"mode":"required","runtime":"macos-vm","enabled":true}}`)
	_, isolated, err := s.command(req, "/adapters/test", "/tmp/work")
	if err == nil || isolated {
		t.Fatalf("VM driver without image must fail closed: isolated=%v err=%v", isolated, err)
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

// A pinned manifest must not cause a native readiness probe before isolation.
func TestRequiredIsolationNeverProbesNatively(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	marker := filepath.Join(t.TempDir(), "probed")
	s := testScript(t, "echo launched > '"+marker+"'; printf '{}' ")
	descriptor := environmentDescriptor()
	raw, _ := json.Marshal(descriptor)
	if err := os.WriteFile(s.scriptPath("test")+".json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"unavailable", "bwrap", "oci", "docker-vm"} {
		params, _ := json.Marshal(map[string]any{"isolation": map[string]any{"mode": "required", "runtime": backend, "image": "approved-image", "enabled": true}})
		result, err := s.Execute(ExecuteRequest{Executor: "test", Environment: descriptor.Environment, Params: params})
		if err != nil || result.Error == "" {
			t.Fatalf("backend %s failed open: %+v %v", backend, result, err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("native probe launched: %v", err)
	}
}
