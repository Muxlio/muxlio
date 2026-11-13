package main

import (
	"encoding/json"
	"github.com/muxlio/muxlio-adapters/contract"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/adapter"
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
	got := withStderr("process failed", "  QUOTA_EXCEEDED\n")
	if !strings.Contains(got, "QUOTA_EXCEEDED") {
		t.Fatalf("withStderr() = %q, expected QUOTA_EXCEEDED in output", got)
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
	raw := []byte(`{"content":[{"type":"text","text":"alpha"},{"type":"tool_use","text":""},{"type":"text","text":"beta"}]}`)
	chunks := extractAssistantText(raw)
	if len(chunks) != 2 || chunks[0] != "alpha" || chunks[1] != "beta" {
		t.Fatalf("extractAssistantText blocks = %v, want [alpha beta]", chunks)
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

func TestAgentAntigravityAdapterCLIError(t *testing.T) {
	bin := buildAgentAntigravity(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\n" +
		"echo '{\"type\":\"result\",\"status\":\"error\",\"error\":{\"type\":\"AuthError\",\"message\":\"auth failed\"}}'\n" +
		"echo 'provider error' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(fakeCLI, []byte(versionedFixture(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"authorization": fixtureAuthorization(),
		"workspace":     workDir,
		"executor":      "agent/antigravity",
		"task":          "do nothing",
		"workDir":       workDir,
		"nodeId":        "n1",
	})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "ANTIGRAVITY_CLI="+fakeCLI)
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
	if resp.Error == "" || !strings.Contains(resp.Error, "agent-antigravity:") {
		t.Fatalf("expected adapter error in response, got %q", resp.Error)
	}
	if _, err := os.Stat(filepath.Join(workDir, "agy-stream.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("adapter should not write a CLI stream diagnostic file, stat error = %v", err)
	}
}

func TestAgentAntigravityAdapterAntigravityResultErrorStatus(t *testing.T) {
	bin := buildAgentAntigravity(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\n" +
		"echo '{\"type\":\"result\",\"status\":\"error\",\"error\":{\"type\":\"QuotaError\",\"message\":\"quota exceeded\"}}'\n" +
		"exit 0\n"
	if err := os.WriteFile(fakeCLI, []byte(versionedFixture(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"authorization": fixtureAuthorization(),
		"workspace":     workDir,
		"executor":      "agent/antigravity",
		"task":          "do nothing",
		"workDir":       workDir,
		"nodeId":        "n1",
	})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "ANTIGRAVITY_CLI="+fakeCLI)
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("adapter process failed: %v\n%s", err, out)
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	if !strings.Contains(resp.Error, "quota exceeded") {
		t.Fatalf("expected Antigravity result error in response, got %q", resp.Error)
	}
}

func TestAgentAntigravityAdapterCollectsOutputs(t *testing.T) {
	bin := buildAgentAntigravity(t)
	workDir := t.TempDir()
	outFile := filepath.Join(workDir, "notes.md")
	fakeCLI := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}'\n" +
		"echo '{\"type\":\"result\",\"result\":\"done\"}'\n" +
		"printf '%s' '# notes' > '" + outFile + "'\n"
	if err := os.WriteFile(fakeCLI, []byte(versionedFixture(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"authorization": fixtureAuthorization(),
		"workspace":     workDir,
		"executor":      "agent/antigravity",
		"task":          "write notes",
		"workDir":       workDir,
		"nodeId":        "n1",
		"outputs": map[string]any{
			"notes": map[string]any{"mime": "text/markdown", "path": "notes.md"},
		},
	})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "ANTIGRAVITY_CLI="+fakeCLI)
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

func TestAgentAntigravityAdapterCapturesResultResponseField(t *testing.T) {
	bin := buildAgentAntigravity(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\n" +
		`echo '{"event":"result","result":{"status":"SUCCESS","response":"only in response field","usage":{"input_tokens":5,"output_tokens":6}}}'` + "\n"
	if err := os.WriteFile(fakeCLI, []byte(versionedFixture(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{"authorization": fixtureAuthorization(), "workspace": workDir, "executor": "agent/antigravity", "task": "say hello", "workDir": workDir, "nodeId": "n1"})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "ANTIGRAVITY_CLI="+fakeCLI)
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("adapter process failed: %v\n%s", err, out)
	}
	var resp struct {
		Outputs map[string]any `json:"outputs"`
		Error   string         `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter response: %v\n%s", err, out)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected adapter error: %q", resp.Error)
	}
	text, ok := resp.Outputs["_text"].(map[string]any)
	if !ok || text["data"] != "only in response field" {
		t.Fatalf("_text output = %v", resp.Outputs["_text"])
	}
}

func TestAgentAntigravityAdapterMessageEventUsesNestedMessage(t *testing.T) {
	bin := buildAgentAntigravity(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\n" +
		`echo '{"event":"message","role":"assistant","message":{"content":"from nested message"}}'` + "\n" +
		`echo '{"type":"result","status":"success"}'` + "\n"
	if err := os.WriteFile(fakeCLI, []byte(versionedFixture(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{"authorization": fixtureAuthorization(), "workspace": workDir, "executor": "agent/antigravity", "task": "t", "workDir": workDir, "nodeId": "n1"})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "ANTIGRAVITY_CLI="+fakeCLI)
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("adapter process failed: %v\n%s", err, out)
	}
	var resp struct {
		Outputs map[string]any `json:"outputs"`
		Error   string         `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter response: %v\n%s", err, out)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected adapter error: %q", resp.Error)
	}
	text, ok := resp.Outputs["_text"].(map[string]any)
	if !ok || text["data"] != "from nested message" {
		t.Fatalf("_text output = %v", resp.Outputs["_text"])
	}
}

func TestAgentAntigravityAdapterAggregatesInterruptedStepUsage(t *testing.T) {
	bin := buildAgentAntigravity(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\n" +
		`echo '{"event":"init","conversation_id":"c3b66b04-872b-4fbe-a3a4-058a026ef20a"}'` + "\n" +
		`echo '{"event":"step_update","step_update":{"conversation_id":"c3b66b04-872b-4fbe-a3a4-058a026ef20a","step_index":3,"state":"DONE","step_type":"agent_response","usage":{"input_tokens":10302,"output_tokens":582,"thinking_tokens":551,"cache_read_tokens":8113,"total_tokens":10884}}}'` + "\n" +
		`echo '{"event":"step_update","step_update":{"conversation_id":"c3b66b04-872b-4fbe-a3a4-058a026ef20a","step_index":4,"state":"DONE","step_type":"checkpoint","usage":{"input_tokens":116,"output_tokens":7,"thinking_tokens":0,"cache_read_tokens":0,"total_tokens":123}}}'` + "\n"
	if err := os.WriteFile(fakeCLI, []byte(versionedFixture(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{"authorization": fixtureAuthorization(), "workspace": workDir, "executor": "agent/antigravity", "task": "t", "workDir": workDir, "nodeId": "n1"})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "ANTIGRAVITY_CLI="+fakeCLI)
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
		t.Fatalf("invalid adapter response: %v\n%s", err, out)
	}
	if resp.Error == "" || !strings.Contains(resp.Error, "stream ended without a completion event") {
		t.Fatalf("expected interrupted stream error, got %q", resp.Error)
	}
	if fixtureTokens(t, resp.Accounting) == nil || fixtureTokens(t, resp.Accounting).Input != 18531 || fixtureTokens(t, resp.Accounting).Output != 589 || fixtureTokens(t, resp.Accounting).Cached != 8113 {
		t.Fatalf("usage = %+v, want inclusive input=18531 output=589 cached=8113", fixtureTokens(t, resp.Accounting))
	}
}

func TestAgentAntigravityAdapterStepUsageUsesTerminalResultTotals(t *testing.T) {
	bin := buildAgentAntigravity(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\n" +
		`echo '{"event":"step_update","step_update":{"conversation_id":"c1","step_index":3,"state":"DONE","step_type":"agent_response","usage":{"input_tokens":10302,"output_tokens":582,"cache_read_tokens":8113}}}'` + "\n" +
		`echo '{"event":"step_update","step_update":{"conversation_id":"c1","step_index":4,"state":"DONE","step_type":"checkpoint","usage":{"input_tokens":116,"output_tokens":7,"cache_read_tokens":0}}}'` + "\n" +
		`echo '{"event":"result","result":{"status":"SUCCESS","response":"ok","usage":{"input_tokens":10418,"output_tokens":589,"cache_read_tokens":8113}}}'` + "\n"
	if err := os.WriteFile(fakeCLI, []byte(versionedFixture(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{"authorization": fixtureAuthorization(), "workspace": workDir, "executor": "agent/antigravity", "task": "t", "workDir": workDir, "nodeId": "n1"})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "ANTIGRAVITY_CLI="+fakeCLI)
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
		t.Fatalf("invalid adapter response: %v\n%s", err, out)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected adapter error: %q", resp.Error)
	}
	if fixtureTokens(t, resp.Accounting) == nil || fixtureTokens(t, resp.Accounting).Input != 18531 || fixtureTokens(t, resp.Accounting).Output != 589 || fixtureTokens(t, resp.Accounting).Cached != 8113 {
		t.Fatalf("usage = %+v, want terminal cumulative totals", fixtureTokens(t, resp.Accounting))
	}
}

func TestAgentAntigravityAdapterParsesDocumentedStreamEvents(t *testing.T) {
	bin := buildAgentAntigravity(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\n" +
		`echo '{"event":"init","conversation_id":"c1"}'` + "\n" +
		`echo '{"event":"step_update","step_update":{"state":"DONE","step_type":"agent_response","text_delta":"hello from agy"}}'` + "\n" +
		`echo '{"event":"result","result":{"status":"SUCCESS","response":"hello from agy","usage":{"input_tokens":12,"output_tokens":3}}}'` + "\n"
	if err := os.WriteFile(fakeCLI, []byte(versionedFixture(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{"authorization": fixtureAuthorization(), "workspace": workDir, "executor": "agent/antigravity", "task": "say hello", "workDir": workDir, "nodeId": "n1"})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "ANTIGRAVITY_CLI="+fakeCLI)
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("adapter process failed: %v", err)
	}
	var resp struct {
		Outputs    map[string]any       `json:"outputs"`
		Error      string               `json:"error"`
		Accounting *contract.Accounting `json:"accounting"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter response: %v\n%s", err, out)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected adapter error: %q", resp.Error)
	}
	text, ok := resp.Outputs["_text"].(map[string]any)
	if !ok || text["data"] != "hello from agy" {
		t.Fatalf("text output = %v", resp.Outputs["_text"])
	}
	if fixtureTokens(t, resp.Accounting) == nil || fixtureTokens(t, resp.Accounting).Input != 12 || fixtureTokens(t, resp.Accounting).Output != 3 {
		t.Fatalf("usage = %+v", fixtureTokens(t, resp.Accounting))
	}
}

func TestAgentAntigravityAdapterReportsUsageWhenThinkingTokensNull(t *testing.T) {
	bin := buildAgentAntigravity(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\n" +
		`echo '{"type":"result","status":"success","stats":{"input_tokens":100,"output_tokens":50,"thinking_tokens":null,"cached":30}}'` + "\n"
	if err := os.WriteFile(fakeCLI, []byte(versionedFixture(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{"authorization": fixtureAuthorization(), "workspace": workDir, "executor": "agent/antigravity", "task": "do nothing", "workDir": workDir, "nodeId": "n1"})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "ANTIGRAVITY_CLI="+fakeCLI)
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
	if fixtureTokens(t, resp.Accounting) == nil {
		t.Fatal("expected usage when optional thinking_tokens is null")
	}
	if fixtureTokens(t, resp.Accounting).Input != 100 || fixtureTokens(t, resp.Accounting).Output != 50 || fixtureTokens(t, resp.Accounting).Cached != 30 {
		t.Fatalf("usage mismatch: %+v", fixtureTokens(t, resp.Accounting))
	}
}

// TestAgentAntigravityAdapterReportsUsageFromResultStats verifies the adapter
// maps provider-reported token counts from the terminal result event's
// "stats" object into the shared attempt-level usage record on a successful run.
func TestAgentAntigravityAdapterReportsUsageFromResultStats(t *testing.T) {
	bin := buildAgentAntigravity(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\n" +
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}'` + "\n" +
		`echo '{"type":"result","status":"success","stats":{"input_tokens":100,"output_tokens":50,"cached":30,"input":70}}'` + "\n"
	if err := os.WriteFile(fakeCLI, []byte(versionedFixture(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"authorization": fixtureAuthorization(),
		"workspace":     workDir,
		"executor":      "agent/antigravity",
		"task":          "do nothing",
		"workDir":       workDir,
		"nodeId":        "n1",
	})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "ANTIGRAVITY_CLI="+fakeCLI)
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
	if fixtureTokens(t, resp.Accounting) == nil {
		t.Fatal("expected non-nil usage from successful result stats")
	}
	if fixtureTokens(t, resp.Accounting).Input != 100 || fixtureTokens(t, resp.Accounting).Output != 50 || fixtureTokens(t, resp.Accounting).Cached != 30 {
		t.Fatalf("usage mismatch: %+v", fixtureTokens(t, resp.Accounting))
	}
}

// TestAgentAntigravityAdapterReportsUsageFromUsageEvent verifies the adapter
// captures usage from a dedicated "usage" event whose provider-reported
// counts use camelCase field names at the top level of the event object.
func TestAgentAntigravityAdapterReportsUsageFromUsageEvent(t *testing.T) {
	bin := buildAgentAntigravity(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\n" +
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}'` + "\n" +
		`echo '{"type":"usage","model":"antigravity-pro","inputTokens":200,"outputTokens":80,"cachedTokens":15}'` + "\n" +
		`echo '{"type":"result","status":"success","result":"done"}'` + "\n"
	if err := os.WriteFile(fakeCLI, []byte(versionedFixture(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"authorization": fixtureAuthorization(),
		"workspace":     workDir,
		"executor":      "agent/antigravity",
		"task":          "do nothing",
		"workDir":       workDir,
		"nodeId":        "n1",
	})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "ANTIGRAVITY_CLI="+fakeCLI)
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
	if fixtureTokens(t, resp.Accounting) == nil {
		t.Fatal("expected non-nil usage from usage event")
	}
	if fixtureTokens(t, resp.Accounting).Input != 200 || fixtureTokens(t, resp.Accounting).Output != 80 || fixtureTokens(t, resp.Accounting).Cached != 15 {
		t.Fatalf("usage mismatch: %+v", fixtureTokens(t, resp.Accounting))
	}
}

// TestAgentAntigravityAdapterPreservesUsageOnFailure verifies that a terminal
// result event which reports both an error and usage keeps the real token
// counts in the failure response, so failed attempts are not recorded as
// unavailable when the provider returned usage.
func TestAgentAntigravityAdapterPreservesUsageOnFailure(t *testing.T) {
	bin := buildAgentAntigravity(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\n" +
		`echo '{"type":"result","status":"error","error":{"type":"QuotaError","message":"quota exceeded"},"stats":{"input_tokens":7,"output_tokens":3,"cached_content_token_count":2}}'` + "\n" +
		"echo 'provider error' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(fakeCLI, []byte(versionedFixture(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"authorization": fixtureAuthorization(),
		"workspace":     workDir,
		"executor":      "agent/antigravity",
		"task":          "do nothing",
		"workDir":       workDir,
		"nodeId":        "n1",
	})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "ANTIGRAVITY_CLI="+fakeCLI)
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
	if resp.Error == "" || !strings.Contains(resp.Error, "agent-antigravity:") {
		t.Fatalf("expected adapter error in response, got %q", resp.Error)
	}
	if fixtureTokens(t, resp.Accounting) == nil {
		t.Fatal("expected non-nil usage from failed result event that reported usage")
	}
	if fixtureTokens(t, resp.Accounting).Input != 7 || fixtureTokens(t, resp.Accounting).Output != 3 || fixtureTokens(t, resp.Accounting).Cached != 2 {
		t.Fatalf("usage mismatch: %+v", fixtureTokens(t, resp.Accounting))
	}
}

// TestAgentAntigravityAdapterUsageAbsentWhenNotReported verifies the adapter
// does not estimate or synthesize usage when the provider does not report it
// — the usage field stays absent (nil) rather than being reported as zero.
func TestAgentAntigravityAdapterUsageAbsentWhenNotReported(t *testing.T) {
	bin := buildAgentAntigravity(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\n" +
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}'` + "\n" +
		`echo '{"type":"result","status":"success","result":"done"}'` + "\n"
	if err := os.WriteFile(fakeCLI, []byte(versionedFixture(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"authorization": fixtureAuthorization(),
		"workspace":     workDir,
		"executor":      "agent/antigravity",
		"task":          "do nothing",
		"workDir":       workDir,
		"nodeId":        "n1",
	})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "ANTIGRAVITY_CLI="+fakeCLI)
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
	if _, hasUsage := raw["usage"]; hasUsage {
		t.Fatalf("expected usage field absent when not reported, got raw=%s", raw["usage"])
	}
}

// TestAgentAntigravityAdapterUsageAbsentOnFailureWhenNotReported verifies that
// a failed run which never reported usage omits the usage field entirely
// rather than synthesising a zero-valued record.
func TestAgentAntigravityAdapterUsageAbsentOnFailureWhenNotReported(t *testing.T) {
	bin := buildAgentAntigravity(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\n" +
		`echo '{"type":"result","status":"error","error":{"type":"AuthError","message":"auth failed"}}'` + "\n" +
		"exit 1\n"
	if err := os.WriteFile(fakeCLI, []byte(versionedFixture(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"authorization": fixtureAuthorization(),
		"workspace":     workDir,
		"executor":      "agent/antigravity",
		"task":          "do nothing",
		"workDir":       workDir,
		"nodeId":        "n1",
	})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "ANTIGRAVITY_CLI="+fakeCLI)
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("adapter process failed: %v\n%s", err, out)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	if _, hasUsage := raw["usage"]; hasUsage {
		t.Fatalf("expected usage field absent on failure without reported usage, got raw=%s", raw["usage"])
	}
}

func TestAntigravityUsesConfiguredModelUnlessOverridden(t *testing.T) {
	bin := buildAgentAntigravity(t)
	for _, model := range []string{"", "fixture-model"} {
		t.Run("model="+model, func(t *testing.T) {
			workspace := t.TempDir()
			cli := filepath.Join(t.TempDir(), "agy")
			script := `#!/bin/sh
found=false
previous=''
for arg in "$@"; do
  if [ "$previous" = '--model' ]; then
    [ -n "$EXPECTED_MODEL" ] && [ "$arg" = "$EXPECTED_MODEL" ] || exit 1
    found=true
  fi
  previous=$arg
done
if [ -n "$EXPECTED_MODEL" ]; then $found || exit 1; else $found && exit 1; fi
echo '{"type":"result","status":"success","result":"done"}'
`
			if err := os.WriteFile(cli, []byte(versionedFixture(script)), 0o700); err != nil {
				t.Fatal(err)
			}
			params, _ := json.Marshal(map[string]string{"model": model})
			req := adapter.Request{Authorization: fixtureAuthorization(), Executor: "agent/antigravity", Workspace: workspace, WorkDir: workspace, Params: params, Task: "do nothing"}
			input, _ := json.Marshal(req)
			cmd := exec.Command(bin)
			cmd.Env = []string{"PATH=/bin:/usr/bin", "HOME=" + workspace, "ANTIGRAVITY_CLI=" + cli, "EXPECTED_MODEL=" + model}
			cmd.Stdin = strings.NewReader(string(input))
			raw, err := cmd.Output()
			if err != nil {
				t.Fatalf("adapter process: %v\n%s", err, raw)
			}
			var resp adapter.Response
			if err := json.Unmarshal(raw, &resp); err != nil || resp.Error != "" {
				t.Fatalf("model selection: %v %+v", err, resp)
			}
		})
	}
}

func buildAgentAntigravity(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "agent-antigravity")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = wd
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build agent-antigravity: %v\n%s", err, out)
	}
	return bin
}

type fixtureTokenValues struct{ Input, Output, Cached int64 }

func fixtureTokens(t *testing.T, a *contract.Accounting) *fixtureTokenValues {
	t.Helper()
	if a == nil {
		t.Fatal("missing accounting snapshot")
	}
	if !a.Final {
		t.Fatal("accounting snapshot is not final")
	}
	for _, e := range a.Entries {
		if e.Tokens == nil {
			continue
		}
		read := func(m contract.Measure[contract.Count]) int64 {
			if m.Collected == nil {
				return 0
			}
			n, err := strconv.ParseInt(string(m.Collected.Value), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		return &fixtureTokenValues{Input: read(e.Tokens.Input), Output: read(e.Tokens.Output), Cached: read(e.Tokens.CacheRead)}
	}
	return nil
}

// Replay captured native events through the executable's actual progress
// channel. A cached step must publish valid accounting before terminal output.
func TestAgentAntigravityStreamsCachedUsage(t *testing.T) {
	bin := buildAgentAntigravity(t)
	raw, err := os.ReadFile("testdata/1.5.7-cached-success.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	for i, line := range lines {
		if i == len(lines)-1 {
			script.WriteString("sleep 3\n")
		}
		script.WriteString("printf '%s\\n' '" + line + "'\n")
	}
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "agy")
	if err := os.WriteFile(fakeCLI, []byte(versionedFixture(script.String())), 0o755); err != nil {
		t.Fatal(err)
	}
	progress, err := os.CreateTemp(t.TempDir(), "progress")
	if err != nil {
		t.Fatal(err)
	}
	defer progress.Close()
	stdin, _ := json.Marshal(map[string]any{"authorization": fixtureAuthorization(), "workspace": workDir, "executor": "agent/antigravity", "task": "fixture replay", "workDir": workDir, "nodeId": "n1"})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "ANTIGRAVITY_CLI="+fakeCLI, "MUXLIO_LOG_FD=3")
	cmd.ExtraFiles = []*os.File{progress}
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("adapter: %v %s", err, out)
	}
	var response adapter.Response
	if err := json.Unmarshal(out, &response); err != nil || response.Error != "" {
		t.Fatalf("response: %v %+v", err, response)
	}
	progressRaw, err := os.ReadFile(progress.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(progressRaw), "accounting-observation-rejected") {
		t.Fatalf("valid live usage rejected: %s", progressRaw)
	}
	streamedCachedUsage := false
	for _, line := range strings.Split(strings.TrimSpace(string(progressRaw)), "\n") {
		var frame contract.Progress
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Accounting == nil || frame.Accounting.Final {
			continue
		}
		if err := frame.Accounting.Validate(); err != nil {
			t.Fatal(err)
		}
		for _, entry := range frame.Accounting.Entries {
			if entry.Tokens != nil && entry.Tokens.CacheRead.Collected != nil && entry.Tokens.CacheRead.Collected.Value == "19314" {
				streamedCachedUsage = true
			}
		}
	}
	if !streamedCachedUsage {
		t.Fatalf("no cached accounting before final: %s", progressRaw)
	}
	values := fixtureTokens(t, response.Accounting)
	if values.Input != 34255 || values.Output != 643 || values.Cached != 19314 {
		t.Fatalf("final usage: %+v", values)
	}
}
