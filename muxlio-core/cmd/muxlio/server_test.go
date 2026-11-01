package main

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// TestServiceRetryFailedRun verifies POST /v1/runs/{id}/retry after a failed
// run does not panic the coordinator when the run worker unwinds without a
// prior workers channel (the recovered-run path), and that the service keeps
// serving HTTP after retry completes.
func TestServiceRetryFailedRun(t *testing.T) {
	root := t.TempDir()
	adapters := filepath.Join(root, "installed", "libexec", "muxlio", "adapters")
	workspace := filepath.Join(root, "authorized-workspace")
	ui := filepath.Join(root, "installed", "share", "muxlio", "ui")
	for _, dir := range []string{adapters, workspace, ui} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, raw string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(raw), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(ui, "index.html"), "<html>UI</html>", 0600)
	failOnce := `#!/bin/sh
exec python3 -c '
import json, os, sys
r = json.load(sys.stdin)
work = r.get("workDir") or ""
marker = os.path.normpath(os.path.join(work, "..", "..", ".fail-once"))
if os.path.isfile(marker):
    print("{\"outputs\":{\"result\":\"retried-ok\"}}")
    sys.exit(0)
os.makedirs(os.path.dirname(marker), exist_ok=True)
open(marker, "w").close()
sys.exit(1)
'
`
	write(filepath.Join(adapters, "fake-fail-once"), failOnce, 0700)
	echo := `#!/bin/sh
exec python3 -c '
import json, sys
r = json.load(sys.stdin)
print(json.dumps({"outputs":{"value":"checkpoint-value"}}))
'
`
	write(filepath.Join(adapters, "fake-echo"), echo, 0700)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()

	runtimeDir := filepath.Join(root, "runtime")
	config := serviceConfig{
		RuntimeDir:   runtimeDir,
		Addr:         addr,
		Adapters:     adapters,
		UI:           ui,
		Workspace:    workspace,
		Capabilities: []string{"fake/fail-once", "fake/echo"},
	}
	raw, _ := json.Marshal(config)
	configPath := filepath.Join(root, "config.json")
	write(configPath, string(raw), 0600)

	env := append(os.Environ(), "MUXLIO_TEST_CLI=1", "MUXLIO_CONFIG="+configPath, "MUXLIO_HOME="+runtimeDir)
	client := &http.Client{Timeout: 5 * time.Second}
	base := "http://" + addr

	start := exec.Command(os.Args[0], "-test.run=TestCLIProcess", "--", "service", "start", "--config", configPath)
	start.Dir = root
	start.Env = env
	logs := new(bytes.Buffer)
	start.Stdout = logs
	start.Stderr = logs
	if err := start.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if start.ProcessState == nil {
			_ = start.Process.Kill()
			_ = start.Wait()
		}
		stop := exec.Command(os.Args[0], "-test.run=TestCLIProcess", "--", "service", "stop", "--config", configPath)
		stop.Dir = root
		stop.Env = env
		stop.Run()
	})

	deadline := time.Now().Add(8 * time.Second)
	healthy := false
	for time.Now().Before(deadline) {
		resp, e := client.Get(base + "/healthz")
		if e == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				healthy = true
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !healthy {
		t.Fatalf("service failed to start: %s", logs.Bytes())
	}

	configRaw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var authConfig struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(configRaw, &authConfig); err != nil || authConfig.Token == "" {
		t.Fatalf("coordinator token was not generated: %v", err)
	}
	auth := func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+authConfig.Token)
	}

	planJSON := `{"irVersion":"1.0","name":"retry-run","root":"root","subgraphs":{"root":{"nodes":[{"id":"checkpoint","kind":"execute","executor":"fake/echo","outputs":{"value":{}}},{"id":"work","kind":"execute","executor":"fake/fail-once","inputs":{"input":{"ref":{"node":"checkpoint","port":"value"}}},"outputs":{"result":{}}}],"output":{"result":{"ref":{"node":"work","port":"result"}}}}}}`
	planPath := filepath.Join(root, "plan.json")
	inputPath := filepath.Join(root, "input.json")
	write(planPath, planJSON, 0600)
	write(inputPath, "{}", 0600)

	submit := exec.Command(os.Args[0], "-test.run=TestCLIProcess", "--", "submit", "--server", base, planPath, inputPath)
	submit.Env = env
	submit.Dir = root
	out, err := submit.CombinedOutput()
	if err != nil {
		t.Fatalf("submit: %v %s", err, out)
	}
	var submitResult struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(out, &submitResult); err != nil || submitResult.RunID == "" {
		t.Fatalf("submit output %s", out)
	}
	runID := submitResult.RunID

	waitState := func(want string) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		var lastBody string
		for time.Now().Before(deadline) {
			req, _ := http.NewRequest(http.MethodGet, base+"/v1/runs/"+runID, nil)
			auth(req)
			resp, e := client.Do(req)
			if e != nil {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastBody = string(body)
			if resp.StatusCode != http.StatusOK {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			var run struct {
				State string `json:"state"`
			}
			if json.Unmarshal(body, &run) == nil && run.State == want {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("run %s did not reach state %q (last body %s; service log tail:\n%s)", runID, want, lastBody, tailLogs(logs, 4000))
	}

	waitState("failed")

	retryReq, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/v1/runs/%s/retry", base, runID), nil)
	auth(retryReq)
	retryResp, err := client.Do(retryReq)
	if err != nil {
		t.Fatal(err)
	}
	retryBody, _ := io.ReadAll(retryResp.Body)
	retryResp.Body.Close()
	if retryResp.StatusCode != http.StatusOK {
		t.Fatalf("retry status = %d body = %s logs = %s", retryResp.StatusCode, retryBody, logs.Bytes())
	}

	waitState("succeeded")

	health, err := client.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("health check after retry: %v logs = %s", err, logs.Bytes())
	}
	health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("health status after retry = %d", health.StatusCode)
	}

	statusReq, _ := http.NewRequest(http.MethodGet, base+"/v1/runs/"+runID, nil)
	auth(statusReq)
	statusResp, err := client.Do(statusReq)
	if err != nil {
		t.Fatal(err)
	}
	statusBody, _ := io.ReadAll(statusResp.Body)
	statusResp.Body.Close()
	if !strings.Contains(string(statusBody), "retried-ok") {
		t.Fatalf("expected retried output, got %s", statusBody)
	}
	var finalRun struct {
		Output any `json:"output"`
	}
	if err := json.Unmarshal(statusBody, &finalRun); err != nil {
		t.Fatalf("decode final run: %v", err)
	}
	switch v := finalRun.Output.(type) {
	case string:
		if v != "retried-ok" {
			t.Fatalf("unexpected final output: %#v", finalRun.Output)
		}
	case map[string]any:
		if v["result"] != "retried-ok" {
			t.Fatalf("unexpected final output: %#v", finalRun.Output)
		}
	default:
		t.Fatalf("unexpected final output type: %#v", finalRun.Output)
	}
}

func tailLogs(logs *bytes.Buffer, max int) string {
	b := logs.Bytes()
	if len(b) <= max {
		return string(b)
	}
	return string(b[len(b)-max:])
}

// TestServiceCoordinatorLogging verifies service.log captures run lifecycle
// fields and never writes configured secret values.
func TestServiceCoordinatorLogging(t *testing.T) {
	const secretValue = "coordinator-log-test-secret-xyzzy"
	root := t.TempDir()
	adapters := filepath.Join(root, "installed", "libexec", "muxlio", "adapters")
	workspace := filepath.Join(root, "authorized-workspace")
	ui := filepath.Join(root, "installed", "share", "muxlio", "ui")
	for _, dir := range []string{adapters, workspace, ui} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, raw string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(raw), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(ui, "index.html"), "<html>UI</html>", 0600)
	echo := `#!/bin/sh
exec python3 -c '
import json, sys
r = json.load(sys.stdin)
secret = r.get("secrets", {}).get("apiKey", "")
print(json.dumps({"outputs":{"echoed": secret}}))
'
`
	write(filepath.Join(adapters, "fake-echo"), echo, 0700)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()

	runtimeDir := filepath.Join(root, "runtime")
	config := serviceConfig{
		RuntimeDir:   runtimeDir,
		Addr:         addr,
		Adapters:     adapters,
		UI:           ui,
		Workspace:    workspace,
		Capabilities: []string{"fake/echo"},
	}
	raw, _ := json.Marshal(config)
	configPath := filepath.Join(root, "config.json")
	write(configPath, string(raw), 0600)

	env := append(os.Environ(), "MUXLIO_TEST_CLI=1", "MUXLIO_CONFIG="+configPath, "MUXLIO_HOME="+runtimeDir)
	client := &http.Client{Timeout: 5 * time.Second}
	base := "http://" + addr
	logPath := filepath.Join(runtimeDir, "logs", "service.log")

	start := exec.Command(os.Args[0], "-test.run=TestCLIProcess", "--", "service", "start", "--config", configPath)
	start.Dir = root
	start.Env = env
	if err := start.Run(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop := exec.Command(os.Args[0], "-test.run=TestCLIProcess", "--", "service", "stop", "--config", configPath)
		stop.Dir = root
		stop.Env = env
		stop.Run()
	})

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		resp, e := client.Get(base + "/healthz")
		if e == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}

	configRaw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var authConfig struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(configRaw, &authConfig); err != nil || authConfig.Token == "" {
		t.Fatalf("coordinator token was not generated: %v", err)
	}
	auth := func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+authConfig.Token)
	}

	planJSON := fmt.Sprintf(`{"irVersion":"1.0","name":"log-run","root":"root","subgraphs":{"root":{"nodes":[{"id":"work","kind":"execute","executor":"fake/echo","params":{"executionPolicy":{"required":true,"allowedCapabilities":["fake/echo"],"allowedSecrets":["apiKey"]}},"outputs":{"echoed":{}}}],"output":{"echoed":{"ref":{"node":"work","port":"echoed"}}}}}}`)
	planPath := filepath.Join(root, "plan.json")
	inputPath := filepath.Join(root, "input.json")
	write(planPath, planJSON, 0600)
	write(inputPath, fmt.Sprintf(`{"secrets":{"apiKey":%q}}`, secretValue), 0600)

	submit := exec.Command(os.Args[0], "-test.run=TestCLIProcess", "--", "submit", "--server", base, planPath, inputPath)
	submit.Env = env
	submit.Dir = root
	out, err := submit.CombinedOutput()
	if err != nil {
		t.Fatalf("submit: %v %s", err, out)
	}
	var submitResult struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(out, &submitResult); err != nil || submitResult.RunID == "" {
		t.Fatalf("submit output %s", out)
	}
	runID := submitResult.RunID

	waitSucceeded := func() {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			req, _ := http.NewRequest(http.MethodGet, base+"/v1/runs/"+runID, nil)
			auth(req)
			resp, e := client.Do(req)
			if e != nil {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var run struct {
				State string `json:"state"`
			}
			if json.Unmarshal(body, &run) == nil && run.State == "succeeded" {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("run %s did not succeed", runID)
	}
	waitSucceeded()

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read service log: %v", err)
	}
	logText := string(logData)
	if strings.Contains(logText, secretValue) {
		t.Fatalf("secret leaked into service.log")
	}
	if strings.Contains(logText, authConfig.Token) {
		t.Fatalf("coordinator bearer token leaked into service.log")
	}
	for _, needle := range []string{
		"run.accepted",
		"run.state_changed",
		"activation.started",
		"activation.finished",
		"attempt.started",
		"attempt.finished",
		"worker.assigned",
		"worker.completed",
		`"runId":"` + runID + `"`,
		`"workerId":"local"`,
		`"executor":"fake/echo"`,
		`"attempt":1`,
	} {
		if !strings.Contains(logText, needle) {
			t.Fatalf("service.log missing %q\n%s", needle, tailLogs(bytes.NewBuffer(logData), 8000))
		}
	}
	activationNeedle := `"activationId":"` + runID + `:work"`
	if !strings.Contains(logText, activationNeedle) {
		t.Fatalf("service.log missing activation id %q\n%s", activationNeedle, tailLogs(bytes.NewBuffer(logData), 8000))
	}
}
