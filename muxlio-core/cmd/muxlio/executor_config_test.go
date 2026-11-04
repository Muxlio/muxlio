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
	settings, err := localExecutorSettings(dir)
	if err != nil || len(settings) != 0 {
		t.Fatalf("default settings: %v, %v", settings, err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"executors":{"custom/tool":{"allow":true,"env":{"EXAMPLE_CREDENTIAL":"configured"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err = localExecutorSettings(dir)
	if err != nil || !settings["custom/tool"].ExplicitlyAllowed() || settings["custom/tool"].Env["EXAMPLE_CREDENTIAL"] != "configured" {
		t.Fatalf("standalone settings: %v, %v", settings, err)
	}
	config, err := loadServiceConfig(path)
	if err != nil || !config.Executors["custom/tool"].ExplicitlyAllowed() || config.Executors["custom/tool"].Env["EXAMPLE_CREDENTIAL"] != "configured" {
		t.Fatalf("service settings: %v, %v", config.Executors, err)
	}
	t.Setenv("MUXLIO_CONFIG", path)
	settings, err = localExecutorSettings(t.TempDir())
	if err != nil || settings["custom/tool"].Env["EXAMPLE_CREDENTIAL"] != "configured" {
		t.Fatalf("custom config: %v, %v", settings, err)
	}
	if err := os.WriteFile(path, []byte(`{"executors":{"custom/tool":{"env":{"BAD=KEY":"value"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadServiceConfig(path); err == nil {
		t.Fatal("service accepted invalid executor environment")
	}
	if _, err := localExecutorSettings(dir); err == nil {
		t.Fatal("standalone run accepted invalid executor environment")
	}
}
