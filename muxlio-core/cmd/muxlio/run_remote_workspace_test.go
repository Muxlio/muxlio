package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

const workspaceRunPlan = `{
  "irVersion":"1.0","name":"shared","root":"root",
  "inputSchema":{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"],"additionalProperties":false},
  "subgraphs":{"root":{
    "nodes":[{"id":"read","kind":"execute","executor":"test","workspaceMode":"snapshot","outputs":{"value":{}}}],
    "output":{"value":{"expr":{"op":"ref","node":"read","port":"value"}}}
  }}
}`

func TestRunRemoteWorkspaceSelection(t *testing.T) {
	dir := t.TempDir()
	write := func(path, body string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	first, second, prepared := t.TempDir(), t.TempDir(), t.TempDir()
	for id, workspace := range map[string]string{"first": first, "second": second, "prepared": prepared} {
		write(filepath.Join(workspace, "result.json"), fmt.Sprintf(`{"outputs":{"value":%q}}`, id), 0o600)
	}
	digest, err := workers.WorkspaceDigest(prepared)
	if err != nil {
		t.Fatal(err)
	}
	registry := workers.WorkspaceRegistry{
		"first":    {WorkerID: "owner", Path: first, Aliases: []string{"alias"}},
		"second":   {WorkerID: "owner", Path: second},
		"prepared": {WorkerID: "owner", Path: prepared, Digest: digest, Aliases: []string{"prepared-alias"}},
		"offline":  {WorkerID: "absent", Path: t.TempDir()},
	}
	store, err := sqlite.Open(filepath.Join(dir, "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := api.NewCoordinator(store)
	coordinator.Workspaces = registry
	runner := &serverRunner{store: store, exec: coordinator, engines: map[string]*plan.Engine{}, workers: map[string]chan struct{}{}, ctx: ctx}
	server := httptest.NewServer(api.WithAuth(api.NewRouterWithArtifacts(store, runner, nil, api.NewWorkerProtocolHandler(coordinator)), "client", "worker"))
	t.Cleanup(server.Close)

	adapters := t.TempDir()
	write(filepath.Join(adapters, "test"), "#!/bin/sh\n/bin/cat workspace/result.json\n", 0o700)
	write(filepath.Join(adapters, "test.json"), `{"manifestVersion":"1","executor":"test","capabilities":{}}`, 0o600)
	workBase := filepath.Join(dir, "work")
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- workers.RunRemoteWorker(ctx, workers.RemoteWorkerConfig{
			CoordinatorURL: server.URL, WorkerID: "owner", Token: "worker", Workspaces: registry,
			WorkBase: workBase, PollInterval: 20 * time.Millisecond, Capabilities: []workers.RemoteCapability{{Name: "test"}},
		}, workers.NewScriptExecutor(adapters, workBase))
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-workerDone; err != nil && err != context.Canceled {
			t.Errorf("worker: %v", err)
		}
		runner.wg.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		online, err := store.ListWorkers(time.Now().Add(-time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if len(online) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not register")
		}
		time.Sleep(20 * time.Millisecond)
	}

	config := fmt.Sprintf(`{"servers":{"test-profile":{"url":%q,"token":"client"}}}`, server.URL)
	configPath := filepath.Join(dir, "config.json")
	write(configPath, config, 0o600)
	tokenPath := filepath.Join(dir, "token")
	write(tokenPath, "client\n", 0o600)
	inputPath := filepath.Join(dir, "input.json")
	const input = `{"value":9007199254740993}`
	write(inputPath, input, 0o600)
	cli := func(t *testing.T, wantCode int, args ...string) (string, string) {
		t.Helper()
		cliCtx, stop := context.WithTimeout(ctx, 45*time.Second)
		defer stop()
		cmd := exec.CommandContext(cliCtx, os.Args[0], append([]string{"-test.run=^TestCLIProcess$", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "MUXLIO_TEST_CLI=1", "MUXLIO_CONFIG="+configPath, "MUXLIO_HOME="+dir, "MUXLIO_SERVER=", "MUXLIO_TOKEN=", "MUXLIO_WORKSPACE=", "OTEL_SDK_DISABLED=true")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		checkWorkspaceExit(t, args, wantCode, []byte(stdout.String()+stderr.String()), err)
		return stdout.String(), stderr.String()
	}
	type cliRunResult struct {
		RunID  string `json:"runId"`
		State  string `json:"state"`
		Output string `json:"output"`
	}
	assertRun := func(t *testing.T, want string, args ...string) {
		t.Helper()
		stdout, stderr := cli(t, 0, args...)
		var result cliRunResult
		if err := json.Unmarshal([]byte(stdout), &result); err != nil || result.State != "succeeded" || result.Output != want {
			t.Fatalf("terminal JSON: %s (%v)\n%s", stdout, err, stderr)
		}
		run, err := store.GetRun(result.RunID)
		if err != nil {
			t.Fatal(err)
		}
		var pinned plan.Plan
		if err := json.Unmarshal(run.PlanJSON, &pinned); err != nil {
			t.Fatal(err)
		}
		gotInput, err := json.Marshal(run.Input)
		if err != nil || string(gotInput) != input {
			t.Fatalf("persisted input = %s (%v)", gotInput, err)
		}
		if run.WorkspaceID != want || pinned.WorkspaceID != want || run.PlanDigest == "" || pinned.Digest != run.PlanDigest || pinned.VerifyDigest() != nil {
			t.Fatalf("persisted workspace/digest: workspace=%q plan workspace=%q digest=%q", run.WorkspaceID, pinned.WorkspaceID, run.PlanDigest)
		}
	}
	writePlan := func(name, id, revision string) string {
		t.Helper()
		var p plan.Plan
		if err := json.Unmarshal([]byte(workspaceRunPlan), &p); err != nil {
			t.Fatal(err)
		}
		p.WorkspaceID, p.WorkspaceDigest = id, revision
		if _, err := p.ComputeDigest(); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(&p)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name+".json")
		write(path, string(raw), 0o600)
		assertWorkspaceFileUnchanged(t, path, string(raw))
		return path
	}
	planPath := writePlan("plan", "", "")
	t.Run("compiled JSON", func(t *testing.T) {
		assertRun(t, "first", "run", planPath, inputPath, "--server", "test-profile", "--workspace-id", "alias", "--json")
		assertRun(t, "second", "--workspace-id=second", "--server", server.URL, "run", planPath, inputPath, "--token-file", tokenPath, "--json")
	})
	t.Run("TypeScript", func(t *testing.T) {
		if _, err := exec.LookPath("node"); err != nil {
			t.Skip("node unavailable")
		}
		root, err := filepath.Abs("../../..")
		if err != nil {
			t.Fatal(err)
		}
		modules := filepath.Join(root, "node_modules")
		if _, err := os.Stat(filepath.Join(modules, "tsx", "dist", "cli.mjs")); os.IsNotExist(err) {
			t.Skip("install the repository's pinned tsx to run the TypeScript integration")
		} else if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(modules, filepath.Join(dir, "node_modules")); err != nil {
			t.Fatal(err)
		}
		source := fmt.Sprintf("import { computeDigest } from %q;\nconst plan = %s;\nexport default { ...plan, digest: computeDigest(plan) };\n", filepath.Join(root, "muxlio-sdk/src/builder.ts"), workspaceRunPlan)
		path := filepath.Join(dir, "workflow.ts")
		write(path, source, 0o600)
		assertWorkspaceFileUnchanged(t, path, source)
		assertRun(t, "first", "run", "--workspace-id=first", path, inputPath, "--server=test-profile", "--json")
		assertRun(t, "second", "--workspace-id", "second", "--server", "test-profile", "--json", "run", path, inputPath)
	})

	bound := writePlan("bound", "first", "")
	assertRun(t, "first", "run", bound, inputPath, "--server", "test-profile", "--workspace-id=alias", "--json")
	pinned := writePlan("pinned", "prepared", digest)
	assertRun(t, "prepared", "run", pinned, inputPath, "--server", "test-profile", "--workspace-id", "prepared-alias", "--json")
	badPin := writePlan("bad-pin", "prepared", "sha256:"+strings.Repeat("0", 64))
	tampered := filepath.Join(dir, "tampered.json")
	original, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	write(tampered, strings.Replace(string(original), `"name":"shared"`, `"name":"tampered"`, 1), 0o600)
	wrongVersion := filepath.Join(dir, "wrong-version.json")
	write(wrongVersion, strings.Replace(workspaceRunPlan, `"1.0"`, `"2.0"`, 1), 0o600)

	before, err := store.ListRuns()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, selector, target, wantError string
	}{
		{"conflict", bound, "second", "test-profile", "submission and plan workspace IDs disagree"},
		{"unknown workspace", planPath, "unknown", "test-profile", "MUXLIO_WORKSPACE_UNAUTHORIZED"},
		{"offline owner", planPath, "offline", "test-profile", `no available owning worker`},
		{"workspace digest", badPin, "prepared-alias", "test-profile", "prepared digest does not match"},
		{"plan digest", tampered, "first", "test-profile", "plan digest mismatch"},
		{"IR version", wrongVersion, "first", "test-profile", "MUXLIO_UNSUPPORTED_IR_VERSION"},
		{"missing credential", planPath, "first", server.URL, "HTTP 401"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr := cli(t, 1, "run", tc.path, inputPath, "--server", tc.target, "--workspace-id", tc.selector, "--json")
			if stdout != "" || !strings.Contains(stderr, tc.wantError) {
				t.Fatalf("expected %q, stdout=%q stderr=%q", tc.wantError, stdout, stderr)
			}
		})
	}
	if _, stderr := cli(t, 1, "run", planPath, inputPath, "--server", "test-profile", "--workspace-id", "first", "--token", "denied"); !strings.Contains(stderr, "HTTP 401") {
		t.Fatalf("explicit credential did not take precedence over profile: %s", stderr)
	}
	invalidInput := filepath.Join(dir, "invalid-input.json")
	write(invalidInput, `{`, 0o600)
	if _, stderr := cli(t, 1, "run", planPath, invalidInput, "--server", "test-profile", "--workspace-id", "first"); !strings.Contains(stderr, "encoding run submission") {
		t.Fatalf("invalid JSON input: %s", stderr)
	}
	after, err := store.ListRuns()
	if err != nil || len(before) != len(after) {
		t.Fatalf("rejected submissions persisted runs: before=%d after=%d (%v)", len(before), len(after), err)
	}
	stdout, stderr := cli(t, 0, "run", planPath, inputPath, "--adapters", adapters, "--workspace", second, "--json")
	var local cliRunResult
	if err := json.Unmarshal([]byte(stdout), &local); err != nil || local.State != "succeeded" || local.Output != "second" {
		t.Fatalf("standalone workspace result: %s (%v)\n%s", stdout, err, stderr)
	}
	assertWorkspaceFileUnchanged(t, inputPath, input)
	assertWorkspaceFileUnchanged(t, configPath, config)
}

func TestRunRejectsMissingWorkspaceID(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"run", "plan.json", "--workspace-id", "first"},
		{"run", "plan.json", "--fake-config", "fake.json", "--workspace-id", "first"},
		{"run", "plan.json", "--server", "unused", "--workspace-id"},
		{"run", "plan.json", "--workspace-id", "--server", "unused"},
		{"run", "plan.json", "--server", "unused", "--workspace-id="},
		{"--workspace-id=", "run", "plan.json", "--server", "unused"},
		{"--workspace-id", "--json", "run", "plan.json", "--server", "unused"},
	} {
		out := workspaceCLI(t, 2, append([]string{"--data-dir", dir}, args...)...)
		if !strings.Contains(out, "--workspace-id requires") {
			t.Errorf("%v: %s", args, out)
		}
	}
	out := workspaceCLI(t, 0, "run", "--help")
	for _, option := range []string{"--server", "--workspace-id", "--workspace", "--json"} {
		if !strings.Contains(out, option) {
			t.Errorf("help is missing %q", option)
		}
	}
}

func assertWorkspaceFileUnchanged(t *testing.T, path, want string) {
	t.Helper()
	t.Cleanup(func() {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Errorf("%s changed: %s (%v)", path, got, err)
		}
	})
}
