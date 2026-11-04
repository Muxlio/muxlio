package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/adapter"
)

func commandResponse(t *testing.T, params map[string]any) (adapter.Response, string) {
	t.Helper()
	workspace, workDir := t.TempDir(), t.TempDir()
	input, err := os.CreateTemp(t.TempDir(), "request-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	req := map[string]any{"workspace": workspace, "workDir": workDir, "executor": "command", "params": params, "outputs": map[string]any{"passed": map[string]any{"mime": "application/json"}}}
	if err = json.NewEncoder(input).Encode(req); err != nil {
		t.Fatal(err)
	}
	_, _ = input.Seek(0, 0)
	oldIn, oldOut := os.Stdin, os.Stdout
	defer func() { os.Stdin = oldIn; os.Stdout = oldOut }()
	os.Stdin = input
	r, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	os.Stdout = outW
	main()
	_ = outW.Close()
	os.Stdout = oldOut
	var resp adapter.Response
	if err = json.NewDecoder(r).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return resp, workspace
}

func TestDirectArgvAndReturnedNonzero(t *testing.T) {
	arg := `space quote" dollar$ semi; backtick` + "`" + ` sub$(touch nope)`
	resp, workspace := commandResponse(t, map[string]any{"program": "/bin/echo", "args": []string{arg}, "onNonZero": "return", "timeout": "2s"})
	result := resp.Outputs
	if _, wrapped := result["_result"]; wrapped {
		t.Fatalf("unexpected wrapper output: %#v", resp)
	}
	if got := result["stdout"].(string); strings.TrimSpace(got) != arg {
		t.Fatalf("argv changed: %q", got)
	}
	if result["exitCode"] != float64(0) {
		t.Fatalf("exit code=%v", result["exitCode"])
	}
	if signal, exists := result["signal"]; !exists || signal != nil {
		t.Fatalf("expected null signal: %#v", result)
	}
	if result["passed"] != true {
		t.Fatalf("passed=%v", result["passed"])
	}
	if resp.Outputs["passed"] != true || resp.Error != "" {
		t.Fatalf("top-level passed/error: %#v", resp)
	}
	if _, err := os.Stat(filepath.Join(workspace, "nope")); !os.IsNotExist(err) {
		t.Fatalf("shell interpreted argument: %v", err)
	}
}

func TestTopLevelPassedForNonzeroExit(t *testing.T) {
	for _, policy := range []string{"return", "fail"} {
		t.Run(policy, func(t *testing.T) {
			resp, _ := commandResponse(t, map[string]any{"program": "/bin/sh", "args": []string{"-c", "exit 7"}, "onNonZero": policy})
			result := resp.Outputs
			if result["passed"] != false || result["exitCode"] != float64(7) {
				t.Fatalf("failed command outputs: %#v", resp)
			}
			if (resp.Error != "") != (policy == "fail") {
				t.Fatalf("policy %s: error=%q", policy, resp.Error)
			}
		})
	}
}

func TestFlatCommandDetails(t *testing.T) {
	t.Run("trimmed stdout", func(t *testing.T) {
		resp, _ := commandResponse(t, map[string]any{"program": "/bin/echo", "args": []string{"hello"}, "trimStdout": true})
		if resp.Error != "" || resp.Outputs["stdout"] != "hello" || resp.Outputs["stderr"] != "" {
			t.Fatalf("flat details: %#v", resp)
		}
		if _, ok := resp.Outputs["durationMs"].(float64); !ok {
			t.Fatalf("missing duration: %#v", resp.Outputs)
		}
	})
	t.Run("signal", func(t *testing.T) {
		resp, _ := commandResponse(t, map[string]any{"program": "/bin/sh", "args": []string{"-c", "kill -TERM $$"}, "onNonZero": "return"})
		if resp.Error != "" || resp.Outputs["passed"] != false || resp.Outputs["exitCode"] != nil || resp.Outputs["signal"] == nil {
			t.Fatalf("signal details: %#v", resp)
		}
	})
}

func TestRenderArgTemplateUsesTaskInput(t *testing.T) {
	got, err := renderArgTemplate("feat({{feature.id}}): {{feature.feature}}", map[string]any{"feature": map[string]any{"id": "F-7", "feature": "add cache"}})
	if err != nil || got != "feat(F-7): add cache" {
		t.Fatalf("rendered=%q err=%v", got, err)
	}
}

func TestResolveProgramUsesWorkerPath(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "tool")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveProgram("tool", dir, dir, dir)
	expected, _ := filepath.EvalSymlinks(executable)
	if err != nil || got != expected {
		t.Fatalf("resolved=%q err=%v", got, err)
	}
}
