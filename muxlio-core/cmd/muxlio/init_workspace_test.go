package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/integrations"
)

func TestSkillSourcePrefersDistributionOverWorkspace(t *testing.T) {
	root, ws := t.TempDir(), t.TempDir()
	distributed := filepath.Join(root, "share", "muxlio", "skills")
	for _, dir := range []string{distributed, filepath.Join(ws, "muxlio-skills"), filepath.Join(ws, "muxlio-core")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(distributed, "SKILL.md"), filepath.Join(ws, "muxlio-skills", "SKILL.md")} {
		if err := os.WriteFile(path, []byte("---\nname: muxlio\ndescription: test\n---\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Even a checkout-shaped target must not shadow the installed distribution.
	if err := os.WriteFile(filepath.Join(ws, "muxlio-core", "go.mod"), []byte("module github.com/muxlio/muxlio-core\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(ws)
	if got := getSkillDir(root); got != distributed {
		t.Fatalf("workspace shadowed distributed skill: %s", got)
	}
	if err := os.Remove(filepath.Join(distributed, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if got := getSkillDir(root); got != filepath.Join(ws, "muxlio-skills") {
		t.Fatalf("development checkout lookup broken: %s", got)
	}
	if err := os.Remove(filepath.Join(ws, "muxlio-core", "go.mod")); err != nil {
		t.Fatal(err)
	}
	if got := getSkillDir(root); got != "" {
		t.Fatalf("unrelated workspace accepted as a distribution: %s", got)
	}
}

func TestInitInstallsDistributedSkillOutsideCheckout(t *testing.T) {
	source, err := filepath.Abs("../../../muxlio-skills")
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range []string{"claude", "codex", "kiro", "cursor"} {
		for _, scope := range []string{"project", "user"} {
			t.Run(client+"/"+scope, func(t *testing.T) {
				root, ws, home := t.TempDir(), t.TempDir(), t.TempDir()
				if _, err := integrations.InstallSkill(source, filepath.Join(root, "share", "muxlio", "skills")); err != nil {
					t.Fatal(err)
				}
				bin := t.TempDir()
				bridge := filepath.Join(bin, "muxlio-mcp")
				if err := os.WriteFile(bridge, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(bin, "cursor"), []byte("#!/bin/sh\necho 2.4.0\n"), 0700); err != nil {
					t.Fatal(err)
				}
				call := func() {
					t.Helper()
					args := []string{"-test.run=^TestCLIProcess$", "--", "init", "--data-dir", root, "--workspace", ws, "--client", client, "--scope", scope, "--mcp-binary", bridge, "--json"}
					cmd := exec.Command(os.Args[0], args...)
					cmd.Env = append(os.Environ(), "MUXLIO_TEST_CLI=1", "HOME="+home, "CODEX_HOME=", "PATH="+bin)
					cmd.Dir = ws
					out, err := cmd.CombinedOutput()
					if err != nil {
						t.Fatalf("init: %v\n%s", err, out)
					}
					var result struct{ Skills []string }
					if err := json.Unmarshal(out, &result); err != nil || len(result.Skills) < 8 {
						t.Fatalf("skill bundle not reported: %s %v", out, err)
					}
				}
				call()
				call()
				base := ws
				if scope == "user" {
					base = home
				}
				dir := map[string]string{"claude": ".claude", "codex": ".agents", "kiro": ".kiro", "cursor": ".cursor"}[client]
				for _, file := range []string{"SKILL.md", "references/authoring.md", "references/operations.md", "references/security.md"} {
					got, err := os.ReadFile(filepath.Join(base, dir, "skills", "muxlio", file))
					want, readErr := os.ReadFile(filepath.Join(source, file))
					if err != nil || readErr != nil || string(got) != string(want) {
						t.Fatalf("installed bundle differs for %s: %v %v", file, err, readErr)
					}
				}
			})
		}
	}
}

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
