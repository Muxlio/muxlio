package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalCommandHostEnabledConfigAndEnvironmentOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"commandHostEnabled":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MUXLIO_COMMAND_HOST_ENABLED", "")
	if !localCommandHostEnabled(dir) {
		t.Fatal("empty environment should leave config setting in effect")
	}
	t.Setenv("MUXLIO_COMMAND_HOST_ENABLED", "true")
	if !localCommandHostEnabled(dir) {
		t.Fatal("environment should enable host command")
	}
	t.Setenv("MUXLIO_COMMAND_HOST_ENABLED", "false")
	if localCommandHostEnabled(dir) {
		t.Fatal("environment false should override enabled config")
	}
	t.Setenv("MUXLIO_COMMAND_HOST_ENABLED", "")
	if !localCommandHostEnabled(dir) {
		t.Fatal("config should enable local host command")
	}
}
