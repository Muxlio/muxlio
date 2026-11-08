package workers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestSharedWorkerEnvironmentAndExecutorPrecedence(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("AMBIENT_SECRET", "not-forwarded")
	shared, override := t.TempDir(), t.TempDir()
	for dir, value := range map[string]string{shared: "shared", override: "executor"} {
		script := "#!/bin/sh\nprintf '%s' '{\"outputs\":{\"source\":\"" + value + "\"}}'\n"
		if err := os.WriteFile(filepath.Join(dir, "fixture-tool"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	s := testScript(t, "fixture-tool\n")
	s.WorkerEnv = map[string]string{"PATH": shared, "SHARED_VALUE": "shared"}
	result, err := s.Execute(ExecuteRequest{Executor: "test"})
	if err != nil || result.Error != "" || result.Outputs["source"] != "shared" {
		t.Fatalf("shared PATH was not used with a minimal launcher PATH: %+v %v", result, err)
	}
	s.Executors = map[string]ExecutorConfig{"test": {Env: map[string]string{"PATH": override}}}
	result, err = s.Execute(ExecuteRequest{Executor: "test"})
	if err != nil || result.Error != "" || result.Outputs["source"] != "executor" {
		t.Fatalf("executor PATH did not override shared PATH: %+v %v", result, err)
	}
	for _, isolated := range []bool{false, true} {
		env, err := s.environment(t.TempDir(), isolated, "test")
		if err != nil {
			t.Fatal(err)
		}
		values := map[string]string{}
		for _, entry := range env {
			key, value, _ := strings.Cut(entry, "=")
			if _, duplicate := values[key]; duplicate {
				t.Fatalf("duplicate environment key %s", key)
			}
			values[key] = value
		}
		if _, leaked := values["AMBIENT_SECRET"]; leaked {
			t.Fatal("ambient credentials leaked")
		}
		if isolated {
			if values["PATH"] != "/usr/bin:/bin" || values["SHARED_VALUE"] != "" {
				t.Fatal("isolated execution inherited native environment settings")
			}
		} else if values["PATH"] != override || values["SHARED_VALUE"] != "shared" {
			t.Fatal("shared values or executor precedence lost")
		}
	}
}

func TestConfiguredEnvironmentRejectsManagedVariablesAndUnsafePaths(t *testing.T) {
	for _, env := range []map[string]string{
		{"HOME": "/override"}, {"TMPDIR": "/override"}, {"MUXLIO_LOG_FD": "3"},
		{"BAD=KEY": "value"}, {"VALUE": "bad\x00value"},
		{"PATH": ""}, {"PATH": "relative/bin"}, {"PATH": "/bin:"}, {"PATH": "$HOME/bin:/bin"},
	} {
		if err := ValidateWorkerEnv(env); err == nil {
			t.Fatalf("invalid shared environment accepted: %v", env)
		}
		if err := ValidateExecutorConfigs(map[string]ExecutorConfig{"custom/tool": {Env: env}}); err == nil {
			t.Fatalf("invalid executor environment accepted: %v", env)
		}
	}
}

func TestExecutorTimeoutOverridesGlobalButNotWorkflowLimit(t *testing.T) {
	s := testScript(t, "sleep 0.2\nprintf '%s' '{\"outputs\":{\"ok\":true}}'\n")
	s.Timeout = 50 * time.Millisecond
	s.Executors = map[string]ExecutorConfig{"test": {TimeoutSeconds: 2}}

	result, err := s.Execute(ExecuteRequest{Executor: "test"})
	if err != nil || result.Error != "" || result.Outputs["ok"] != true {
		t.Fatalf("capability timeout did not override global default: %+v, %v", result, err)
	}

	// An explicit workflow attempt timeout remains an upper bound.
	seconds := 0.05
	result, err = ExecuteAttempt(s, ExecuteRequest{Executor: "test", TimeoutSeconds: &seconds})
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("workflow timeout was not enforced: %+v, %v", result, err)
	}
}

func TestExecutorTimeoutConfigurationBounds(t *testing.T) {
	for _, seconds := range []int{-1, 86401} {
		if err := ValidateExecutorConfigs(map[string]ExecutorConfig{"agent/kiro": {TimeoutSeconds: seconds}}); err == nil {
			t.Errorf("accepted timeoutSeconds=%d", seconds)
		}
	}
	for _, seconds := range []int{0, 1, 86400} {
		if err := ValidateExecutorConfigs(map[string]ExecutorConfig{"agent/kiro": {TimeoutSeconds: seconds}}); err != nil {
			t.Errorf("rejected timeoutSeconds=%d: %v", seconds, err)
		}
	}
}
