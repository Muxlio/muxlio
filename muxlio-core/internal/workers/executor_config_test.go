package workers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutorEnvironmentIsConfiguredAndScoped(t *testing.T) {
	t.Setenv("EXAMPLE_CREDENTIAL", "ambient-value")
	s := NewScriptExecutor(t.TempDir(), t.TempDir())
	s.Executors = map[string]ExecutorConfig{"custom/tool": {Env: map[string]string{"EXAMPLE_CREDENTIAL": "configured-value"}}}
	for _, tc := range []struct {
		name           string
		isolated, want bool
	}{
		{"custom/tool", false, true}, {"custom/tool", true, false}, {"another/tool", false, false},
	} {
		env, err := s.environment(t.TempDir(), tc.isolated, tc.name)
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(env, "\n")
		if strings.Contains(joined, "EXAMPLE_CREDENTIAL=configured-value") != tc.want {
			t.Fatalf("executor %s isolated=%v has incorrect configured environment", tc.name, tc.isolated)
		}
		if strings.Contains(joined, "ambient-value") {
			t.Fatal("ambient credentials leaked")
		}
	}
}

func TestExecutorDescriptorAndLocalHostPermission(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "custom-tool.json"), []byte(`{"requiresHostExecution":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "custom-tool"), []byte("#!/bin/sh\nprintf '%s' '{\"outputs\":{\"ok\":true}}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewScriptExecutor(dir, t.TempDir())
	r, err := s.Execute(ExecuteRequest{Executor: "custom/tool", Params: json.RawMessage(`{"hostExecutionAllowed":true}`)})
	if err != nil || !strings.Contains(r.Error, "denied by worker policy") {
		t.Fatalf("default permission: %+v, %v", r, err)
	}
	allow := true
	s.Executors = map[string]ExecutorConfig{"custom/tool": {Allow: &allow}}
	// Adapter-owned parameters are opaque to the worker.
	r, err = s.Execute(ExecuteRequest{Executor: "custom/tool", Params: json.RawMessage(`{"execution":{"isolation":"adapter-specific"}}`)})
	if err != nil || r.Error != "" || r.Outputs["ok"] != true {
		t.Fatalf("authorized execution: %+v, %v", r, err)
	}
}

func TestExplicitDenyOverridesExecutorDefaults(t *testing.T) {
	s := NewScriptExecutor(t.TempDir(), t.TempDir())
	if allowed, err := s.CanExecute("custom/tool"); err != nil || !allowed {
		t.Fatalf("default policy: allowed=%v error=%v", allowed, err)
	}
	deny := false
	s.Executors = map[string]ExecutorConfig{"custom/tool": {Allow: &deny}}
	if allowed, err := s.CanExecute("custom/tool"); err != nil || allowed {
		t.Fatalf("explicit deny: allowed=%v error=%v", allowed, err)
	}
}
