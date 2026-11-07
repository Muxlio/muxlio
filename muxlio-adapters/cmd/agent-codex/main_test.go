package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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

func TestStderrTailIsBounded(t *testing.T) {
	buf := &tailBuffer{limit: 8 * 1024}
	_, _ = buf.Write([]byte(strings.Repeat("x", 9*1024)))
	if got := len(buf.String()); got != 8*1024 {
		t.Fatalf("stderr tail length = %d, want %d", got, 8*1024)
	}
}

// runCodexAdapter builds the agent-codex binary, installs a fake codex script
// that emits the given JSONL lines on stdout, and runs the adapter with a
// minimal request. It returns the adapter's stdout (the Response JSON) and the
// process state so callers can inspect the exit code.
func runCodexAdapter(t *testing.T, fakeScript string) ([]byte, *os.ProcessState) {
	t.Helper()
	bin := buildAgentCodex(t)
	workDir := t.TempDir()
	fakeDir := t.TempDir()
	fakeCodex := filepath.Join(fakeDir, "codex")
	if err := os.WriteFile(fakeCodex, []byte(fakeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"workspace": workDir,
		"executor":  "agent/codex",
		"task":      "do nothing",
		"workDir":   workDir,
		"nodeId":    "n1",
	})
	cmd := exec.Command(bin)
	// Minimal env so the adapter finds the fake codex on PATH and does not
	// inherit MUXLIO_LOG_FD from the test harness.
	cmd.Env = []string{"PATH=" + fakeDir, "HOME=" + workDir}
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("adapter process failed: %v\n%s", err, out)
	}
	return out, cmd.ProcessState
}

func TestAgentCodexAdapterRunsWithoutGitMetadata(t *testing.T) {
	script := `#!/bin/sh
if [ -e .git ]; then
  echo 'test workspace unexpectedly contains Git metadata' >&2
  exit 1
fi
for arg in "$@"; do
  if [ "$arg" = "--skip-git-repo-check" ]; then
    echo '{"type":"turn.completed"}'
    exit 0
  fi
done
echo 'Not inside a trusted directory and --skip-git-repo-check was not specified.' >&2
exit 1
`
	out, _ := runCodexAdapter(t, script)
	var resp adapter.Response
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	if resp.Error != "" {
		t.Fatalf("Codex rejected workspace without Git metadata: %s", resp.Error)
	}
}

// TestAgentCodexAdapterReportsUsageOnSuccess verifies the adapter maps the
// provider-reported token counts from the turn.completed event into the
// shared attempt-level usage record on a successful run. Missing counts are
// not estimated: cached_input_tokens maps to "cached".
func TestAgentCodexAdapterReportsUsageOnSuccess(t *testing.T) {
	script := "#!/bin/sh\n" +
		`echo '{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"done"}}'` + "\n" +
		`echo '{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":50,"cached_input_tokens":30}}'` + "\n"
	out, _ := runCodexAdapter(t, script)
	var resp struct {
		Error string              `json:"error"`
		Usage *adapter.TokenUsage `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected adapter error: %q", resp.Error)
	}
	if resp.Usage == nil {
		t.Fatal("expected non-nil usage from successful turn.completed event")
	}
	if resp.Usage.Input != 100 || resp.Usage.Output != 50 || resp.Usage.Cached != 30 {
		t.Fatalf("usage mismatch: %+v", resp.Usage)
	}
}

// TestAgentCodexAdapterPreservesUsageOnFailure verifies that a run which
// reports usage via turn.completed and then fails (turn.failed + non-zero
// exit) keeps the real token counts in the failure response, so failed
// attempts are not recorded as unavailable when the provider returned usage.
func TestAgentCodexAdapterPreservesUsageOnFailure(t *testing.T) {
	script := "#!/bin/sh\n" +
		`echo '{"type":"turn.completed","usage":{"input_tokens":7,"output_tokens":3,"cached_input_tokens":2}}'` + "\n" +
		`echo '{"type":"turn.failed","error":{"message":"model response stream ended unexpectedly"}}'` + "\n" +
		"echo 'provider error' >&2\n" +
		"exit 1\n"
	out, ps := runCodexAdapter(t, script)
	if ps.ExitCode() != 0 {
		t.Fatalf("adapter should exit 0 on adapter error, got %d", ps.ExitCode())
	}
	var resp struct {
		Error string              `json:"error"`
		Usage *adapter.TokenUsage `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	if resp.Error == "" || !strings.Contains(resp.Error, "agent-codex:") {
		t.Fatalf("expected adapter error in response, got %q", resp.Error)
	}
	if !strings.Contains(resp.Error, "model response stream ended unexpectedly") {
		t.Fatalf("expected turn.failed message in error, got %q", resp.Error)
	}
	if resp.Usage == nil {
		t.Fatal("expected non-nil usage from failed run that reported usage")
	}
	if resp.Usage.Input != 7 || resp.Usage.Output != 3 || resp.Usage.Cached != 2 {
		t.Fatalf("usage mismatch: %+v", resp.Usage)
	}
}

// TestAgentCodexAdapterUsageAbsentWhenNotReported verifies the adapter does
// not estimate or synthesize usage when the provider does not report it — the
// usage field stays absent (nil/omitted) rather than being reported as zero.
func TestAgentCodexAdapterUsageAbsentWhenNotReported(t *testing.T) {
	script := "#!/bin/sh\n" +
		`echo '{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"done"}}'` + "\n" +
		`echo '{"type":"turn.completed"}'` + "\n"
	out, _ := runCodexAdapter(t, script)
	// Decode into a raw map to detect field presence vs. zero value.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	if _, hasUsage := raw["usage"]; hasUsage {
		t.Fatalf("expected usage field absent when not reported, got raw=%s", raw["usage"])
	}
}

// TestAgentCodexAdapterCollectsOutputs verifies the adapter still collects
// declared output files written to the work directory on a successful run.
func TestAgentCodexAdapterCollectsOutputs(t *testing.T) {
	bin := buildAgentCodex(t)
	workDir := t.TempDir()
	outFile := filepath.Join(workDir, "notes.md")
	fakeDir := t.TempDir()
	fakeCodex := filepath.Join(fakeDir, "codex")
	script := "#!/bin/sh\n" +
		`echo '{"type":"turn.completed"}'` + "\n" +
		"printf '%s' '# notes' > '" + outFile + "'\n"
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{
		"workspace": workDir,
		"executor":  "agent/codex",
		"task":      "write notes",
		"workDir":   workDir,
		"nodeId":    "n1",
		"outputs": map[string]any{
			"notes": map[string]any{"mime": "text/markdown", "path": "notes.md"},
		},
	})
	cmd := exec.Command(bin)
	cmd.Env = []string{"PATH=" + fakeDir, "HOME=" + workDir}
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
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected adapter error: %q", resp.Error)
	}
	notes, ok := resp.Outputs["notes"].(map[string]any)
	if !ok || notes["data"] != "# notes" {
		t.Fatalf("notes output = %v", resp.Outputs["notes"])
	}
}

// TestAgentCodexAdapterSkipsTransientReconnect verifies the adapter treats
// transient "Reconnecting..." error events as non-fatal progress (per the
// Codex JSONL spec) so the run still succeeds and reports usage from
// turn.completed. Without this, a dropped-and-retried stream would be recorded
// as a failure even though the turn completed.
func TestAgentCodexAdapterSkipsTransientReconnect(t *testing.T) {
	script := "#!/bin/sh\n" +
		`echo '{"type":"error","message":"Reconnecting... 1/5"}'` + "\n" +
		`echo '{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"done"}}'` + "\n" +
		`echo '{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":5,"cached_input_tokens":2}}'` + "\n"
	out, _ := runCodexAdapter(t, script)
	var resp struct {
		Error string              `json:"error"`
		Usage *adapter.TokenUsage `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	if resp.Error != "" {
		t.Fatalf("transient reconnect should not fail the run, got error: %q", resp.Error)
	}
	if resp.Usage == nil || resp.Usage.Input != 10 || resp.Usage.Output != 5 || resp.Usage.Cached != 2 {
		t.Fatalf("expected usage from turn.completed after reconnect skip, got %+v", resp.Usage)
	}
}

// TestAgentCodexAdapterUsageAbsentOnCleanFailure verifies that a run which
// fails without ever reporting a turn.completed (clean failure) reports usage
// as absent — not estimated as zero — satisfying the "do not estimate missing
// counts" requirement on the failure path. This complements the
// preserve-usage-on-failure case, which has a preceding turn.completed.
func TestAgentCodexAdapterUsageAbsentOnCleanFailure(t *testing.T) {
	script := "#!/bin/sh\n" +
		`echo '{"type":"turn.failed","error":{"message":"model response stream ended unexpectedly"}}'` + "\n" +
		"echo 'provider error' >&2\n" +
		"exit 1\n"
	out, ps := runCodexAdapter(t, script)
	if ps.ExitCode() != 0 {
		t.Fatalf("adapter should exit 0 on adapter error, got %d", ps.ExitCode())
	}
	// Decode into a raw map to detect field presence vs. zero value.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	if _, hasUsage := raw["usage"]; hasUsage {
		t.Fatalf("expected usage absent on clean failure, got raw=%s", raw["usage"])
	}
	var resp struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(out, &resp)
	if resp.Error == "" || !strings.Contains(resp.Error, "agent-codex:") {
		t.Fatalf("expected adapter error on clean failure, got %q", resp.Error)
	}
	if !strings.Contains(resp.Error, "model response stream ended unexpectedly") {
		t.Fatalf("expected turn.failed message in error, got %q", resp.Error)
	}
}

// TestAgentCodexAdapterUsageLastTurnWins verifies that when multiple
// turn.completed events arrive, the adapter reports the last one's usage.
// With --ephemeral the thread is fresh, so each turn.completed carries the
// cumulative running total; the final event holds the run's total, so the
// adapter overwrites rather than accumulates.
func TestAgentCodexAdapterUsageLastTurnWins(t *testing.T) {
	script := "#!/bin/sh\n" +
		`echo '{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":5,"cached_input_tokens":2}}'` + "\n" +
		`echo '{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":50,"cached_input_tokens":30}}'` + "\n"
	out, _ := runCodexAdapter(t, script)
	var resp struct {
		Error string              `json:"error"`
		Usage *adapter.TokenUsage `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected adapter error: %q", resp.Error)
	}
	if resp.Usage == nil {
		t.Fatal("expected non-nil usage from last turn.completed")
	}
	if resp.Usage.Input != 100 || resp.Usage.Output != 50 || resp.Usage.Cached != 30 {
		t.Fatalf("expected last turn.completed usage {100,50,30}, got %+v", resp.Usage)
	}
}

func buildAgentCodex(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "agent-codex")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = wd
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build agent-codex: %v\n%s", err, out)
	}
	return bin
}

func TestCodexProbeInstalledButLoggedOut(t *testing.T) {
	bin := buildAgentCodex(t)
	dir := t.TempDir()
	script := `#!/bin/sh
case "$1" in
 --version) echo 'codex 1.2.3'; exit 0 ;;
 login) echo 'Not logged in; private-token' >&2; exit 1 ;;
 *) echo 'probe attempted task execution' >&2; exit 99 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, adapter.ProbeFlag)
	cmd.Env = append(os.Environ(), "PATH="+dir)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	var report struct {
		Ready  bool
		Checks []struct {
			Name   string
			Passed bool
		}
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("invalid report: %s", raw)
	}
	if report.Ready || strings.Contains(string(raw), "private-token") {
		t.Fatalf("unsafe report: %s", raw)
	}
	found := false
	for _, c := range report.Checks {
		if c.Name == "authentication" && !c.Passed {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing login failure: %s", raw)
	}
}
