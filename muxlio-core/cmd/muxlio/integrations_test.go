package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-core/internal/integrations"
)

func TestIntegrationsCLIProfilesAndLifecycle(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home, ws, root := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	config := []byte(`{"mcpProfiles":{"remote":{"url":"https://example.test/mcp","bearerEnv":"EXAMPLE_MCP_TOKEN"}}}`)
	if err := os.WriteFile(filepath.Join(root, "config.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	for _, client := range integrations.Clients {
		for _, scope := range []string{"user", "project"} {
			t.Run(client+scope, func(t *testing.T) {
				args := []string{"--client", client, "--scope", scope, "--workspace", ws, "--data-dir", root, "--transport", "http", "--profile", "remote", "--json"}
				for _, action := range []string{"add", "add", "list", "doctor", "remove", "remove"} {
					command := append([]string{action}, args...)
					if action == "doctor" {
						command = append(command, "--config-only")
					}
					if code := cmdIntegrations(command, "", false); code != 0 {
						t.Fatalf("%s: %d", action, code)
					}
				}
			})
		}
	}
	if code := cmdIntegrations([]string{"add", "--client", "cursor", "--workspace", ws, "--transport", "http", "--url", "https://example.test/mcp"}, root, false); code == 0 {
		t.Fatal("scope must be explicit")
	}
	if code := cmdIntegrations([]string{"add", "--scope", "project", "--client", "cursor", "--profile", "remote", "--server", "other"}, root, false); code == 0 {
		t.Fatal("ambiguous target accepted")
	}
	if code := cmdIntegrations([]string{"doctor", "--scope", "project", "--client", "cursor", "--workspace", ws, "--config-only"}, root, false); code == 0 {
		t.Fatal("missing registration accepted")
	}
}

func TestInitClientAndAgentAliasRegisterAuthoringClient(t *testing.T) {
	for _, flag := range []string{"--client", "--agent"} {
		t.Run(flag, func(t *testing.T) {
			root, ws := t.TempDir(), t.TempDir()
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			args := []string{flag, "cursor", "--workspace", ws, "--data-dir", root, "--mcp-binary", binary, "--scope", "project", "--json"}
			if code := cmdInit(args, "", true); code != 0 {
				t.Fatalf("init: %d", code)
			}
			path := filepath.Join(ws, ".cursor", "mcp.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Servers map[string]struct {
					Command string
					Env     map[string]string
				} `json:"mcpServers"`
			}
			if err := json.Unmarshal(raw, &config); err != nil {
				t.Fatal(err)
			}
			if config.Servers["muxlio"].Command != binary || config.Servers["muxlio"].Env["MUXLIO_CONFIG"] != filepath.Join(root, "config.json") {
				t.Fatalf("wrong registration: %s", raw)
			}
			if code := cmdInit(args, "", true); code != 0 {
				t.Fatalf("repeat init: %d", code)
			}
			next, _ := os.ReadFile(path)
			if string(raw) != string(next) {
				t.Fatal("init changed existing registration")
			}
			cfgRaw, _ := os.ReadFile(filepath.Join(root, "config.json"))
			var runtime map[string]any
			if err := json.Unmarshal(cfgRaw, &runtime); err != nil {
				t.Fatal(err)
			}
			if runtime["defaultServer"] != "http://127.0.0.1:7600" {
				t.Fatal("stdio cannot resolve local credentials through saved default")
			}
		})
	}
}

func TestInitUserScopeDoesNotWriteProjectInstructionsOrConfig(t *testing.T) {
	home, workspace, root := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if code := cmdInit([]string{"--client", "cursor", "--scope", "user", "--workspace", workspace, "--data-dir", root, "--mcp-binary", binary, "--json"}, "", true); code != 0 {
		t.Fatalf("init: %d", code)
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("user setup modified project: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor", "mcp.json")); err != nil {
		t.Fatal(err)
	}
}

func TestInitStdioDefaultUsesCoordinatorTLS(t *testing.T) {
	root, workspace := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, []byte(`{"addr":"127.0.0.1:7600","tlsCert":"server.crt","tlsKey":"server.key"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if code := cmdInit([]string{"--workspace", workspace, "--data-dir", root, "--json"}, "", true); code != 0 {
		t.Fatalf("init: %d", code)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct{ DefaultServer string }
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if config.DefaultServer != "https://127.0.0.1:7600" {
		t.Fatalf("stdio points at the wrong coordinator transport: %s", config.DefaultServer)
	}
}
