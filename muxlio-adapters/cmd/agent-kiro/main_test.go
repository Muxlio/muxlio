package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseKiroUsageFromStructuredResultMetadata(t *testing.T) {
	got := parseKiroUsage(json.RawMessage(`{"status":"success","metadata":{"usage":{"input_tokens":100,"output_tokens":40,"cached_input_tokens":12}}}`))
	if got == nil || got.Input != 100 || got.Output != 40 || got.Cached != 12 {
		t.Fatalf("usage = %+v, want input=100 output=40 cached=12", got)
	}
}

func TestParseKiroOptionalNumericMetadata(t *testing.T) {
	// These shapes exercise optional token counts understood by Kiro's client;
	// they are not evidence that its credit-only completion reports tokens.
	for _, container := range []string{"tokenUsage", "usage", "metrics"} {
		raw := `{"update":{"sessionUpdate":"session_info_update","_meta":{"kiro":{"kind":"turn_completion","` + container + `":{"uncachedInputTokens":100,"outputTokens":40,"cacheReadInputTokens":12}}}}}`
		got := parseKiroUsage(json.RawMessage(raw))
		if got == nil || got.Input != 100 || got.Output != 40 || got.Cached != 12 {
			t.Fatalf("ACP %s usage = %+v", container, got)
		}
	}
	for _, raw := range []string{
		`{"update":{"_meta":{"kiro":{"kind":"turn_completion","promptTurnSummaries":[{"unit":"credit","usage":0.1}]}}}}`,
		`{"update":{"_meta":{"kiro":{"kind":"context_usage","contextUsage":{"usagePercentage":10},"breakdown":{"yourPrompts":{"tokens":1000}}}}}}`,
	} {
		if got := parseKiroUsage(json.RawMessage(raw)); got != nil {
			t.Fatalf("credits/context size must not become token usage: %+v", got)
		}
	}
}

func TestParseKiroUsageFromFailedResult(t *testing.T) {
	got := parseKiroUsage(json.RawMessage(`{"status":"error","metadata":{"inputTokens":7,"outputTokens":3,"cachedTokens":2}}`))
	if got == nil || got.Input != 7 || got.Output != 3 || got.Cached != 2 {
		t.Fatalf("usage = %+v, want input=7 output=3 cached=2", got)
	}
}

func TestParseKiroUsageLeavesUnreportedCountsUnavailable(t *testing.T) {
	got := parseKiroUsage(json.RawMessage(`{"status":"success","metadata":{"usage":{"input_tokens":9}}}`))
	if got == nil || got.Input != 9 {
		t.Fatalf("usage = %+v, want reported input=9", got)
	}
	if _, outOK := got.ReportedOutput(); outOK {
		t.Fatalf("usage = %+v, want missing output unavailable", got)
	}
	if _, cachedOK := got.ReportedCached(); cachedOK {
		t.Fatalf("usage = %+v, want missing output and cached unavailable", got)
	}
	if absent := parseKiroUsage(json.RawMessage(`{"status":"success","metadata":{}}`)); absent != nil {
		t.Fatalf("usage = %+v, want absent", absent)
	}
}

func TestParseKiroUsageFromRunFinishedUsageAndDirectMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
	}{
		{"direct usage", `{"usage":{"input_tokens":11,"output_tokens":5,"cached_input_tokens":2}}`},
		{"direct metadata fields", `{"metadata":{"input_tokens":13,"output_tokens":6,"cached_input_tokens":3}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseKiroUsage(json.RawMessage(tc.data))
			if got == nil || got.Input == 0 || got.Output == 0 || got.Cached == 0 {
				t.Fatalf("usage = %+v, want all direct counts parsed", got)
			}
		})
	}
}

func TestParseKiroUsageKeepsAnyReportedTerminalCounts(t *testing.T) {
	prior := parseKiroUsage(json.RawMessage(`{"metadata":{"usage":{"input_tokens":11}}}`))
	later := parseKiroUsage(json.RawMessage(`{"status":"success","metadata":{}}`))
	prior = mergeTerminalUsage(prior, later)
	if prior == nil || prior.Input != 11 {
		t.Fatalf("usage after empty terminal metadata = %+v, want prior reported input=11", prior)
	}
}

func TestMergeTerminalUsageOverlaysPartialSessionUpdate(t *testing.T) {
	prior := parseKiroUsage(json.RawMessage(`{"update":{"_meta":{"kiro":{"tokenUsage":{"input_tokens":120,"output_tokens":30,"cached_input_tokens":40}}}}}`))
	later := parseKiroUsage(json.RawMessage(`{"update":{"_meta":{"kiro":{"tokenUsage":{"input_tokens":99}}}}}`))
	got := mergeTerminalUsage(prior, later)
	want := parseKiroUsage(json.RawMessage(`{"update":{"_meta":{"kiro":{"tokenUsage":{"input_tokens":99,"output_tokens":30,"cached_input_tokens":40}}}}}`))
	if got == nil || want == nil || !got.Equal(*want) {
		t.Fatalf("merged usage = %+v, want %+v", got, want)
	}
}

func TestKiroStreamMergesPartialSessionUsage(t *testing.T) {
	bin := buildAgentKiro(t)
	workDir := t.TempDir()
	binDir := t.TempDir()
	stream := `{"type":"sessionUpdate","data":{"update":{"sessionUpdate":"session_info_update","_meta":{"kiro":{"kind":"turn_completion","tokenUsage":{"input_tokens":120,"output_tokens":30,"cached_input_tokens":40}}}}}}
{"type":"sessionUpdate","data":{"update":{"sessionUpdate":"session_info_update","_meta":{"kiro":{"kind":"turn_completion","tokenUsage":{"input_tokens":99}}}}}}
{"type":"runFinished","data":{"status":"success","finalText":"done"}}
`
	cli := filepath.Join(binDir, "kiro-cli")
	script := "#!/bin/sh\n/bin/cat <<'EOF'\n" + stream + "EOF\n"
	if err := os.WriteFile(cli, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	req, err := json.Marshal(map[string]any{
		"contractVersion":      "1",
		"workspace":            workDir,
		"workDir":              workDir,
		"executor":             "agent/kiro",
		"task":                 "partial-usage",
		"hostExecutionAllowed": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = []string{"PATH=" + binDir, "HOME=" + workDir, "KIRO_CLI=" + cli}
	cmd.Stdin = strings.NewReader(string(req))
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("adapter process failed: %v", err)
	}
	var resp struct {
		Usage *struct {
			Input, Output, Cached int64
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, raw)
	}
	if resp.Usage == nil || resp.Usage.Input != 99 || resp.Usage.Output != 30 || resp.Usage.Cached != 40 {
		t.Fatalf("usage = %+v, want input=99 output=30 cached=40", resp.Usage)
	}
}

func TestTailBufferKeepsLastLimitBytes(t *testing.T) {
	buffer := &tailBuffer{limit: 8}
	_, _ = buffer.Write([]byte("012345"))
	_, _ = buffer.Write([]byte("6789"))
	if got, want := buffer.String(), "23456789"; got != want {
		t.Fatalf("tailBuffer.String() = %q, want %q", got, want)
	}
}

func TestTailBufferTruncatesLargeWrite(t *testing.T) {
	buffer := &tailBuffer{limit: 8}
	_, _ = buffer.Write([]byte("0123456789abcdef"))
	if got, want := buffer.String(), "89abcdef"; got != want {
		t.Fatalf("tailBuffer.String() = %q, want %q", got, want)
	}
}

func TestWithStderr(t *testing.T) {
	tests := []struct {
		name, reason, stderr, want string
	}{
		{name: "detail", reason: "process failed", stderr: " MONTHLY_REQUEST_COUNT\n", want: "process failed (stderr: MONTHLY_REQUEST_COUNT)"},
		{name: "empty stderr", reason: "process failed", want: "process failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := withStderr(tt.reason, tt.stderr); got != tt.want {
				t.Fatalf("withStderr() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStderrTailIsBounded(t *testing.T) {
	buffer := &tailBuffer{limit: 8 * 1024}
	_, _ = buffer.Write([]byte(strings.Repeat("x", 9*1024)))
	if got := len(buffer.String()); got != 8*1024 {
		t.Fatalf("stderr tail length = %d, want %d", got, 8*1024)
	}
}

func buildAgentKiro(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "agent-kiro")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func TestKiroAdapterTreatsRunErrorAsTerminalEvent(t *testing.T) {
	bin := buildAgentKiro(t)
	workDir := t.TempDir()
	binDir := t.TempDir()
	stream := `{"type":"runError","data":{"stage":"interrupted","message":"run was interrupted"}}` + "\n"
	cli := filepath.Join(binDir, "kiro-cli")
	script := "#!/bin/sh\n/bin/cat <<'EOF'\n" + stream + "EOF\n"
	if err := os.WriteFile(cli, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	req, err := json.Marshal(map[string]any{
		"contractVersion":      "1",
		"workspace":              workDir,
		"workDir":                workDir,
		"executor":               "agent/kiro",
		"task":                   "run-error",
		"hostExecutionAllowed":   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = []string{"PATH=" + binDir, "HOME=" + workDir, "KIRO_CLI=" + cli}
	cmd.Stdin = strings.NewReader(string(req))
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("adapter process failed: %v", err)
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, raw)
	}
	if resp.Error == "" {
		t.Fatal("expected adapter error for runError terminal event")
	}
	if !strings.Contains(resp.Error, "run was interrupted") {
		t.Fatalf("error = %q, want provider runError message", resp.Error)
	}
	if strings.Contains(resp.Error, "stream ended without a completion event") {
		t.Fatalf("runError must be a completion event: %q", resp.Error)
	}
}
