package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/mcp"
)

// The subprocess runs the real MCP stdio entry point using exactly the command,
// args and MUXLIO_CONFIG installed into each client fixture.
func TestMain(m *testing.M) {
	if os.Getenv("MUXLIO_INTEGRATION_MCP_HELPER") == "1" {
		flag.CommandLine = flag.NewFlagSet("muxlio-mcp", flag.ExitOnError)
		mcp.Main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestInstalledConfigurationsConnectToMuxlio(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-credential" {
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/healthz":
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		case "/v1/workers":
			_, _ = io.WriteString(w, `{"workers":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	handler, err := mcp.NewHTTPHandler(upstream.URL, "fixture-credential", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := httptest.NewServer(handler)
	defer endpoint.Close()
	for _, client := range Clients {
		for _, scope := range []string{"user", "project"} {
			for _, transport := range []string{"stdio", "http"} {
				t.Run(client+"/"+scope+"/"+transport, func(t *testing.T) {
					o := fixture(t, client, scope, transport)
					o.Binary, err = os.Executable()
					if err != nil {
						t.Fatal(err)
					}
					o.URL = endpoint.URL + "/mcp"
					config, _ := json.Marshal(map[string]any{"defaultServer": "local", "servers": map[string]any{"local": map[string]string{"url": upstream.URL, "credentialRef": "root"}}, "token": "fixture-credential"})
					writeFixture(t, o.Config, config)
					s, err := Apply(o, "add")
					if err != nil {
						t.Fatal(err)
					}
					d, err := read(s.Path, client == "codex")
					if err != nil {
						t.Fatal(err)
					}
					entry := d.servers["muxlio"].(map[string]any)
					var call func(map[string]any) map[string]any
					if transport == "stdio" {
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						args := []string{}
						for _, arg := range entry["args"].([]any) {
							args = append(args, arg.(string))
						}
						cmd := exec.CommandContext(ctx, entry["command"].(string), args...)
						cmd.Env = append(os.Environ(), "MUXLIO_INTEGRATION_MCP_HELPER=1", "MUXLIO_CONFIG="+entry["env"].(map[string]any)["MUXLIO_CONFIG"].(string), "MUXLIO_SERVER=", "MUXLIO_TOKEN=")
						input, err := cmd.StdinPipe()
						if err != nil {
							t.Fatal(err)
						}
						output, err := cmd.StdoutPipe()
						if err != nil {
							t.Fatal(err)
						}
						if err := cmd.Start(); err != nil {
							t.Fatal(err)
						}
						defer func() {
							_ = input.Close()
							if err := cmd.Wait(); err != nil {
								t.Error(err)
							}
						}()
						enc, dec := json.NewEncoder(input), json.NewDecoder(output)
						call = func(request map[string]any) map[string]any {
							if err := enc.Encode(request); err != nil {
								t.Fatal(err)
							}
							var response map[string]any
							if err := dec.Decode(&response); err != nil {
								t.Fatal(err)
							}
							return response
						}
					} else {
						call = func(request map[string]any) map[string]any {
							raw, _ := json.Marshal(request)
							req, err := http.NewRequest("POST", entry["url"].(string), bytes.NewReader(raw))
							if err != nil {
								t.Fatal(err)
							}
							req.Header.Set("Authorization", "Bearer fixture-credential")
							req.Header.Set("Content-Type", "application/json")
							req.Header.Set("Accept", "application/json, text/event-stream")
							req.Header.Set("MCP-Protocol-Version", "2025-11-25")
							resp, err := http.DefaultClient.Do(req)
							if err != nil {
								t.Fatal(err)
							}
							defer resp.Body.Close()
							var response map[string]any
							if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
								t.Fatal(err)
							}
							if resp.StatusCode != 200 {
								t.Fatalf("HTTP %d", resp.StatusCode)
							}
							return response
						}
					}
					requests := []map[string]any{
						{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "fixture", "version": "1"}}},
						{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "server_health", "arguments": map[string]any{}}},
						{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "get_workers", "arguments": map[string]any{}}},
					}
					for _, request := range requests {
						response := call(request)
						if response["error"] != nil || response["result"] == nil {
							t.Fatalf("MCP request failed: %v", response)
						}
						if result, ok := response["result"].(map[string]any); ok && result["isError"] == true {
							t.Fatalf("tool failed: %v", response)
						}
					}
					if again, err := Apply(o, "add"); err != nil || again.Changed {
						t.Fatalf("second installation: %+v %v", again, err)
					}
					if info, err := os.Stat(filepath.Clean(s.Path)); err != nil || info.Mode().Perm() != 0600 {
						t.Fatalf("config permissions: %v %v", info, err)
					}
				})
			}
		}
	}
}
