package integrations

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func fixture(t *testing.T, client, scope, transport string) Options {
	t.Helper()
	t.Setenv("CODEX_HOME", "")
	dir := t.TempDir()
	bin := filepath.Join(dir, "muxlio-mcp with spaces")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return Options{Client: client, Scope: scope, Home: dir, Workspace: dir, Transport: transport, Binary: bin, Config: filepath.Join(dir, "config.json"), URL: "https://example.test/mcp", BearerEnv: "MUXLIO_TEST_TOKEN"}
}

func writeFixture(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestClientScopeTransportLifecycle(t *testing.T) {
	for _, client := range Clients {
		for _, scope := range []string{"user", "project"} {
			for _, transport := range []string{"stdio", "http"} {
				t.Run(client+"/"+scope+"/"+transport, func(t *testing.T) {
					o := fixture(t, client, scope, transport)
					path, err := o.Path()
					if err != nil {
						t.Fatal(err)
					}
					initial := []byte(`{"custom":{"nested":[1,"keep"]},"mcpServers":{"other":{"command":"custom"}}}`)
					if client == "codex" {
						initial = []byte("model = 'keep'\n[custom]\nnested = [1, 'keep']\n[mcp_servers.other]\ncommand = 'custom'\n")
					}
					writeFixture(t, path, initial)
					before, err := read(path, client == "codex")
					if err != nil {
						t.Fatal(err)
					}
					first, err := Apply(o, "add")
					if err != nil || !first.Changed || !first.Installed {
						t.Fatalf("add: %+v %v", first, err)
					}
					raw, _ := os.ReadFile(path)
					second, err := Apply(o, "add")
					if err != nil || second.Changed || !second.Managed {
						t.Fatalf("repeat: %+v %v", second, err)
					}
					next, _ := os.ReadFile(path)
					if !bytes.Equal(raw, next) {
						t.Fatal("second add rewrote config")
					}
					after, err := read(path, client == "codex")
					if err != nil {
						t.Fatal(err)
					}
					if digest(before.root["custom"]) != digest(after.root["custom"]) || digest(before.servers["other"]) != digest(after.servers["other"]) || len(after.servers) != 2 {
						t.Fatal("lost unrelated settings")
					}
					entry := after.servers["muxlio"].(map[string]any)
					if transport == "stdio" {
						if entry["command"] != o.Binary {
							t.Fatal("absolute executable was not preserved")
						}
						if digest(entry["env"]) != digest(map[string]any{"MUXLIO_CONFIG": o.Config}) {
							t.Fatal("wrong runtime config")
						}
					} else {
						if entry["url"] != o.URL {
							t.Fatal("wrong endpoint")
						}
						switch client {
						case "codex":
							if entry["bearer_token_env_var"] != o.BearerEnv {
								t.Fatal("wrong Codex auth format")
							}
						case "cursor":
							if entry["headers"].(map[string]any)["Authorization"] != "Bearer ${env:MUXLIO_TEST_TOKEN}" {
								t.Fatal("wrong Cursor interpolation")
							}
						default:
							if entry["headers"].(map[string]any)["Authorization"] != "Bearer ${MUXLIO_TEST_TOKEN}" {
								t.Fatal("wrong client interpolation")
							}
						}
					}
					if _, err := Apply(o, "doctor"); err != nil {
						t.Fatal(err)
					}
					o.URL = "https://replacement.test/mcp"
					o.Server = "new-profile"
					if _, err := Apply(o, "add"); err == nil {
						t.Fatal("add replaced an incompatible entry")
					}
					if repaired, err := Apply(o, "repair"); err != nil || !repaired.Changed {
						t.Fatalf("repair: %+v %v", repaired, err)
					}
					if removed, err := Apply(o, "remove"); err != nil || !removed.Changed || removed.Installed {
						t.Fatalf("remove: %+v %v", removed, err)
					}
					if removed, err := Apply(o, "remove"); err != nil || removed.Changed {
						t.Fatalf("repeat remove: %+v %v", removed, err)
					}
					after, _ = read(path, client == "codex")
					if digest(before.root) != digest(after.root) {
						t.Fatal("remove changed unrelated settings")
					}
				})
			}
		}
	}
}

func TestFreshInstallAndMissingRepair(t *testing.T) {
	for _, client := range Clients {
		for _, scope := range []string{"user", "project"} {
			t.Run(client+scope, func(t *testing.T) {
				o := fixture(t, client, scope, "stdio")
				s, err := Apply(o, "add")
				if err != nil || !s.Changed {
					t.Fatalf("%+v %v", s, err)
				}
				if err := os.Remove(s.Path); err != nil {
					t.Fatal(err)
				}
				if s, err := Apply(o, "repair"); err != nil || !s.Changed {
					t.Fatalf("missing repair: %+v %v", s, err)
				}
			})
		}
	}
}

func TestCustomEntriesAndEditsAreProtected(t *testing.T) {
	for _, client := range Clients {
		t.Run(client, func(t *testing.T) {
			o := fixture(t, client, "project", "stdio")
			s, err := Apply(o, "add")
			if err != nil {
				t.Fatal(err)
			}
			d, _ := read(s.Path, client == "codex")
			d.servers["muxlio"].(map[string]any)["command"] = "custom SECRET"
			var raw []byte
			if client == "codex" {
				raw, err = toml.Marshal(d.root)
			} else {
				raw, err = json.Marshal(d.root)
			}
			if err != nil {
				t.Fatal(err)
			}
			writeFixture(t, s.Path, raw)
			for _, action := range []string{"add", "repair", "remove"} {
				if _, err := Apply(o, action); err == nil || strings.Contains(err.Error(), "SECRET") {
					t.Fatalf("%s must reject without exposing config: %v", action, err)
				}
				next, _ := os.ReadFile(s.Path)
				if !bytes.Equal(raw, next) {
					t.Fatal("edited config was modified")
				}
			}
		})
	}
}

func TestScopeIsolation(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home, project := t.TempDir(), t.TempDir()
	for _, scope := range []string{"user", "project"} {
		for _, client := range Clients {
			o := fixture(t, client, scope, "http")
			o.Home = home
			o.Workspace = project
			if _, err := Apply(o, "add"); err != nil {
				t.Fatal(err)
			}
			for _, otherClient := range Clients {
				for _, otherScope := range []string{"user", "project"} {
					other := o
					other.Client = otherClient
					other.Scope = otherScope
					p, _ := other.Path()
					if otherClient != client || otherScope != scope {
						if _, err := os.Stat(p); !os.IsNotExist(err) {
							t.Fatalf("touched other client/scope: %s", p)
						}
					}
				}
			}
			if _, err := Apply(o, "remove"); err != nil {
				t.Fatal(err)
			}
			p, _ := o.Path()
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestRejectInvalidInputsWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Options)
	}{
		{"scope", func(o *Options) { o.Scope = "" }}, {"client", func(o *Options) { o.Client = "antigravity" }},
		{"relative binary", func(o *Options) { o.Binary = "muxlio-mcp" }}, {"missing binary", func(o *Options) { o.Binary = "/does/not/exist" }},
		{"transport", func(o *Options) { o.Transport = "sse" }},
		{"credentials", func(o *Options) { o.Transport = "http"; o.URL = "https://secret@example.test/mcp" }},
		{"query", func(o *Options) { o.Transport = "http"; o.URL = "https://example.test/mcp?secret=1" }},
		{"remote cleartext", func(o *Options) { o.Transport = "http"; o.URL = "http://example.test/mcp" }},
		{"env", func(o *Options) { o.Transport = "http"; o.BearerEnv = "secret value" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := fixture(t, "cursor", "project", "stdio")
			path, _ := o.Path()
			test.change(&o)
			if _, err := Apply(o, "add"); err == nil {
				t.Fatal("accepted invalid input")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("invalid input wrote config")
			}
		})
	}
	for _, client := range []string{"cursor", "codex"} {
		for _, contents := range []string{"null", "broken SECRET", `{"mcpServers":null}`} {
			o := fixture(t, client, "project", "stdio")
			path, _ := o.Path()
			writeFixture(t, path, []byte(contents))
			if _, err := Apply(o, "add"); err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("bad syntax: %v", err)
			}
			raw, _ := os.ReadFile(path)
			if string(raw) != contents {
				t.Fatal("malformed file modified")
			}
		}
	}
}

func TestRejectConfigSymlink(t *testing.T) {
	o := fixture(t, "kiro", "project", "http")
	other := t.TempDir()
	if err := os.Symlink(other, filepath.Join(o.Workspace, ".kiro")); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(o, "add"); err == nil {
		t.Fatal("followed scope-escaping symlink")
	}
	entries, _ := os.ReadDir(other)
	if len(entries) != 0 {
		t.Fatal("wrote through symlink")
	}
}

func TestCodexHomeAndExactPaths(t *testing.T) {
	for _, client := range Clients {
		for _, scope := range []string{"user", "project"} {
			o := fixture(t, client, scope, "stdio")
			path, _ := o.Path()
			base, _ := filepath.EvalSymlinks(o.Home)
			want := map[string]string{"claudeuser": ".claude.json", "claudeproject": ".mcp.json", "codexuser": ".codex/config.toml", "codexproject": ".codex/config.toml", "cursoruser": ".cursor/mcp.json", "cursorproject": ".cursor/mcp.json", "kirouser": ".kiro/settings/mcp.json", "kiroproject": ".kiro/settings/mcp.json"}[client+scope]
			if path != filepath.Join(base, want) {
				t.Fatalf("%s: %s", client+scope, path)
			}
		}
	}
	o := fixture(t, "codex", "user", "stdio")
	custom := t.TempDir()
	t.Setenv("CODEX_HOME", custom)
	path, err := o.Path()
	resolved, _ := filepath.EvalSymlinks(custom)
	if err != nil || path != filepath.Join(resolved, "config.toml") {
		t.Fatalf("CODEX_HOME: %s %v", path, err)
	}
}

func TestDoctorDetectsMissingExecutable(t *testing.T) {
	o := fixture(t, "cursor", "project", "stdio")
	if _, err := Apply(o, "add"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(o.Binary); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(o, "doctor"); err == nil {
		t.Fatal("doctor accepted a missing executable")
	}
}

func TestStdioTargetsRejectCredentials(t *testing.T) {
	for _, target := range []string{"https://secret@example.test", "https://example.test?token=secret", "https://example.test/#secret", "profile secret"} {
		o := fixture(t, "cursor", "project", "stdio")
		o.Server = target
		if _, err := Apply(o, "add"); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe target: %v", err)
		}
	}
}

func TestFailedRepairPreservesReceipt(t *testing.T) {
	o := fixture(t, "cursor", "project", "http")
	s, err := Apply(o, "add")
	if err != nil {
		t.Fatal(err)
	}
	receipt := s.Path + ".muxlio-receipt"
	before, _ := os.ReadFile(receipt)
	config, _ := os.ReadFile(s.Path)
	target := filepath.Join(t.TempDir(), "config.json")
	writeFixture(t, target, config)
	if err := os.Remove(s.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, s.Path); err != nil {
		t.Fatal(err)
	}
	o.URL = "https://replacement.test/mcp"
	if _, err := Apply(o, "repair"); err == nil {
		t.Fatal("repair followed config symlink")
	}
	after, _ := os.ReadFile(receipt)
	if !bytes.Equal(before, after) {
		t.Fatal("failed repair changed ownership of the original registration")
	}
}
