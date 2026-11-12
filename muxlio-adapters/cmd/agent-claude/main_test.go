package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
)

func TestTailBufferKeepsLastLimitBytes(t *testing.T) {
	buf := &tailBuffer{limit: 8}
	_, _ = buf.Write([]byte("012345"))
	_, _ = buf.Write([]byte("6789"))
	if got, want := buf.String(), "23456789"; got != want {
		t.Fatalf("tailBuffer.String() = %q, want %q", got, want)
	}
}

func TestTailBufferTruncatesLargeWrite(t *testing.T) {
	buf := &tailBuffer{limit: 8}
	_, _ = buf.Write([]byte("0123456789abcdef"))
	if got, want := buf.String(), "89abcdef"; got != want {
		t.Fatalf("tailBuffer.String() = %q, want %q", got, want)
	}
}

func TestWithStderrEmpty(t *testing.T) {
	if got := withStderr("process failed", ""); got != "process failed" {
		t.Fatalf("withStderr(_, \"\") = %q, want plain reason", got)
	}
}

func TestWithStderrAddsDetail(t *testing.T) {
	got := withStderr("process failed", "  AUTH_FAILED\n")
	if !strings.Contains(got, "AUTH_FAILED") {
		t.Fatalf("withStderr() = %q, expected AUTH_FAILED in output", got)
	}
}

func TestAppendFailure(t *testing.T) {
	if got := appendFailure("", "first"); got != "first" {
		t.Fatalf("appendFailure(\"\", \"first\") = %q, want %q", got, "first")
	}
	if got := appendFailure("first", "second"); got != "first; second" {
		t.Fatalf("appendFailure(\"first\", \"second\") = %q, want %q", got, "first; second")
	}
}

func TestExtractAssistantTextPlainString(t *testing.T) {
	raw := []byte(`{"content":"hello world"}`)
	chunks := extractAssistantText(raw)
	if len(chunks) != 1 || chunks[0] != "hello world" {
		t.Fatalf("extractAssistantText plain = %v, want [\"hello world\"]", chunks)
	}
}

func TestExtractAssistantTextBlockArray(t *testing.T) {
	raw := []byte(`{"content":[{"type":"text","text":"first"},{"type":"tool_use","text":""},{"type":"text","text":"second"}]}`)
	chunks := extractAssistantText(raw)
	if len(chunks) != 2 || chunks[0] != "first" || chunks[1] != "second" {
		t.Fatalf("extractAssistantText blocks = %v, want [first second]", chunks)
	}
}

func TestExtractAssistantTextNil(t *testing.T) {
	chunks := extractAssistantText(nil)
	if len(chunks) != 0 {
		t.Fatalf("extractAssistantText(nil) should return empty, got %v", chunks)
	}
}

func TestStderrTailIsBounded(t *testing.T) {
	buf := &tailBuffer{limit: 8 * 1024}
	_, _ = buf.Write([]byte(strings.Repeat("x", 9*1024)))
	if got := len(buf.String()); got != 8*1024 {
		t.Fatalf("stderr tail length = %d, want %d", got, 8*1024)
	}
}

func TestBuildPromptDeclaresOutputs(t *testing.T) {
	workDir := t.TempDir()
	req := &adapter.Request{
		Task:    "implement feature",
		WorkDir: workDir,
		NodeID:  "step1",
		Outputs: map[string]adapter.OutputDecl{
			"notes": {Mime: "text/markdown", Path: "notes.md"},
		},
	}
	prompt := buildPrompt(req)
	if !strings.Contains(prompt, workDir+"/notes.md") {
		t.Fatalf("buildPrompt() missing declared output path, got %q", prompt)
	}
}

func TestAgentClaudeAdapterCLIError(t *testing.T) {
	bin := buildAgentClaude(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"echo '{\"type\":\"result\",\"is_error\":true,\"error\":\"auth failed\"}'\n" +
		"echo 'provider error' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(fakeCLI, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"workspace": workDir,
		"executor":  "agent/claude",
		"task":      "do nothing",
		"workDir":   workDir,
		"nodeId":    "n1",
	})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "CLAUDE_CLI="+fakeCLI)
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("adapter process failed: %v\n%s", err, out)
	}
	if cmd.ProcessState.ExitCode() != 0 {
		t.Fatalf("adapter should exit 0 on adapter error, got %d", cmd.ProcessState.ExitCode())
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	if resp.Error == "" || !strings.Contains(resp.Error, "agent-claude:") {
		t.Fatalf("expected adapter error in response, got %q", resp.Error)
	}
	if _, err := os.Stat(filepath.Join(workDir, "claude-stream.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("adapter unexpectedly created diagnostic file in workDir: stat err=%v", err)
	}
}

func TestAgentClaudeAdapterAcceptsDoneTerminalEvent(t *testing.T) {
	bin := buildAgentClaude(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}'` + "\n" +
		`echo '{"type":"done"}'` + "\n"
	if err := os.WriteFile(fakeCLI, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"workspace": workDir,
		"executor":  "agent/claude",
		"task":      "do nothing",
		"workDir":   workDir,
		"nodeId":    "n1",
	})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "CLAUDE_CLI="+fakeCLI)
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("adapter process failed: %v\n%s", err, out)
	}
	var resp struct {
		Error   string         `json:"error"`
		Outputs map[string]any `json:"outputs"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected adapter error: %q", resp.Error)
	}
	text, ok := resp.Outputs["_text"].(map[string]any)
	if !ok || text["data"] != "done" {
		t.Fatalf("_text output = %v", resp.Outputs["_text"])
	}
}

func TestAgentClaudeAdapterRejectsStreamWithoutResult(t *testing.T) {
	bin := buildAgentClaude(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\necho '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"partial\"}]}}'\n"
	if err := os.WriteFile(fakeCLI, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"workspace": workDir,
		"executor":  "agent/claude",
		"task":      "do nothing",
		"workDir":   workDir,
		"nodeId":    "n1",
	})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "CLAUDE_CLI="+fakeCLI)
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("adapter process failed: %v", err)
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	if !strings.Contains(resp.Error, "without a terminal event") {
		t.Fatalf("expected incomplete stream adapter error, got %q", resp.Error)
	}
}

func TestAgentClaudeAdapterCollectsOutputs(t *testing.T) {
	bin := buildAgentClaude(t)
	workDir := t.TempDir()
	outFile := filepath.Join(workDir, "notes.md")
	fakeCLI := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}'\n" +
		"echo '{\"type\":\"result\",\"result\":\"done\"}'\n" +
		"printf '%s' '# notes' > '" + outFile + "'\n"
	if err := os.WriteFile(fakeCLI, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"workspace": workDir,
		"executor":  "agent/claude",
		"task":      "write notes",
		"workDir":   workDir,
		"nodeId":    "n1",
		"outputs": map[string]any{
			"notes": map[string]any{"mime": "text/markdown", "path": "notes.md"},
		},
	})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "CLAUDE_CLI="+fakeCLI)
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("adapter process failed: %v", err)
	}
	var resp struct {
		Outputs map[string]any `json:"outputs"`
		Error   string         `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected adapter error: %q", resp.Error)
	}
	notes, ok := resp.Outputs["notes"].(map[string]any)
	if !ok || notes["data"] != "# notes" {
		t.Fatalf("notes output = %v", resp.Outputs["notes"])
	}
	text, ok := resp.Outputs["_text"].(map[string]any)
	if !ok || text["data"] != "done" {
		t.Fatalf("_text output = %v", resp.Outputs["_text"])
	}
}

// TestAgentClaudeAdapterReportsUsageOnSuccess verifies the adapter maps the
// provider-reported token counts from the terminal result event into the
// shared attempt-level usage record on a successful run. Missing counts are
// not estimated: cache_read_input_tokens maps to "cached".
func TestAgentClaudeAdapterReportsUsageOnSuccess(t *testing.T) {
	bin := buildAgentClaude(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}'` + "\n" +
		`echo '{"type":"result","result":"done","usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":30,"cache_creation_input_tokens":0}}'` + "\n"
	if err := os.WriteFile(fakeCLI, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"workspace": workDir,
		"executor":  "agent/claude",
		"task":      "do nothing",
		"workDir":   workDir,
		"nodeId":    "n1",
	})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "CLAUDE_CLI="+fakeCLI)
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("adapter process failed: %v\n%s", err, out)
	}
	var resp struct {
		Error      string               `json:"error"`
		Accounting *contract.Accounting `json:"accounting"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected adapter error: %q", resp.Error)
	}
	assertClaudeTokens(t, resp.Accounting, "130", "50", "30")
}

// TestAgentClaudeAdapterPreservesUsageOnFailure verifies that a terminal
// result event which reports both an error and usage keeps the real token
// counts in the failure response, so failed attempts are not recorded as
// unavailable when the provider returned usage.
func TestAgentClaudeAdapterPreservesUsageOnFailure(t *testing.T) {
	bin := buildAgentClaude(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		`echo '{"type":"result","is_error":true,"error":"model overloaded","usage":{"input_tokens":7,"output_tokens":3,"cache_read_input_tokens":2,"cache_creation_input_tokens":0}}'` + "\n" +
		"echo 'provider error' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(fakeCLI, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"workspace": workDir,
		"executor":  "agent/claude",
		"task":      "do nothing",
		"workDir":   workDir,
		"nodeId":    "n1",
	})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "CLAUDE_CLI="+fakeCLI)
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("adapter process failed: %v\n%s", err, out)
	}
	if cmd.ProcessState.ExitCode() != 0 {
		t.Fatalf("adapter should exit 0 on adapter error, got %d", cmd.ProcessState.ExitCode())
	}
	var resp struct {
		Error      string               `json:"error"`
		Accounting *contract.Accounting `json:"accounting"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	if resp.Error == "" || !strings.Contains(resp.Error, "agent-claude:") {
		t.Fatalf("expected adapter error in response, got %q", resp.Error)
	}
	assertClaudeTokens(t, resp.Accounting, "9", "3", "2")
}

func TestAgentClaudeAdapterReportsInclusiveUsageWithCacheCreation(t *testing.T) {
	bin := buildAgentClaude(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		`echo '{"type":"result","result":"done","usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40}}'` + "\n"
	if err := os.WriteFile(fakeCLI, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"workspace": workDir,
		"executor":  "agent/claude",
		"task":      "do nothing",
		"workDir":   workDir,
		"nodeId":    "n1",
	})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "CLAUDE_CLI="+fakeCLI)
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("adapter process failed: %v\n%s", err, out)
	}
	var resp struct {
		Error      string               `json:"error"`
		Accounting *contract.Accounting `json:"accounting"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	assertClaudeTokens(t, resp.Accounting, "170", "20", "30")
}

// TestAgentClaudeAdapterUsageAbsentWhenNotReported verifies the adapter does
// not estimate or synthesize usage when the provider does not report it — the
// usage field stays absent (nil) rather than being reported as zero.
func TestAgentClaudeAdapterUsageAbsentWhenNotReported(t *testing.T) {
	bin := buildAgentClaude(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		`echo '{"type":"result","result":"done"}'` + "\n"
	if err := os.WriteFile(fakeCLI, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"workspace": workDir,
		"executor":  "agent/claude",
		"task":      "do nothing",
		"workDir":   workDir,
		"nodeId":    "n1",
	})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "CLAUDE_CLI="+fakeCLI)
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("adapter process failed: %v\n%s", err, out)
	}
	// Decode into a raw map to detect field presence vs. zero value.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	_, hasUsage := raw["usage"]
	if hasUsage {
		t.Fatalf("expected usage field absent when not reported, got raw=%s", raw["usage"])
	}
}

func TestAgentClaudeAdapterReportsEstimatedCostOnSuccess(t *testing.T) {
	bin := buildAgentClaude(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		`echo '{"type":"result","result":"done","total_cost_usd":0.0812}'` + "\n"
	if err := os.WriteFile(fakeCLI, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"workspace": workDir, "executor": "agent/claude", "task": "do nothing", "workDir": workDir, "nodeId": "n1",
	})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "CLAUDE_CLI="+fakeCLI)
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("adapter process failed: %v\n%s", err, out)
	}
	var resp struct {
		Accounting *contract.Accounting `json:"accounting"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	assertClaudeCost(t, resp.Accounting, "0.0812")
}

func TestAgentClaudeAdapterPreservesEstimatedCostOnFailure(t *testing.T) {
	bin := buildAgentClaude(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		`echo '{"type":"result","is_error":true,"error":"overloaded","total_cost_usd":0.05}'` + "\n" +
		"exit 1\n"
	if err := os.WriteFile(fakeCLI, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"workspace": workDir, "executor": "agent/claude", "task": "do nothing", "workDir": workDir, "nodeId": "n1",
	})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "CLAUDE_CLI="+fakeCLI)
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("adapter process failed: %v\n%s", err, out)
	}
	var resp struct {
		Error      string               `json:"error"`
		Accounting *contract.Accounting `json:"accounting"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	if resp.Error == "" {
		t.Fatal("expected failure response")
	}
	assertClaudeCost(t, resp.Accounting, "0.05")
}

func TestAgentClaudeAdapterLatestResultCostWins(t *testing.T) {
	bin := buildAgentClaude(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		`echo '{"type":"result","total_cost_usd":0.9}'` + "\n" +
		`echo '{"type":"result","result":"done","total_cost_usd":0.1}'` + "\n"
	if err := os.WriteFile(fakeCLI, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"workspace": workDir, "executor": "agent/claude", "task": "do nothing", "workDir": workDir, "nodeId": "n1",
	})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "CLAUDE_CLI="+fakeCLI)
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("adapter process failed: %v\n%s", err, out)
	}
	var resp struct {
		Accounting *contract.Accounting `json:"accounting"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	assertClaudeCost(t, resp.Accounting, "0.1")
}

func buildAgentClaude(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "agent-claude")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = wd
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build agent-claude: %v\n%s", err, out)
	}
	return bin
}

func assertClaudeTokens(t *testing.T, a *contract.Accounting, input, output, cache string) {
	t.Helper()
	if a == nil || !a.Final || len(a.Entries) != 1 || a.Entries[0].Tokens == nil {
		t.Fatalf("missing accounting: %+v", a)
	}
	v := a.Entries[0].Tokens
	if v.Input.Collected == nil || string(v.Input.Collected.Value) != input || v.Output.Collected == nil || string(v.Output.Collected.Value) != output || v.CacheRead.Collected == nil || string(v.CacheRead.Collected.Value) != cache {
		t.Fatalf("unexpected tokens: %+v", v)
	}
}
func assertClaudeCost(t *testing.T, a *contract.Accounting, want string) {
	t.Helper()
	if a == nil || !a.Final || len(a.Entries) != 1 || len(a.Entries[0].Costs) != 1 {
		t.Fatalf("missing cost accounting: %+v", a)
	}
	c := a.Entries[0].Costs[0]
	if c.Currency != "USD" || c.Basis != "unspecified" || c.Amount.Collected == nil || string(c.Amount.Collected.Value) != want || c.Amount.Collected.Fidelity != "provider-estimate" || c.Amount.Estimated != nil {
		t.Fatalf("unexpected cost: %+v", c)
	}
}
