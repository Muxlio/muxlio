package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestInitCommandAuthorizationPreservesExecutorConfiguration(t *testing.T) {
	root, ws := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "config.json")
	initial := map[string]any{"workspace": ws, "executors": map[string]any{
		"command":      map[string]any{"allow": false, "env": map[string]string{"EXAMPLE": "value"}, "timeoutSeconds": 120},
		"generic/task": map[string]any{"allow": false},
	}}
	raw, _ := json.Marshal(initial)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--data-dir", root, "--json"}
	read := func() map[string]map[string]any {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Executors map[string]map[string]any `json:"executors"`
		}
		if err := json.Unmarshal(raw, &config); err != nil {
			t.Fatal(err)
		}
		return config.Executors
	}
	if code := cmdInit(args, "", true); code != 0 {
		t.Fatalf("init: %d", code)
	}
	if read()["command"]["allow"] != false {
		t.Fatal("init without authorization changed command permission")
	}
	if code := cmdInit(append(args, "--allow-command"), "", true); code != 0 {
		t.Fatalf("init with command authorization: %d", code)
	}
	executors := read()
	if executors["command"]["allow"] != true || executors["generic/task"]["allow"] != false {
		t.Fatalf("incorrect permissions: %v", executors)
	}
	if executors["command"]["timeoutSeconds"] != float64(120) || executors["command"]["env"].(map[string]any)["EXAMPLE"] != "value" {
		t.Fatalf("command configuration lost: %v", executors["command"])
	}
}

func TestInitAgentAntigravityInstallsWorkspaceSkill(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	skillsDir := filepath.Join(root, "skills")
	if err := os.MkdirAll(skillsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "SKILL.md"), []byte("---\nname: muxlio\ndescription: Muxlio test skill\n---\n# Muxlio\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := cmdInit([]string{"--data-dir", root, "--workspace", ws, "--agent", "antigravity", "--json"}, "", true); code != 0 {
		t.Fatalf("cmdInit returned %d", code)
	}
	skillPath := filepath.Join(ws, ".agents", "skills", "muxlio", "SKILL.md")
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read antigravity skill: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("antigravity workspace skill is empty")
	}
}

func TestInitRejectsUnknownAgent(t *testing.T) {
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
	if code := cmdInit([]string{"--data-dir", root, "--workspace", ws, "--agent", "unknown", "--json"}, "", true); code == 0 {
		t.Fatal("expected unknown agent to be rejected")
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

func TestInitDefaultsAllowBundledExecutorsAndPreserveUserPolicies(t *testing.T) {
	root, ws := t.TempDir(), t.TempDir()
	args := []string{"--data-dir", root, "--workspace", ws, "--json"}
	if code := cmdInit(args, "", true); code != 0 {
		t.Fatal(code)
	}
	path := filepath.Join(root, "config.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg serviceConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, capability := range BundledExecutorCapabilities {
		c := cfg.Executors[capability]
		if !c.ExplicitlyAllowed() || c.Authorization == nil {
			t.Fatalf("%s not authorized: %+v", capability, c)
		}
		p := c.Authorization
		if p.Mode != "trusted" || !p.WorkspaceWrite || p.Network != "allowed" || p.Isolation != "native" || !slices.Equal(p.AllowedTools, []string{"*"}) || !slices.Equal(p.AllowedCommands, []string{"*"}) {
			t.Fatalf("%s defaults: %+v", capability, p)
		}
		if err := p.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	// Apply operator restrictions to one capability, an explicit absent policy
	// to another, and retain custom executors and fields verbatim.
	var config map[string]json.RawMessage
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	executors := map[string]json.RawMessage{
		BundledExecutorCapabilities[0]: json.RawMessage(`{"allow":false,"authorization":{"mode":"restricted","network":"none","isolation":"required"},"env":{"EXAMPLE":"value"},"timeoutSeconds":120,"custom":true}`),
		BundledExecutorCapabilities[1]: json.RawMessage(`{"allow":false,"authorization":null}`),
		BundledExecutorCapabilities[2]: json.RawMessage(`null`),
		"custom/tool":                  json.RawMessage(`{"allow":false,"custom":"keep"}`),
	}
	config["executors"], _ = json.Marshal(executors)
	before, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	if code := cmdInit(args, "", true); code != 0 {
		t.Fatal(code)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &config); err != nil {
		t.Fatal(err)
	}
	var next map[string]json.RawMessage
	if err := json.Unmarshal(config["executors"], &next); err != nil {
		t.Fatal(err)
	}
	for key, want := range executors {
		var gotValue, wantValue any
		_ = json.Unmarshal(next[key], &gotValue)
		_ = json.Unmarshal(want, &wantValue)
		if !reflect.DeepEqual(gotValue, wantValue) {
			t.Fatalf("init changed explicit %s policy: %s != %s", key, next[key], want)
		}
	}
	if code := cmdInit(args, "", true); code != 0 {
		t.Fatal(code)
	}
	repeated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(repeated) {
		t.Fatal("repeated init changed configuration")
	}
}

func TestInitUpgradesMissingPoliciesWithoutReplacingExecutorOverrides(t *testing.T) {
	config := map[string]json.RawMessage{"executors": json.RawMessage(`{"agent/kiro":{"timeoutSeconds":1800},"command":{"allow":true}}`)}
	changed, err := initializeExecutorPolicies(config)
	if err != nil || !changed {
		t.Fatalf("initialize: changed=%v %v", changed, err)
	}
	var cfg map[string]workers.ExecutorConfig
	if err := json.Unmarshal(config["executors"], &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["agent/kiro"].TimeoutSeconds != 1800 || cfg["agent/kiro"].Authorization == nil {
		t.Fatalf("override lost or policy missing: %+v", cfg["agent/kiro"])
	}
}
