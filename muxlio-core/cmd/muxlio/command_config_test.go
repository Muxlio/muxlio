package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalCommandHostEnabledConfigAndEnvironmentOverride(t *testing.T) {
	t.Setenv("MUXLIO_CONFIG", "")
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

func TestSSHAgentSocketComesFromConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MUXLIO_CONFIG", "")
	t.Setenv("SSH_AUTH_SOCK", "/ambient/agent.sock")
	if got := localCommandSettings(dir).SSHAgentSocket; got != "" {
		t.Fatalf("unconfigured socket = %q", got)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"commandHostEnabled":true,"sshAgentSocket":"/configured/agent.sock"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := localCommandSettings(dir).SSHAgentSocket; got != "/configured/agent.sock" {
		t.Fatalf("standalone socket = %q", got)
	}
	config, err := loadServiceConfig(path)
	if err != nil || config.SSHAgentSocket != "/configured/agent.sock" {
		t.Fatalf("service socket = %q, error = %v", config.SSHAgentSocket, err)
	}
	t.Setenv("MUXLIO_CONFIG", path)
	if got := localCommandSettings(t.TempDir()).SSHAgentSocket; got != "/configured/agent.sock" {
		t.Fatalf("custom config socket = %q", got)
	}
	if err := os.WriteFile(path, []byte(`{"sshAgentSocket":"relative.sock"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadServiceConfig(path); err == nil {
		t.Fatal("service accepted a relative SSH agent socket path")
	}
}
