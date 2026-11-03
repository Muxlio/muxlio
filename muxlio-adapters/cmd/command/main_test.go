package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/adapter"
)

func TestDirectArgvAndReturnedNonzero(t *testing.T) {
	workspace, workDir := t.TempDir(), t.TempDir()
	input, err := os.CreateTemp(t.TempDir(), "request-*.json")
	if err != nil {
		t.Fatal(err)
	}
	arg := `space quote" dollar$ semi; backtick` + "`" + ` sub$(touch nope)`
	req := map[string]any{"workspace": workspace, "workDir": workDir, "executor": "command", "params": map[string]any{"program": "/bin/echo", "args": []string{arg}, "onNonZero": "return", "timeout": "2s"}, "outputs": map[string]any{"_result": map[string]any{"mime": "application/json"}}}
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
	os.Stdout = outW
	main()
	_ = outW.Close()
	os.Stdout = oldOut
	var resp adapter.Response
	if err = json.NewDecoder(r).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	result, ok := resp.Outputs["_result"].(map[string]any)
	if !ok {
		t.Fatalf("missing command result: %#v", resp)
	}
	if got := result["stdout"].(string); strings.TrimSpace(got) != arg {
		t.Fatalf("argv changed: %q", got)
	}
	if result["exitCode"] != float64(0) {
		t.Fatalf("exit code=%v", result["exitCode"])
	}
	if result["passed"] != true {
		t.Fatalf("passed=%v", result["passed"])
	}
	if _, err = os.Stat(filepath.Join(workspace, "nope")); !os.IsNotExist(err) {
		t.Fatalf("shell interpreted argument: %v", err)
	}
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
