package conformance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/adapter"
)

// Exercise the adapter boundary with Git-free workspaces and separate output
// directories. Provider launch requirements belong here, outside worker tests.
func TestAgentSnapshotWorkspaces(t *testing.T) {
	launch := map[string]struct {
		flags      []string
		outputFlag string
	}{
		"agent/codex":       {[]string{"--json", "--skip-git-repo-check"}, "--add-dir"},
		"agent/claude":      {[]string{"--print", "--verbose"}, "--add-dir"},
		"agent/cursor":      {[]string{"--print", "--trust"}, "--add-dir"},
		"agent/antigravity": {[]string{"--dangerously-skip-permissions"}, "--add-dir"},
		"agent/kiro":        {[]string{"--no-interactive", "--trust-workspace", "--trust-all-tools"}, ""},
	}
	for _, fixture := range bundledAdapters {
		policy, ok := launch[fixture.Executor]
		if !ok {
			continue // ACP and A2A have transport-specific lifecycle fixtures.
		}
		t.Run(fixture.Executor, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), fixture.Binary)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/"+fixture.Binary)
			build.Dir = "../.."
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build: %v\n%s", err, out)
			}
			for _, selection := range []string{"default", "input-string", "input-artifact"} {
				t.Run(selection, func(t *testing.T) {
					root := t.TempDir()
					workspace := filepath.Join(root, "source workspace")
					selected := workspace
					if selection != "default" {
						selected = filepath.Join(root, "selected snapshot")
					}
					scratch, fakeBin := filepath.Join(root, "attempt scratch"), filepath.Join(root, "bin")
					for _, dir := range []string{workspace, selected, scratch, fakeBin} {
						if err := os.MkdirAll(dir, 0o700); err != nil {
							t.Fatal(err)
						}
					}
					// Match the physical paths returned by WorkingDir on macOS.
					selected, err := filepath.EvalSymlinks(selected)
					if err != nil {
						t.Fatal(err)
					}
					quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
					script := "#!/bin/sh\nset -eu\nif [ \"$1\" = --version ]; then echo '" + fixture.RunnerVersion + "'; exit 0; fi\n"
					script += "[ \"$PWD\" = " + quote(selected) + " ] || { echo 'wrong working directory' >&2; exit 1; }\n"
					script += "[ ! -e .git ] || exit 1\n"
					for _, flag := range policy.flags {
						script += "found=false\nfor arg in \"$@\"; do [ \"$arg\" != " + quote(flag) + " ] || found=true; done\n"
						script += "$found || { echo " + quote("missing launch option "+flag) + " >&2; exit 1; }\n"
					}
					if policy.outputFlag != "" {
						script += "found=false\nprevious=''\nfor arg in \"$@\"; do\n"
						script += "if [ \"$previous\" = " + quote(policy.outputFlag) + " ] && [ \"$arg\" = " + quote(scratch) + " ]; then found=true; fi\nprevious=$arg\ndone\n"
						script += "$found || { echo 'output directory was not granted' >&2; exit 1; }\n"
					}
					script += "printf 'report' > " + quote(filepath.Join(scratch, "report.txt")) + "\n"
					script += "printf 'true' > " + quote(filepath.Join(scratch, "passed")) + "\n"
					script += "printf '%s\\n' " + quote(fixture.SuccessStream) + "\n"
					cli := filepath.Join(fakeBin, fixture.CLIName)
					if err := os.WriteFile(cli, []byte(script), 0o700); err != nil {
						t.Fatal(err)
					}
					req := adapter.Request{Authorization: fixture.Authorization, Workspace: workspace, WorkDir: scratch, Executor: fixture.Executor, Task: "check snapshot", HostExecutionAllowed: true,
						Outputs: map[string]adapter.OutputDecl{"report": {Path: "report.txt", Mime: "text/plain"}, "passed": {Mime: "application/json"}}}
					if selection == "input-string" {
						req.Inputs = map[string]any{"workspace": selected}
					} else if selection == "input-artifact" {
						req.Inputs = map[string]any{"workspace": map[string]any{"data": selected}}
					}
					input, err := json.Marshal(req)
					if err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					cmd := exec.CommandContext(ctx, binary)
					cmd.Env = []string{"PATH=" + fakeBin, "HOME=" + root}
					cmd.Stdin = bytes.NewReader(input)
					raw, err := cmd.Output()
					if err != nil {
						t.Fatalf("adapter: %v\n%s", err, raw)
					}
					var result adapter.Response
					if err := json.Unmarshal(raw, &result); err != nil {
						t.Fatalf("invalid response: %v\n%s", err, raw)
					}
					artifact, ok := result.Outputs["report"].(map[string]any)
					if result.Error != "" || len(result.OutputErrors) != 0 || result.Outputs["passed"] != true || !ok || artifact["data"] != "report" {
						t.Fatalf("snapshot execution failed: %+v", result)
					}
				})
			}
		})
	}
}
