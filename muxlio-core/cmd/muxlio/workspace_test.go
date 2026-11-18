package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func workspaceCLI(t *testing.T, wantCode int, args ...string) string {
	t.Helper()
	cmd := workspaceProcess(t, args...)
	out, err := cmd.CombinedOutput()
	checkWorkspaceExit(t, args, wantCode, out, err)
	return string(out)
}

func workspaceProcess(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, append([]string{"-test.run=^TestCLIProcess$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "MUXLIO_TEST_CLI=1")
	return cmd
}

func checkWorkspaceExit(t *testing.T, args []string, wantCode int, out []byte, err error) {
	t.Helper()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatal(err)
		}
		code = exitErr.ExitCode()
	}
	if code != wantCode {
		t.Fatalf("%v exit = %d, want %d\n%s", args, code, wantCode, out)
	}
}

func TestWorkspaceRegistryLifecycle(t *testing.T) {
	root, local := t.TempDir(), t.TempDir()
	t.Setenv("MUXLIO_HOME", t.TempDir())
	t.Setenv("MUXLIO_CONFIG", "")
	path := filepath.Join(root, "config.json")
	original := `{
		"workspace": "/legacy/default",
		"token": "client-secret",
		"workerToken": "worker-secret",
		"mcp": {"listen": "127.0.0.1:7601", "tokenFile": "mcp-token"},
		"executors": {"command": {"env": {"EXAMPLE_SECRET": "executor-secret"}}},
		"other": {"number": 9007199254740993, "nested": {"secret": "<value>"}},
		"workspaces": {"remote": {"workerId": "worker-2", "path": "/worker/volume", "unrecognized": true}}
	}`
	writeWorkspaceConfig(t, path, original)
	symlink := filepath.Join(root, "linked")
	if err := os.Symlink(local, symlink); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(local)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.TrimSpace(workspaceCLI(t, 0, "workspace", "digest", symlink))
	out := workspaceCLI(t, 0, "--data-dir", root, "workspace", "add", "volume", symlink, "--worker-id=local", "--alias", "alias", "--digest", digest, "--alias=secondary")
	if !strings.Contains(out, "Restart the coordinator and owning worker") {
		t.Fatalf("add output = %q", out)
	}
	list := workspaceCLI(t, 0, "workspace", "list", "--data-dir="+root, "--json")
	var entries []workspaceEntry
	if err := json.Unmarshal([]byte(list), &entries); err != nil || len(entries) != 2 || entries[0].ID != "remote" || entries[1].ID != "volume" {
		t.Fatalf("list output = %q, err = %v", list, err)
	}
	t.Setenv("MUXLIO_HOME", root)
	show := workspaceCLI(t, 0, "--json", "workspace", "show", "alias")
	var entry workspaceEntry
	if err := json.Unmarshal([]byte(show), &entry); err != nil || entry.ID != "volume" || entry.Path != canonical ||
		entry.WorkerID != "local" || entry.Digest != digest || !reflect.DeepEqual(entry.Aliases, []string{"alias", "secondary"}) {
		t.Fatalf("show output = %q, err = %v", show, err)
	}
	if !reflect.DeepEqual(entry, entries[1]) || strings.Contains(list+show, "secret") || strings.Contains(list+show, "/legacy/default") {
		t.Fatalf("unexpected registry output: %s%s", list, show)
	}
	if out := workspaceCLI(t, 0, "workspace", "show", "volume"); !strings.Contains(out, canonical) || !strings.Contains(out, "worker=") {
		t.Fatalf("human output = %q", out)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("config permissions: %v, err: %v", info, err)
	}
	out = workspaceCLI(t, 0, "workspace", "remove", "secondary")
	if !strings.Contains(out, `Removed workspace "volume"`) || !strings.Contains(out, "Restart the coordinator and owning worker") {
		t.Fatalf("remove output = %q", out)
	}
	workspaceCLI(t, 1, "workspace", "show", "alias")
	workspaceCLI(t, 1, "workspace", "remove", "default")
	if !bytes.Equal(compactWorkspaceConfig(t, []byte(original)), compactWorkspaceConfig(t, readWorkspaceConfig(t, path))) {
		t.Fatal("unrelated fields, default workspace, or existing binding changed")
	}
}

func TestWorkspaceRegistryValidation(t *testing.T) {
	root, local, other := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("MUXLIO_HOME", root)
	t.Setenv("MUXLIO_CONFIG", "")
	path := filepath.Join(root, "config.json")
	linked := filepath.Join(root, "linked")
	if err := os.Symlink(local, linked); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file")
	writeWorkspaceConfig(t, file, "")
	writeWorkspaceConfig(t, path, `{"workspaces":{"first":{"workerId":"local","path":`+jsonString(t, linked)+`,"aliases":["alias"]}}}`)
	before := readWorkspaceConfig(t, path)
	cases := []struct {
		name   string
		args   []string
		reason string
	}{
		{"existing ID", []string{"first", other, "--worker-id", "local"}, "already exists"},
		{"alias as ID", []string{"alias", other, "--worker-id", "local"}, "duplicate workspace identity"},
		{"existing ID as alias", []string{"second", other, "--worker-id", "local", "--alias", "first"}, "duplicate workspace identity"},
		{"duplicate alias in entry", []string{"second", other, "--worker-id", "local", "--alias", "same", "--alias", "same"}, "duplicate workspace identity"},
		{"duplicate canonical directory", []string{"second", local, "--worker-id", "local"}, "same directory"},
		{"relative local", []string{"second", "relative", "--worker-id", "local"}, "absolute path"},
		{"relative remote", []string{"second", "relative", "--worker-id", "worker-2"}, "absolute path"},
		{"missing local", []string{"second", filepath.Join(root, "missing"), "--worker-id", "local"}, "unavailable"},
		{"local regular file", []string{"second", file, "--worker-id", "local"}, "not a directory"},
		{"bad worker", []string{"second", local, "--worker-id", "host/name"}, "workerId"},
		{"dot worker", []string{"second", local, "--worker-id", ".."}, "workerId"},
		{"bad digest", []string{"second", local, "--worker-id", "worker-2", "--digest", "sha256:bad"}, "SHA-256"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := workspaceCLI(t, 1, append([]string{"workspace", "add"}, tc.args...)...)
			if !strings.Contains(out, tc.reason) || !bytes.Equal(readWorkspaceConfig(t, path), before) {
				t.Fatalf("failure did not preserve config or explain reason: %s", out)
			}
		})
	}
	workspaceCLI(t, 2, "workspace", "add", "second", other)
	workspaceCLI(t, 2, "workspace", "add", "second", other, "--worker-id", "local", "--digest=")
}

func TestWorkspaceRegistryRemoteOwnerAndConfigOverride(t *testing.T) {
	root, override := t.TempDir(), t.TempDir()
	t.Setenv("MUXLIO_HOME", root)
	path := filepath.Join(override, "operator.json")
	t.Setenv("MUXLIO_CONFIG", path)
	writeWorkspaceConfig(t, path, `{"workspace":"/existing/default"}`)
	defaultPath := filepath.Join(root, "config.json")
	writeWorkspaceConfig(t, defaultPath, `{"workspaces":{}}`)
	remote := filepath.Join(root, "unavailable-locally", "worker-volume")
	flags := []string{"--worker-id", "node_2@site:1"}
	out := workspaceCLI(t, 0, append([]string{"workspace", "add", "volume", remote, "--data-dir", root}, flags...)...)
	for _, msg := range []string{"local config", "validates its directory at startup", "same binding in both the coordinator and worker configs", "Restart the coordinator and owning worker"} {
		if !strings.Contains(out, msg) {
			t.Fatalf("add output omits %q: %s", msg, out)
		}
	}
	workspaceCLI(t, 1, append([]string{"workspace", "add", "duplicate", remote}, flags...)...)
	workspaceCLI(t, 0, "workspace", "add", "independent", remote, "--worker-id", "other-node")
	show := workspaceCLI(t, 0, "workspace", "show", "volume", "--json")
	var entry workspaceEntry
	if err := json.Unmarshal([]byte(show), &entry); err != nil || entry.Path != remote || entry.WorkerID != flags[1] {
		t.Fatalf("remote binding output = %q, err = %v", show, err)
	}
	out = workspaceCLI(t, 0, "workspace", "remove", "volume")
	if !strings.Contains(out, "remove the same binding from both") {
		t.Fatalf("remove output = %q", out)
	}
	if string(readWorkspaceConfig(t, defaultPath)) != `{"workspaces":{}}` {
		t.Fatal("MUXLIO_CONFIG update changed the runtime-directory config")
	}
	t.Setenv("MUXLIO_CONFIG", "")
	if out := workspaceCLI(t, 0, "workspace", "list", "--json"); strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty list = %q", out)
	}
}

func TestWorkspaceRegistryPreservesMalformedConfig(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MUXLIO_HOME", root)
	t.Setenv("MUXLIO_CONFIG", "")
	path := filepath.Join(root, "config.json")
	for _, raw := range []string{
		`{"workspaces":`, `null`, `[]`, `{"workspaces":[]}`, `{"workerCapacity":null}`,
		`{"workspaces":{},"Workspaces":{}}`, `{"token":"one","token":"two"}`,
		`{"workspaces":{"one":{"workerId":"other","path":"/foo"},"one":{"workerId":"other","path":"/bar"}}}`,
		`{"workspaces":{"one":{"workerId":"other","path":"relative"}}}`,
	} {
		writeWorkspaceConfig(t, path, raw)
		workspaceCLI(t, 1, "workspace", "add", "new", "/worker/data", "--worker-id", "remote")
		if got := string(readWorkspaceConfig(t, path)); got != raw {
			t.Fatalf("malformed config %q replaced with %q", raw, got)
		}
	}
}

func TestWorkspaceRegistryPreservesConfigOnWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("superuser bypasses directory write permissions")
	}
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	t.Setenv("MUXLIO_CONFIG", path)
	before := `{"token":"secret","workspaces":{}}`
	writeWorkspaceConfig(t, path, before)
	if err := os.Chmod(root, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(root, 0700); err != nil {
			t.Error(err)
		}
	})
	out := workspaceCLI(t, 1, "workspace", "add", "volume", "/worker/data", "--worker-id", "remote")
	if !strings.Contains(out, "writing config") || string(readWorkspaceConfig(t, path)) != before {
		t.Fatalf("config changed after failed write: %s", out)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Fatalf("unexpected files after failure: %v, err: %v", entries, err)
	}
}

func TestWorkspaceConfigCleansUpFailedReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(path, "existing")
	writeWorkspaceConfig(t, sentinel, "kept")
	if err := writeRuntimeConfig(path, []byte(`{"workspaces":{}}`)); err == nil {
		t.Fatal("expected replacement of a directory to fail")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() || string(readWorkspaceConfig(t, sentinel)) != "kept" {
		t.Fatalf("failed replacement changed destination or left a temporary file: %v, err: %v", entries, err)
	}
}

func TestWorkspaceRegistryConcurrentEdits(t *testing.T) {
	t.Setenv("MUXLIO_HOME", t.TempDir())
	t.Setenv("MUXLIO_CONFIG", "")
	a := []string{"workspace", "add", "a", "/worker/a", "--worker-id", "remote"}
	b := []string{"workspace", "add", "b", "/worker/b", "--worker-id", "remote"}
	first := workspaceProcess(t, a...)
	var out bytes.Buffer
	first.Stdout, first.Stderr = &out, &out
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	secondOut, secondErr := workspaceProcess(t, b...).CombinedOutput()
	firstErr := first.Wait()
	checkWorkspaceExit(t, a, 0, out.Bytes(), firstErr)
	checkWorkspaceExit(t, b, 0, secondOut, secondErr)
	list := workspaceCLI(t, 0, "workspace", "list", "--json")
	var entries []workspaceEntry
	if err := json.Unmarshal([]byte(list), &entries); err != nil || len(entries) != 2 || entries[0].ID != "a" || entries[1].ID != "b" {
		t.Fatalf("list after concurrent edits = %q, err = %v", list, err)
	}
}

func writeWorkspaceConfig(t *testing.T, path, raw string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
}

func readWorkspaceConfig(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func compactWorkspaceConfig(t *testing.T, raw []byte) []byte {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func jsonString(t *testing.T, s string) string {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
