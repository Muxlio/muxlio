package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExecutorSettingsComeFromConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MUXLIO_CONFIG", "")
	t.Setenv("EXAMPLE_CREDENTIAL", "ambient")
	settings, err := localWorkerSettings(dir)
	if err != nil || len(settings.Executors) != 0 {
		t.Fatalf("default settings: %v, %v", settings, err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"workerEnv":{"PATH":"/configured/bin","SHARED_VALUE":"shared"},"executors":{"custom/tool":{"allow":true,"timeoutSeconds":3600,"env":{"EXAMPLE_CREDENTIAL":"configured"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err = localWorkerSettings(dir)
	if err != nil || !settings.Executors["custom/tool"].ExplicitlyAllowed() || settings.Executors["custom/tool"].TimeoutSeconds != 3600 || settings.Executors["custom/tool"].Env["EXAMPLE_CREDENTIAL"] != "configured" || settings.WorkerEnv["PATH"] != "/configured/bin" {
		t.Fatalf("standalone settings: %v, %v", settings, err)
	}
	config, err := loadServiceConfig(path)
	if err != nil || !config.Executors["custom/tool"].ExplicitlyAllowed() || config.Executors["custom/tool"].TimeoutSeconds != 3600 || config.Executors["custom/tool"].Env["EXAMPLE_CREDENTIAL"] != "configured" || config.WorkerEnv["PATH"] != "/configured/bin" {
		t.Fatalf("service settings: %v, %v", config.Executors, err)
	}
	t.Setenv("MUXLIO_CONFIG", path)
	settings, err = localWorkerSettings(t.TempDir())
	if err != nil || settings.Executors["custom/tool"].TimeoutSeconds != 3600 || settings.Executors["custom/tool"].Env["EXAMPLE_CREDENTIAL"] != "configured" || settings.WorkerEnv["PATH"] != "/configured/bin" {
		t.Fatalf("custom config: %v, %v", settings, err)
	}
	if err := os.WriteFile(path, []byte(`{"executors":{"custom/tool":{"env":{"BAD=KEY":"value"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadServiceConfig(path); err == nil {
		t.Fatal("service accepted invalid executor environment")
	}
	if _, err := localWorkerSettings(dir); err == nil {
		t.Fatal("standalone run accepted invalid executor environment")
	}
}

func TestWorkerSettingsRejectInvalidSharedEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	t.Setenv("MUXLIO_CONFIG", path)
	for _, body := range []string{`{"workerEnv":{"HOME":"/override"}}`, `{"workerEnv":{"PATH":"/bin:"}}`, `{"workerEnv":{"MUXLIO_OUTPUT_DIR":"/override"}}`} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadServiceConfig(path); err == nil {
			t.Fatal("service accepted invalid workerEnv")
		}
		if _, err := localWorkerSettings(dir); err == nil {
			t.Fatal("standalone/remote worker accepted invalid workerEnv")
		}
	}
}
