package workers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScriptExecutorDeliversCompleteLargeInputs(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "generic")
	// Capture stdin so the test can verify admitted inputs are not truncated
	// before adapter presentation/materialization.
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat > \"$0.stdin\"\nprintf '%s' '{\"outputs\":{},\"error\":\"\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	const largeSize = (32 << 10) + 64
	large := strings.Repeat("m", largeSize)
	exec := &ScriptExecutor{adapterDir: dir, Timeout: 5 * time.Second, workBase: t.TempDir()}
	result, err := exec.Execute(ExecuteRequest{
		Executor: "generic",
		Context:  context.Background(),
		Inputs:   map[string]any{"notes": large, "doc": map[string]any{"body": strings.Repeat("j", largeSize)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("executor error: %s", result.Error)
	}
	raw, err := os.ReadFile(script + ".stdin")
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Inputs map[string]any `json:"inputs"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	got, _ := req.Inputs["notes"].(string)
	if got != large {
		t.Fatalf("notes truncated: got %d want %d", len(got), len(large))
	}
	doc, _ := req.Inputs["doc"].(map[string]any)
	body, _ := doc["body"].(string)
	if len(body) != largeSize {
		t.Fatalf("json body truncated: got %d want %d", len(body), largeSize)
	}
}
