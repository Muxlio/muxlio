package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/coordinatorconfig"
	"github.com/muxlio/muxlio-core/internal/integrations"
	"github.com/muxlio/muxlio-core/internal/mcp"
)

func TestInitDefaultCoordinatorAuthenticatesAndMigrates(t *testing.T) {
	t.Setenv("MUXLIO_SERVER", "")
	t.Setenv("MUXLIO_TOKEN", "")
	for _, mode := range []string{"fresh", "literal default", "remote default and local collision"} {
		t.Run(mode, func(t *testing.T) {
			root, ws := t.TempDir(), t.TempDir()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer admin" {
					w.WriteHeader(401)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"registeredWorkers":[]}`))
			}))
			defer server.Close()
			u, _ := url.Parse(server.URL)
			cfg := map[string]any{"addr": u.Host, "token": "admin"}
			switch mode {
			case "literal default":
				cfg["defaultServer"] = server.URL
			case "remote default and local collision":
				cfg["defaultServer"] = "remote"
				cfg["servers"] = map[string]any{"remote": map[string]string{"url": "https://remote.example", "token": "scoped"}, "local": map[string]string{"url": "https://custom.example"}}
			}
			path := filepath.Join(root, "config.json")
			raw, _ := json.Marshal(cfg)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"--data-dir", root, "--workspace", ws, "--json"}
			for i := 0; i < 2; i++ {
				if code := cmdInit(args, "", true); code != 0 {
					t.Fatalf("init = %d", code)
				}
			}
			after, _ := os.ReadFile(path)
			var saved map[string]any
			if err := json.Unmarshal(after, &saved); err != nil {
				t.Fatal(err)
			}
			expected := cfg["defaultServer"]
			if expected == nil {
				expected = "local"
			}
			if saved["defaultServer"] != expected {
				t.Fatalf("default changed: %v", saved["defaultServer"])
			}
			endpoint, token, err := coordinatorconfig.ResolveFile(path, "", "", "")
			if expected == "remote" {
				if err != nil || endpoint != "https://remote.example" || token != "scoped" {
					t.Fatalf("remote default: %s %v", endpoint, err)
				}
				if saved["servers"].(map[string]any)["local"].(map[string]any)["url"] != "https://custom.example" {
					t.Fatal("overwrote local profile")
				}
			} else {
				if err != nil || endpoint != server.URL || token != "admin" {
					t.Fatalf("local resolution: %s %v", endpoint, err)
				}
				if code := cmdIntegrations([]string{"doctor", "--data-dir", root}, "", false); code != 0 {
					t.Fatalf("doctor = %d", code)
				}
			}
			for _, target := range []string{server.URL, "https://unknown.example"} {
				_, token, err := coordinatorconfig.ResolveFile(path, target, "", "")
				if err != nil || token != "" {
					t.Fatalf("literal URL inherited credential: %v", err)
				}
			}
			if profile, err := ensureLocalCoordinatorProfile(path); err != nil || profile == "" {
				t.Fatal(profile, err)
			}
			next, _ := os.ReadFile(path)
			if string(after) != string(next) {
				t.Fatal("repeat migration rewrote config")
			}
		})
	}
}

// This helper launches the real stdio entry point with no coordinator flag.
func TestInitializedMCPProcess(t *testing.T) {
	if os.Getenv("MUXLIO_INIT_MCP_HELPER") != "1" {
		return
	}
	os.Args = []string{"muxlio-mcp"}
	flag.CommandLine = flag.NewFlagSet("muxlio-mcp", flag.ExitOnError)
	mcp.Main()
	os.Exit(0)
}

func TestInitThenMCPWithoutServerFlag(t *testing.T) {
	root, ws := t.TempDir(), t.TempDir()
	token := "initialized-admin"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/healthz":
			w.Write([]byte(`{"status":"ok"}`))
		case "/v1/workers":
			w.Write([]byte(`{"registeredWorkers":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	path := filepath.Join(root, "config.json")
	raw, _ := json.Marshal(map[string]string{"addr": u.Host, "token": token})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if code := cmdInit([]string{"--data-dir", root, "--workspace", ws, "--json"}, "", true); code != 0 {
		t.Fatalf("init = %d", code)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestInitializedMCPProcess$")
	cmd.Env = append(os.Environ(), "MUXLIO_INIT_MCP_HELPER=1", "MUXLIO_CONFIG="+path, "MUXLIO_SERVER=", "MUXLIO_TOKEN=")
	cmd.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"server_health","arguments":{}}}
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_workers","arguments":{}}}
`)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("stdio: %v", err)
	}
	responses := 0
	decoder := json.NewDecoder(bytes.NewReader(output))
	for decoder.More() {
		var response struct {
			ID     int
			Error  any
			Result struct {
				IsError bool
				Tools   []struct{ Name string }
			}
		}
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response.Error != nil || response.Result.IsError {
			t.Fatalf("MCP request %d failed", response.ID)
		}
		if response.ID == 2 && len(response.Result.Tools) == 0 {
			t.Fatal("no tools available")
		}
		responses++
	}
	if responses != 4 {
		t.Fatalf("received %d responses", responses)
	}
}

func TestRepeatInitMigratesManagedRegistration(t *testing.T) {
	root, ws := t.TempDir(), t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "config.json")
	o := integrations.Options{Client: "cursor", Scope: "project", Workspace: ws, Transport: "stdio", Binary: exe, Config: config}
	if _, err := integrations.Apply(o, "add"); err != nil {
		t.Fatal(err)
	}
	args := []string{"--client", "cursor", "--mcp-binary", exe, "--data-dir", root, "--workspace", ws, "--json"}
	if code := cmdInit(args, "", true); code != 0 {
		t.Fatalf("migration = %d", code)
	}
	raw, err := os.ReadFile(filepath.Join(ws, ".cursor", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Servers map[string]struct{ Args []string } `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	args = saved.Servers["muxlio"].Args
	if len(args) != 2 || args[0] != "--server" || args[1] != "local" {
		t.Fatalf("registration args = %v", args)
	}
	if code := cmdIntegrations([]string{"doctor", "--client", "cursor", "--scope", "project", "--workspace", ws, "--data-dir", root, "--config-only"}, "", false); code != 0 {
		t.Fatalf("doctor = %d", code)
	}
}

func TestLiteralDefaultRegistrationPreservesCredentials(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:7600", "https://remote.example"} {
		for _, legacy := range []bool{false, true} {
			t.Run(endpoint+"/legacy="+strconv.FormatBool(legacy), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config.json")
				key := "servers"
				if legacy {
					key = "profiles"
				}
				cfg := map[string]any{
					"defaultServer": endpoint, "token": "admin",
					key: map[string]any{
						endpoint:  map[string]string{"url": endpoint, "token": "scoped"},
						"default": map[string]string{"url": endpoint, "token": "other-scope"},
					},
				}
				raw, _ := json.Marshal(cfg)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					profile, err := ensureLocalCoordinatorProfile(path)
					if err != nil {
						t.Fatal(err)
					}
					gotURL, token, err := coordinatorconfig.ResolveFile(path, profile, "", "")
					if err != nil || gotURL != endpoint || token != "scoped" {
						t.Fatalf("registration changed selected credentials: endpoint=%s scoped=%t err=%v", gotURL, token == "scoped", err)
					}
					_, token, err = coordinatorconfig.ResolveFile(path, endpoint, "", "")
					if err != nil || token != "" {
						t.Fatalf("explicit URL inherited credentials: %v", err)
					}
				}
			})
		}
	}
}
