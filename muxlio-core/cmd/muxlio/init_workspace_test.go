package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func readInitWorkspace(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Workspace string `json:"workspace"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	return config.Workspace
}

func TestInitCLIHonorsExplicitWorkspaceAndPreservesItOnRepeat(t *testing.T) {
	root, first, second := t.TempDir(), t.TempDir(), t.TempDir()
	call := func(args ...string) {
		t.Helper()
		argv := append([]string{"-test.run=^TestCLIProcess$", "--", "init", "--data-dir", root, "--json"}, args...)
		cmd := exec.Command(os.Args[0], argv...)
		cmd.Env = append(os.Environ(), "MUXLIO_TEST_CLI=1", "MUXLIO_HOME="+root, "PATH=/usr/bin:/bin")
		cmd.Dir = t.TempDir()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("init: %v\n%s", err, out)
		}
	}
	call("--workspace", first)
	if got := readInitWorkspace(t, root); got != first {
		t.Fatalf("first explicit workspace ignored: %q", got)
	}
	call("--workspace", second)
	if got := readInitWorkspace(t, root); got != second {
		t.Fatalf("workspace update ignored: %q", got)
	}
	call()
	if got := readInitWorkspace(t, root); got != second {
		t.Fatalf("repeat reset workspace: %q", got)
	}
}

func TestSetupUsesSourceWorkspaceAndPreservesOperatorSelection(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required for setup")
	}
	root := t.TempDir()
	repo, runtime := filepath.Join(root, "repo with spaces"), filepath.Join(root, "runtime")
	scripts := filepath.Join(repo, "scripts")
	if err := os.MkdirAll(scripts, 0700); err != nil {
		t.Fatal(err)
	}
	setup, err := os.ReadFile("../../../scripts/setup.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scripts, "setup.sh"), setup, 0700); err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	shim := filepath.Join(root, "fixture-cli")
	body := "#!/bin/sh\nexec " + quote(os.Args[0]) + " -test.run=^TestCLIProcess$ -- \"$@\"\n"
	if err := os.WriteFile(shim, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	// Replace only the expensive build. Setup still invokes the real CLI entry
	// point, so swallowed --workspace flags cannot pass this regression test.
	body = "#!/bin/sh\nset -eu\nmkdir -p \"$1/bin\"\ncp \"$MUXLIO_SETUP_TEST_CLI\" \"$1/bin/muxlio\"\n"
	if err := os.WriteFile(filepath.Join(scripts, "build-release.sh"), []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	call := func(workspace string) {
		t.Helper()
		cmd := exec.Command("bash", filepath.Join(scripts, "setup.sh"))
		cmd.Env = append(os.Environ(), "MUXLIO_TEST_CLI=1", "MUXLIO_HOME="+runtime, "MUXLIO_WORKSPACE="+workspace, "MUXLIO_SETUP_TEST_CLI="+shim, "PATH=/usr/bin:/bin")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup: %v\n%s", err, out)
		}
	}
	call("")
	if got := readInitWorkspace(t, runtime); got != repo {
		t.Fatalf("setup selected an unusable workspace: %q", got)
	}
	custom := t.TempDir()
	call(custom)
	call("")
	if got := readInitWorkspace(t, runtime); got != custom {
		t.Fatalf("setup lost operator selection: %q", got)
	}
}
