package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestInitAgentGeminiInstallsWorkspaceSkill(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	// Init loads skill content from the runtime tree when the repo checkout is
	// not discoverable (see getSkillContent).
	skillsDir := filepath.Join(root, "skills")
	if err := os.MkdirAll(skillsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "SKILL.md"), []byte("# muxlio test skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := cmdInit([]string{"--data-dir", root, "--workspace", ws, "--agent", "gemini", "--json"}, "", true); code != 0 {
		t.Fatalf("cmdInit returned %d", code)
	}
	skillPath := filepath.Join(ws, ".gemini", "skills", "muxlio", "SKILL.md")
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read gemini skill: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("gemini workspace skill is empty")
	}
}

func TestInitDefaultCapabilitiesIncludeClaude(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	if code := cmdInit([]string{"--data-dir", root, "--workspace", ws, "--json"}, "", true); code != 0 {
		t.Fatalf("cmdInit returned %d", code)
	}
	raw, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, want := range BundledExecutorCapabilities {
		if !slices.Contains(cfg.Capabilities, want) {
			t.Fatalf("config capabilities %v missing %q", cfg.Capabilities, want)
		}
	}
}

func TestInitRegistersMCPAndPreservesCredentials(t *testing.T) {
	root, ws := t.TempDir(), t.TempDir()
	args := []string{"--data-dir", root, "--workspace", ws, "--json"}
	if code := cmdInit(args, "", true); code != 0 {
		t.Fatal(code)
	}
	path := filepath.Join(root, "config.json")
	read := func() map[string]json.RawMessage {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]json.RawMessage
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	cfg := read()
	var listener serviceMCPConfig
	if err := json.Unmarshal(cfg["mcp"], &listener); err != nil || listener.Listen != "127.0.0.1:7602" {
		t.Fatalf("MCP not registered: %s", cfg["mcp"])
	}
	token, worker := readConfiguredAuthToken(path), readConfiguredWorkerToken(path)
	if token == "" || worker == "" || token == worker {
		t.Fatal("missing independent persistent credentials")
	}
	cfg["mcp"] = json.RawMessage(`{"listen":"127.0.0.1:9902","allowOrigins":["http://localhost:6274"]}`)
	cfg["custom"] = json.RawMessage(`{"keep":true}`)
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if code := cmdInit(args, "", true); code != 0 {
		t.Fatal(code)
	}
	next := read()
	if string(next["mcp"]) != string(cfg["mcp"]) || string(next["custom"]) != string(cfg["custom"]) {
		t.Fatal("init replaced existing settings")
	}
	if readConfiguredAuthToken(path) != token || readConfiguredWorkerToken(path) != worker {
		t.Fatal("init rotated credentials")
	}
	if code := cmdInit(append(args, "--mcp-listen", "0.0.0.0:7602"), "", true); code != 0 {
		t.Fatal(code)
	}
	next = read()
	if err := json.Unmarshal(next["mcp"], &listener); err != nil || listener.Listen != "0.0.0.0:7602" || len(listener.AllowOrigins) != 1 {
		t.Fatalf("explicit address update lost settings: %s", next["mcp"])
	}
	cfg = next
	cfg["mcp"] = json.RawMessage(`null`)
	raw, _ = json.Marshal(cfg)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if code := cmdInit(args, "", true); code != 0 {
		t.Fatal(code)
	}
	if string(read()["mcp"]) != "null" {
		t.Fatal("init enabled an explicitly disabled listener")
	}
}

func TestInitPersistsWorkerPathAndPreservesOverrides(t *testing.T) {
	root, ws, initialPath := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("PATH", initialPath)
	args := []string{"--data-dir", root, "--workspace", ws, "--json"}
	if code := cmdInit(args, "", true); code != 0 {
		t.Fatal(code)
	}
	path := filepath.Join(root, "config.json")
	read := func() serviceConfig {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var cfg serviceConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	if got := read().WorkerEnv["PATH"]; got != initialPath {
		t.Fatalf("initial PATH not captured: %q", got)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	if code := cmdInit(args, "", true); code != 0 {
		t.Fatal(code)
	}
	if got := read().WorkerEnv["PATH"]; got != initialPath {
		t.Fatalf("init replaced saved PATH with SSH PATH: %q", got)
	}
	updatedPath := t.TempDir()
	if code := cmdInit(append(args, "--worker-path", updatedPath), "", true); code != 0 {
		t.Fatal(code)
	}
	if got := read().WorkerEnv["PATH"]; got != updatedPath {
		t.Fatalf("explicit PATH not saved: %q", got)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if code := cmdInit(append(args, "--worker-path", "relative:/bin"), "", true); code == 0 {
		t.Fatal("invalid PATH accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("invalid PATH changed existing config")
	}
}
