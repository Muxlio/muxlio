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
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Runs the actual CLI in an isolated subprocess, never any host service.
func TestCLIProcess(t *testing.T) {
	if os.Getenv("MUXLIO_TEST_CLI") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"muxlio"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func TestServiceRemoteLifecycle(t *testing.T) {
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
		if filepath.Dir(path) == adapters {
			raw = readyFixtureAdapter(t, path, raw)
		}
		if err := os.WriteFile(path, []byte(raw), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(ui, "index.html"), "<html>Installed UI</html>", 0600)
	tools := filepath.Join(root, "worker-tools")
	if err := os.MkdirAll(tools, 0700); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(tools, "fixture-result"), "#!/bin/sh\nprintf '%s' '{\"outputs\":{\"result\":\"persisted overnight\"}}'\n", 0700)
	write(filepath.Join(adapters, "fake-slow"), "#!/bin/sh\nsleep 1\nfixture-result\n", 0700)
	write(filepath.Join(adapters, "fake-timeout"), "#!/bin/sh\nsleep 30\n", 0700)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	runtimeDir := filepath.Join(root, "runtime")
	mcpPort, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mcpAddr := mcpPort.Addr().String()
	mcpPort.Close()
	write(filepath.Join(root, "mcp-token"), "listener-secret", 0600)
	config := serviceConfig{WorkerCapacity: 2, RuntimeDir: runtimeDir, Addr: addr, Adapters: adapters, UI: ui, Workspace: workspace, Capabilities: []string{"fake/slow", "fake/timeout", "fake/barrier"}, WorkerEnv: map[string]string{"PATH": tools + ":/usr/bin:/bin"}, AttemptTimeoutSeconds: 15, MCP: &serviceMCPConfig{Listen: mcpAddr, TokenFile: "mcp-token"}}
	// Each admitted adapter records a start and waits for an explicit release.
	// No duration comparison is needed to prove that executions overlap.
	barrierDir := filepath.Join(root, "barrier")
	if err := os.MkdirAll(barrierDir, 0700); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(adapters, "fake-barrier"), fmt.Sprintf("#!/bin/sh\nmktemp '%s/started.XXXXXX' >/dev/null\nwhile [ ! -f '%s/release' ]; do sleep 0.05; done\nfixture-result\n", barrierDir, barrierDir), 0700)
	raw, _ := json.Marshal(config)
	configPath := filepath.Join(root, "config.json")
	write(configPath, string(raw), 0600)
	client := &http.Client{Timeout: time.Second}
	base := "http://" + addr
	env := append(os.Environ(), "MUXLIO_TEST_CLI=1", "MUXLIO_CONFIG="+configPath, "MUXLIO_HOME="+runtimeDir, "PATH=/usr/bin:/bin")
	start := func() (*exec.Cmd, *bytes.Buffer) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=TestCLIProcess", "--", "service", "start", "--config", configPath)
		cmd.Dir = root // no source checkout needed at runtime
		cmd.Env = env
		logs := new(bytes.Buffer)
		cmd.Stdout = logs
		cmd.Stderr = logs
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
		t.Cleanup(func() {
			// Ensure the daemon child is stopped even if the test fails early.
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
				if resp.StatusCode == 200 {
					return cmd, logs
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatal("service failed to start")
		return nil, nil
	}
	stop := func(cmd *exec.Cmd, logs *bytes.Buffer) {
		t.Helper()
		cli := exec.Command(os.Args[0], "-test.run=TestCLIProcess", "--", "service", "stop", "--config", configPath)
		cli.Dir = root
		cli.Env = env
		if out, err := cli.CombinedOutput(); err != nil {
			t.Fatalf("service stop: %v: %s", err, out)
		}
		conn, err := net.DialTimeout("tcp", mcpAddr, time.Second)
		if err == nil {
			conn.Close()
			t.Fatal("MCP listener survived service stop")
		}
		if _, err := os.Stat(filepath.Join(runtimeDir, "service.pid")); !os.IsNotExist(err) {
			t.Fatal("PID metadata survived shutdown")
		}
	}
	cmd, logs := start()
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
	checkMCP := func() {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, "http://"+mcpAddr+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_runs","arguments":{}}}`))
		req.Header.Set("Authorization", "Bearer listener-secret")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-11-25")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var rpc struct {
			Result struct {
				IsError bool `json:"isError"`
			}
			Error any
		}
		if err := json.Unmarshal(body, &rpc); err != nil || resp.StatusCode != 200 || rpc.Error != nil || rpc.Result.IsError {
			t.Fatalf("MCP did not reach authenticated coordinator: %d %s (%v)", resp.StatusCode, body, err)
		}
		req.Body = io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		req.ContentLength = -1
		req.Header.Set("Authorization", "Bearer "+authConfig.Token)
		resp, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("MCP accepted upstream token instead of configured listener token: %d", resp.StatusCode)
		}
	}
	checkMCP()
	duplicate := exec.Command(os.Args[0], "-test.run=TestCLIProcess", "--", "service", "start", "--config", configPath)
	duplicate.Env = env
	if out, err := duplicate.CombinedOutput(); err == nil || !bytes.Contains(out, []byte("already running")) {
		t.Fatalf("duplicate service should be rejected: %v %s", err, out)
	}
	listReq, _ := http.NewRequest(http.MethodGet, base+"/v1/runs", nil)
	listReq.Header.Set("Authorization", "Bearer "+authConfig.Token)
	resp, err := client.Do(listReq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("runs API status = %d", resp.StatusCode)
	}
	resp, err = client.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), "Installed UI") {
		t.Fatalf("UI missing: %s", page)
	}
	p := `{"irVersion":"1.0","name":"overnight","root":"root","subgraphs":{"root":{"nodes":[{"id":"work","kind":"execute","executor":"fake/slow","outputs":{"result":{}}}]}}}`
	planPath, inputPath := filepath.Join(root, "plan.json"), filepath.Join(root, "input.json")
	write(planPath, p, 0600)
	write(inputPath, "{}", 0600)
	submit := func() string {
		t.Helper()
		cli := exec.Command(os.Args[0], "-test.run=TestCLIProcess", "--", "submit", "--server", base, planPath, inputPath)
		cli.Env = env
		cli.Dir = root
		out, err := cli.CombinedOutput()
		if err != nil {
			t.Fatalf("submit: %v %s", err, out)
		}
		var result struct {
			RunID string `json:"runId"`
		}
		if err = json.Unmarshal(out, &result); err != nil || result.RunID == "" {
			t.Fatalf("submit output %s", out)
		}
		return result.RunID
	}
	check := func(id string) string {
		t.Helper()
		cli := exec.Command(os.Args[0], "-test.run=TestCLIProcess", "--", "status", "--server", base, id)
		cli.Env = env
		out, e := cli.CombinedOutput()
		if e != nil {
			t.Fatalf("status: %v %s", e, out)
		}
		return string(out)
	}
	id := submit() // client exits while fake adapter is still executing
	if current := check(id); strings.Contains(current, `"state":"succeeded"`) {
		t.Fatal("task unexpectedly finished before submit client disconnected")
	}
	waitSuccess := func(id string) {
		t.Helper()
		deadline := time.Now().Add(25 * time.Second)
		for time.Now().Before(deadline) {
			if result := check(id); strings.Contains(result, `"state":"succeeded"`) {
				if !strings.Contains(result, "persisted overnight") {
					t.Fatal(result)
				}
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("task did not succeed: %s", check(id))
	}
	waitSuccess(id)
	for _, mode := range []string{"empty", "shared-write"} {
		t.Run(mode, func(t *testing.T) {
			barrierPlan := strings.Replace(p, `"executor":"fake/slow"`, `"executor":"fake/barrier","workspaceMode":"`+mode+`"`, 1)
			write(planPath, barrierPlan, 0600)
			first, second := submit(), submit()
			want := 2
			if mode == "shared-write" {
				want = 1
			}
			deadline := time.Now().Add(8 * time.Second)
			for {
				files, err := filepath.Glob(filepath.Join(barrierDir, "started.*"))
				if err != nil {
					t.Fatal(err)
				}
				if len(files) == want {
					break
				}
				if time.Now().After(deadline) || len(files) > want {
					t.Fatalf("%s: started %d adapters, want %d", mode, len(files), want)
				}
				time.Sleep(25 * time.Millisecond)
			}
			if mode == "shared-write" {
				// Leave multiple poll opportunities while the first write holds its lock.
				time.Sleep(1500 * time.Millisecond)
				files, _ := filepath.Glob(filepath.Join(barrierDir, "started.*"))
				if len(files) != 1 {
					t.Fatal("shared workspace writers overlapped")
				}
			}
			write(filepath.Join(barrierDir, "release"), "", 0600)
			waitSuccess(first)
			waitSuccess(second)
			files, _ := filepath.Glob(filepath.Join(barrierDir, "*"))
			for _, file := range files {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	write(planPath, p, 0600)
	stop(cmd, logs)
	cmd, logs = start()
	checkMCP()
	if result := check(id); !strings.Contains(result, "persisted overnight") {
		t.Fatalf("lost persisted result: %s", result)
	}
	// Timeout produces a recorded failed attempt and configured fallback success.
	p = strings.Replace(p, `"executor":"fake/slow"`, `"executor":"fake/timeout","fallbackExecutors":["fake/slow"],"retry":{"maxAttempts":2,"effect":"retryable"}`, 1)
	write(planPath, p, 0600)
	fallbackID := submit()
	waitSuccess(fallbackID)
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/runs/%s/attempts", base, fallbackID), nil)
	req.Header.Set("Authorization", "Bearer "+authConfig.Token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	attempts, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(attempts, []byte(`"state":"failed"`)) || !bytes.Contains(attempts, []byte(`"state":"succeeded"`)) {
		t.Fatalf("missing attempt lifecycle: %s", attempts)
	}
	stop(cmd, logs)
}

func TestServiceRestart(t *testing.T) {
	for _, capacity := range []int{0, 3} {
		t.Run(fmt.Sprint(capacity), func(t *testing.T) { testServiceRestartCapacity(t, capacity) })
	}
}

func testServiceRestartCapacity(t *testing.T, capacity int) {
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
		if filepath.Dir(path) == adapters {
			raw = readyFixtureAdapter(t, path, raw)
		}
		if err := os.WriteFile(path, []byte(raw), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(ui, "index.html"), "<html>UI</html>", 0600)
	startedPath := filepath.Join(root, "adapter-started")
	write(filepath.Join(adapters, "fake-slow"), fmt.Sprintf("#!/bin/sh\ntouch %q\nprintf '%%s' '{\"outputs\":{\"result\":\"ok\"}}'\n", startedPath), 0700)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	runtimeDir := filepath.Join(root, "runtime")
	mcpPort, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mcpAddr := mcpPort.Addr().String()
	mcpPort.Close()
	config := serviceConfig{WorkerCapacity: capacity, RuntimeDir: runtimeDir, Addr: addr, Adapters: adapters, UI: ui, Workspace: workspace, Capabilities: []string{"fake/slow"}, MCP: &serviceMCPConfig{Listen: mcpAddr}}
	raw, _ := json.Marshal(config)
	configPath := filepath.Join(root, "config.json")
	write(configPath, string(raw), 0600)
	pidPath := filepath.Join(runtimeDir, "service.pid")
	env := append(os.Environ(), "MUXLIO_TEST_CLI=1", "MUXLIO_CONFIG="+configPath, "MUXLIO_HOME="+runtimeDir)
	client := &http.Client{Timeout: time.Second}
	base := "http://" + addr

	runCLI := func(subcmd string, args ...string) ([]byte, error) {
		t.Helper()
		argv := append([]string{os.Args[0], "-test.run=TestCLIProcess", "--", "service", subcmd, "--config", configPath}, args...)
		cli := exec.Command(argv[0], argv[1:]...)
		cli.Dir = root
		cli.Env = env
		return cli.CombinedOutput()
	}
	waitHealthy := func() {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			resp, e := client.Get(base + "/healthz")
			if e == nil {
				resp.Body.Close()
				if resp.StatusCode == 200 {
					req, _ := http.NewRequest(http.MethodPost, "http://"+mcpAddr+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
					req.Header.Set("Authorization", "Bearer "+readConfiguredAuthToken(configPath))
					req.Header.Set("Accept", "application/json, text/event-stream")
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("MCP-Protocol-Version", "2025-11-25")
					mcpResp, err := client.Do(req)
					if err == nil {
						mcpResp.Body.Close()
						if mcpResp.StatusCode == 200 {
							req, _ := http.NewRequest(http.MethodGet, base+"/v1/workers", nil)
							req.Header.Set("Authorization", "Bearer "+readConfiguredAuthToken(configPath))
							roster, err := client.Do(req)
							if err == nil {
								var data struct {
									Workers []struct {
										ID       string `json:"workerId"`
										Capacity int    `json:"capacity"`
									} `json:"registeredWorkers"`
								}
								err = json.NewDecoder(roster.Body).Decode(&data)
								roster.Body.Close()
								if err == nil && len(data.Workers) == 1 && data.Workers[0].ID == "local" && data.Workers[0].Capacity == capacity {
									return
								}
							}
						}
					}
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatal("service failed to become healthy")
	}
	t.Cleanup(func() {
		stop := exec.Command(os.Args[0], "-test.run=TestCLIProcess", "--", "service", "stop", "--config", configPath)
		stop.Dir = root
		stop.Env = env
		stop.Run()
	})

	out, err := runCLI("restart")
	if err != nil {
		t.Fatalf("service restart (cold start): %v: %s", err, out)
	}
	if !bytes.Contains(out, []byte("service started")) {
		t.Fatalf("restart should start service when none running: %s", out)
	}
	waitHealthy()
	firstPID, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	oldPID, err := strconv.Atoi(strings.TrimSpace(string(firstPID)))
	if err != nil {
		t.Fatal(err)
	}

	out, err = runCLI("restart")
	if err != nil {
		t.Fatalf("service restart (warm): %v: %s", err, out)
	}
	if !bytes.Contains(out, []byte("service started")) {
		t.Fatalf("restart should start replacement service: %s", out)
	}
	waitHealthy()
	secondRaw, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	newPID, err := strconv.Atoi(strings.TrimSpace(string(secondRaw)))
	if err != nil {
		t.Fatal(err)
	}
	if newPID == oldPID {
		t.Fatalf("restart should start a new process (old=%d new=%d)", oldPID, newPID)
	}
	if capacity == 0 {
		// A drained service must retain submitted work until capacity is raised.
		planPath, inputPath := filepath.Join(root, "plan.json"), filepath.Join(root, "input.json")
		write(planPath, `{"irVersion":"1.0","name":"drained","root":"root","subgraphs":{"root":{"nodes":[{"id":"work","kind":"execute","executor":"fake/slow","workspaceMode":"empty","outputs":{"result":{}}}]}}}`, 0600)
		write(inputPath, `{}`, 0600)
		submit := exec.Command(os.Args[0], "-test.run=TestCLIProcess", "--", "submit", "--server", base, planPath, inputPath)
		submit.Dir, submit.Env = root, env
		out, err := submit.CombinedOutput()
		if err != nil {
			t.Fatalf("submit while drained: %v: %s", err, out)
		}
		var result struct {
			RunID string `json:"runId"`
		}
		if err := json.Unmarshal(out, &result); err != nil || result.RunID == "" {
			t.Fatalf("submit output: %s (%v)", out, err)
		}
		attempts := func() []byte {
			t.Helper()
			req, _ := http.NewRequest(http.MethodGet, base+"/v1/runs/"+result.RunID+"/attempts", nil)
			req.Header.Set("Authorization", "Bearer "+readConfiguredAuthToken(configPath))
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("attempts: status=%d body=%s err=%v", resp.StatusCode, body, err)
			}
			return body
		}
		time.Sleep(1500 * time.Millisecond) // Multiple assignment poll opportunities.
		if _, err := os.Stat(startedPath); !os.IsNotExist(err) {
			t.Fatalf("drained worker started an adapter: %v", err)
		}
		// Preserve generated credentials when changing the operator's setting.
		raw, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		capacity = 3
		fields["workerCapacity"] = json.RawMessage(`3`)
		raw, err = json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		write(configPath, string(raw), 0600)
		if out, err := runCLI("restart"); err != nil {
			t.Fatalf("restart after raising capacity: %v: %s", err, out)
		}
		waitHealthy()
		deadline := time.Now().Add(8 * time.Second)
		for !bytes.Contains(attempts(), []byte(`"state":"succeeded"`)) {
			if time.Now().After(deadline) {
				t.Fatal("queued work did not finish after raising capacity")
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	oldProc, _ := os.FindProcess(oldPID)
	if oldProc != nil && oldProc.Signal(syscall.Signal(0)) == nil {
		// The process may briefly remain as a zombie after it has released its
		// service lock. The lock, rather than PID liveness, is the lifecycle
		// signal used to prevent overlapping services.
		lock, lockErr := os.OpenFile(filepath.Join(runtimeDir, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if lockErr != nil {
			t.Fatal(lockErr)
		}
		lockErr = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if lockErr == nil {
			_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		} else if lockErr == syscall.EWOULDBLOCK || lockErr == syscall.EAGAIN {
			lock.Close()
			t.Fatalf("old service process %d still holds the service lock after restart", oldPID)
		}
		lock.Close()
	}

	reject := exec.Command(os.Args[0], "-test.run=TestCLIProcess", "--", "service", "reload", "--config", configPath)
	reject.Dir = root
	reject.Env = env
	rejectOut, _ := reject.CombinedOutput()
	if !bytes.Contains(rejectOut, []byte("start|stop|restart")) {
		t.Fatalf("unknown service subcommand should mention restart: %s", rejectOut)
	}
}

func TestServiceMCPStartupFailureReleasesCoordinator(t *testing.T) {
	t.Setenv("MUXLIO_TOKEN", "coordinator-secret")
	t.Setenv("MUXLIO_WORKER_TOKEN", "worker-secret")
	t.Setenv("MUXLIO_MCP_HTTP_TOKEN", "")
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	spare, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := spare.Addr().String()
	spare.Close()
	for _, tc := range []struct {
		name string
		mcp  serviceMCPConfig
	}{
		{"port conflict", serviceMCPConfig{Listen: occupied.Addr().String()}},
		{"missing token", serviceMCPConfig{Listen: "127.0.0.1:0", TokenFile: filepath.Join(t.TempDir(), "missing")}},
		{"unpaired TLS", serviceMCPConfig{Listen: "127.0.0.1:0", TLSCert: "missing"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "data"), 0700); err != nil {
				t.Fatal(err)
			}
			c := serviceConfig{RuntimeDir: root, Addr: addr, Workspace: root, MCP: &tc.mcp}
			if code := serve(root, addr, false, &c); code != 1 {
				t.Fatalf("startup succeeded despite invalid MCP: %d", code)
			}
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				t.Fatalf("coordinator listener leaked after MCP startup failure: %v", err)
			}
			ln.Close()
		})
	}
}

func TestServiceMCPAddressAndSetupCredentials(t *testing.T) {
	for _, tc := range []struct {
		name, host string
		tokenFile  bool
	}{
		{"IPv6 coordinator", "::1", false},
		{"separate runtime", "127.0.0.1", false},
		{"relative worker token file", "127.0.0.1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, workspace, runtimeDir := t.TempDir(), t.TempDir(), t.TempDir()
			if code := cmdInit([]string{"--data-dir", root, "--workspace", workspace, "--json"}, "", true); code != 0 {
				t.Fatalf("init returned %d", code)
			}
			configPath := filepath.Join(root, "config.json")
			clientToken, workerToken := readConfiguredAuthToken(configPath), readConfiguredWorkerToken(configPath)
			reserve := func(host string) string {
				t.Helper()
				ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
				if err != nil {
					t.Fatal(err)
				}
				defer ln.Close()
				return ln.Addr().String()
			}
			addr, mcpAddr := reserve(tc.host), reserve("127.0.0.1")
			adapters := filepath.Join(root, "adapters")
			if err := os.MkdirAll(adapters, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(adapters, "fake-idle"), []byte("#!/bin/sh\nprintf '{}'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			var cfg map[string]any
			if err := json.Unmarshal(raw, &cfg); err != nil {
				t.Fatal(err)
			}
			cfg["runtimeDir"], cfg["addr"], cfg["adapters"] = runtimeDir, addr, adapters
			cfg["capabilities"] = []string{"fake/idle"}
			cfg["mcp"] = map[string]string{"listen": mcpAddr}
			if tc.tokenFile {
				delete(cfg, "workerToken")
				cfg["workerTokenFile"] = "worker-token"
				if err := os.WriteFile(filepath.Join(root, "worker-token"), []byte(workerToken), 0600); err != nil {
					t.Fatal(err)
				}
			}
			raw, err = json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configPath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			log, err := os.Create(filepath.Join(root, "service.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd := exec.Command(os.Args[0], "-test.run=TestCLIProcess", "--", "service", "start", "--config", configPath)
			cmd.Env = append(os.Environ(), "MUXLIO_TEST_CLI=1", "MUXLIO_DAEMON=0", "MUXLIO_CONFIG="+configPath, "MUXLIO_HOME="+runtimeDir, "MUXLIO_TOKEN=", "MUXLIO_WORKER_TOKEN=", "MUXLIO_MCP_HTTP_TOKEN=", "MUXLIO_ADDR=")
			cmd.Stdout, cmd.Stderr = log, log
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			t.Cleanup(func() {
				_ = cmd.Process.Signal(syscall.SIGTERM)
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					_ = cmd.Process.Kill()
					<-done
				}
			})
			client := &http.Client{Timeout: time.Second}
			base := "http://" + addr
			healthy := false
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				resp, err := client.Get(base + "/healthz")
				if err == nil {
					resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						healthy = true
						break
					}
				}
			}
			if !healthy {
				t.Fatal("service did not become healthy")
			}
			req, _ := http.NewRequest(http.MethodPost, "http://"+mcpAddr+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_runs","arguments":{}}}`))
			req.Header.Set("Authorization", "Bearer "+clientToken)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("MCP-Protocol-Version", "2025-11-25")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var rpc struct {
				Result *struct {
					IsError bool `json:"isError"`
				}
				Error any
			}
			if err := json.Unmarshal(body, &rpc); err != nil || resp.StatusCode != 200 || rpc.Error != nil || rpc.Result == nil || rpc.Result.IsError {
				t.Fatalf("MCP could not reach its coordinator: %d %s (%v)", resp.StatusCode, body, err)
			}
			req, _ = http.NewRequest(http.MethodPost, base+"/v1/workers/register", strings.NewReader(`{"protocolVersion":"v1","workerId":"setup-credential-check","capacity":1,"capabilities":[{"name":"fake/idle"}]}`))
			req.Header.Set("Authorization", "Bearer "+workerToken)
			req.Header.Set("Content-Type", "application/json")
			resp, err = client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("worker credential from init was rejected: %d %s", resp.StatusCode, body)
			}
			if _, err := os.Stat(filepath.Join(runtimeDir, "config.json")); !os.IsNotExist(err) {
				t.Fatalf("service created another credential config in runtimeDir: %v", err)
			}
		})
	}
}
