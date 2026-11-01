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
