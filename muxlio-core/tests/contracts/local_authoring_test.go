package contracts_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the public authoring/operations loop without provider credentials or
// paid calls. This integration belongs outside coordinator and worker internals.
func TestLocalAuthoringLoop(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "muxlio-sdk/dist/index.js")); err != nil {
		t.Skip("run npm run build first")
	}
	dir := t.TempDir()
	cli := filepath.Join(dir, "muxlio")
	build := exec.Command("go", "build", "-o", cli, "./cmd/muxlio")
	build.Dir = filepath.Join(root, "muxlio-core")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	call := func(t *testing.T, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, cli, args...)
		cmd.Dir = root
		// tsx CLI creates a Unix socket; long CI scratch paths exceed macOS sun_path.
		cmd.Env = append(os.Environ(), "MUXLIO_HOME="+dir, "MUXLIO_CONFIG="+filepath.Join(dir, "config.json"), "MUXLIO_SERVER=", "TSX_DISABLE_CACHE=1", "TMPDIR=/tmp")
		out, err := cmd.Output()
		if err != nil {
			if failure, ok := err.(*exec.ExitError); ok {
				t.Fatalf("muxlio %v: %v\nstdout: %s\nstderr: %s", args, err, out, failure.Stderr)
			}
			t.Fatalf("muxlio %v: %v\n%s", args, err, out)
		}
		if !json.Valid(out) {
			t.Fatalf("muxlio %v returned invalid JSON: %s", args, out)
		}
		return out
	}
	workflow := filepath.Join(root, "examples/local.workflow.ts")
	input := filepath.Join(root, "examples/local.input.json")
	call(t, "check", workflow, "--json")
	compiled := filepath.Join(dir, "plan.json")
	call(t, "build", workflow, "--out", compiled, "--json")
	fake := filepath.Join(root, "examples/local.fake.json")
	call(t, "test", compiled, "--input", input, "--fake-config", fake, "--json")
	out := call(t, "run", workflow, input, "--data-dir", dir, "--fake-config", fake, "--json")
	var result struct {
		RunID  string         `json:"runId"`
		State  string         `json:"state"`
		Output map[string]any `json:"output"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("run JSON: %v\n%s", err, out)
	}
	if result.RunID == "" || result.State != "succeeded" || result.Output["path"] != "summary.md" {
		t.Fatalf("result=%+v", result)
	}
	inspected := call(t, "inspect", result.RunID, "--data-dir", dir, "--json")
	if !strings.Contains(string(inspected), `"succeeded"`) || !strings.Contains(string(inspected), `summary.md`) {
		t.Fatalf("inspection: %s", inspected)
	}
	graph := call(t, "graph", result.RunID, "--data-dir", dir, "--json")
	for _, id := range []string{"prepare", "summarize"} {
		if !strings.Contains(string(graph), id) {
			t.Fatalf("graph missing %s: %s", id, graph)
		}
	}
	artifacts := call(t, "artifacts", result.RunID, "--json")
	if !strings.Contains(string(artifacts), `"summary"`) {
		t.Fatalf("fake artifacts missing: %s", artifacts)
	}

	t.Run("coordinator and local worker", func(t *testing.T) {
		adapters := filepath.Join(dir, "adapters")
		if err := os.MkdirAll(adapters, 0700); err != nil {
			t.Fatal(err)
		}
		write := func(path string, raw []byte, mode os.FileMode) {
			t.Helper()
			if err := os.WriteFile(path, raw, mode); err != nil {
				t.Fatal(err)
			}
		}
		build := exec.Command("go", "build", "-o", filepath.Join(adapters, "command"), "./cmd/command")
		build.Dir = filepath.Join(root, "muxlio-adapters")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build command adapter: %v\n%s", err, out)
		}
		manifest, err := os.ReadFile(filepath.Join(root, "muxlio-adapters/cmd/command/adapter.json"))
		if err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(adapters, "command.json"), manifest, 0600)
		// Stand in for the paid agent using only the shared adapter protocol.
		write(filepath.Join(adapters, "test-summary.json"), []byte(`{"contractVersion":"1","probeVersion":"1","capabilities":{"artifacts":true}}`), 0600)
		write(filepath.Join(adapters, "test-summary"), []byte(`#!/bin/sh
if [ "$1" = --muxlio-probe ]; then
  printf '%s' '{"ready":true,"checks":[{"name":"version","passed":true}]}'
else
  cat >/dev/null
  printf '# Local summary\n' >summary.md
  printf '%s' '{"outputs":{}}'
fi
`), 0700)
		planBytes, err := os.ReadFile(compiled)
		if err != nil {
			t.Fatal(err)
		}
		var fixturePlan map[string]any
		if err := json.Unmarshal([]byte(strings.ReplaceAll(string(planBytes), "agent/codex", "test/summary")), &fixturePlan); err != nil {
			t.Fatal(err)
		}
		delete(fixturePlan, "digest") // The fixture changes the executor contract.
		planBytes, err = json.Marshal(fixturePlan)
		if err != nil {
			t.Fatal(err)
		}
		write(compiled, planBytes, 0600)
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := listener.Addr().String()
		listener.Close()
		base := "http://" + addr
		config, _ := json.Marshal(map[string]any{
			"runtimeDir": dir, "addr": addr, "workspace": t.TempDir(), "adapters": adapters,
			"capabilities": []string{"command", "test/summary"}, "token": "local-test",
			"workerEnv": map[string]string{"PATH": os.Getenv("PATH")},
			"executors": map[string]any{"command": map[string]bool{"allow": true}},
		})
		configPath := filepath.Join(dir, "config.json")
		write(configPath, config, 0600)
		service := exec.Command(cli, "service", "start", "--config", configPath)
		service.Env = append(os.Environ(), "MUXLIO_HOME="+dir, "MUXLIO_DAEMON=0", "MUXLIO_CONFIG="+configPath)
		// A file avoids concurrent buffer reads while the service logs.
		logs, err := os.Create(filepath.Join(dir, "service-test.log"))
		if err != nil {
			t.Fatal(err)
		}
		defer logs.Close()
		service.Stdout, service.Stderr = logs, logs
		if err := service.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() {
			service.Process.Kill()
			service.Wait()
			if t.Failed() {
				raw, _ := os.ReadFile(logs.Name())
				t.Logf("service log: %s", raw)
			}
		}()
		client := &http.Client{Timeout: time.Second}
		deadline := time.Now().Add(10 * time.Second)
		for {
			resp, err := client.Get(base + "/healthz")
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					break
				}
			}
			if time.Now().After(deadline) {
				raw, _ := os.ReadFile(logs.Name())
				t.Fatalf("service did not start: %s", raw)
			}
			time.Sleep(25 * time.Millisecond)
		}
		out := call(t, "run", compiled, input, "--server", base, "--json")
		if err := json.Unmarshal(out, &result); err != nil || result.State != "succeeded" {
			t.Fatalf("worker result: %s (%v)", out, err)
		}
		inspected := call(t, "inspect", result.RunID, "--json")
		if !strings.Contains(string(inspected), `"succeeded"`) {
			t.Fatalf("worker inspection: %s", inspected)
		}
		artifacts := call(t, "artifacts", result.RunID, "--json")
		if !strings.Contains(string(artifacts), `"digest": "sha256:`) {
			t.Fatalf("worker artifacts: %s", artifacts)
		}
		req, _ := http.NewRequest(http.MethodGet, base+"/v1/runs/"+result.RunID+"/attempts", nil)
		req.Header.Set("Authorization", "Bearer local-test")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		attempts, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(attempts), `"executor":"command"`) || !strings.Contains(string(attempts), `"executor":"test/summary"`) {
			t.Fatalf("mixed worker attempts: %s", attempts)
		}
	})
}
