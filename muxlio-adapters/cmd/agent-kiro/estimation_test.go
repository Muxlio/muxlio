package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/accounting"
	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/tiktoken-go/tokenizer"
)

func kiroTestEncoder(t *testing.T) tokenizer.Codec {
	t.Helper()
	e, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestKiroOutputTokenizationIgnoresChunkBoundariesAndEchoes(t *testing.T) {
	enc := kiroTestEncoder(t)
	for _, chunks := range [][]string{{"hello world 你好"}, {"h", "ello", " world ", "你", "好"}} {
		var e kiroOutputEstimator
		for _, text := range chunks {
			b, _ := json.Marshal(map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": text}}})
			e.observe("sessionUpdate", b)
		}
		e.observe("text", json.RawMessage(`{"content":"hello world 你好"}`))
		e.observe("runFinished", json.RawMessage(`{"finalText":"hello world 你好"}`))
		n, err := e.count(enc)
		// cl100k_base: hello, world, space, 你, 好. Characters/4 gives 4.
		if err != nil || n == nil || *n != 5 {
			t.Fatalf("count = %v, err = %v", n, err)
		}
	}
}

func TestKiroOutputCountsReasoningAndLatestToolArguments(t *testing.T) {
	var e kiroOutputEstimator
	for _, raw := range []string{
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"think"},"_meta":{"kiro":{"reasoningSignature":"do not tokenize"}}}`,
		`{"sessionUpdate":"tool_call","toolCallId":"call","rawInput":{},"title":"do not tokenize"}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call","rawInput":{"path":"a"}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call","rawInput":{"path":"a"},"content":[{"text":"do not tokenize tool result"}]}`,
		`{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"do not tokenize"}}`,
	} {
		e.observe("sessionUpdate", json.RawMessage(raw))
	}
	enc := kiroTestEncoder(t)
	wantArgs, _ := enc.Count(`{"path":"a"}`)
	n, err := e.count(enc)
	if err != nil || n == nil || *n != int64(1+wantArgs) {
		t.Fatalf("count = %v, err = %v", n, err)
	}
}

func TestKiroOutputAbsentIsUnavailableAndLegacyHasFallback(t *testing.T) {
	enc := kiroTestEncoder(t)
	var empty kiroOutputEstimator
	empty.observe("runFinished", json.RawMessage(`{"status":"success"}`))
	if n, err := empty.count(enc); err != nil || n != nil {
		t.Fatalf("missing output = %v, %v", n, err)
	}
	for _, chunks := range []bool{false, true} {
		var e kiroOutputEstimator
		if chunks {
			e.observe("text", json.RawMessage(`{"content":"hel"}`))
			e.observe("text", json.RawMessage(`{"content":"lo"}`))
		}
		e.observe("runFinished", json.RawMessage(`{"finalText":"hello"}`))
		if n, err := e.count(enc); err != nil || n == nil || *n != 1 {
			t.Fatalf("output = %v, %v", n, err)
		}
	}
}

func TestKiroCapturedInputCountsActualRequestsIncludingCompaction(t *testing.T) {
	dir := t.TempDir()
	// A tool continuation resends history. A later compacted request sends a
	// summary instead: the estimator must use these payloads, not replay history.
	requests := []string{
		`{"request":{"conversationState":{"currentMessage":{"userInputMessage":{"content":"hello","modelId":"ignore me"}}}}}`,
		`{"request":{"conversationState":{"history":[{"userInputMessage":{"content":"hello"}},{"assistantResponseMessage":{"content":"world"}}],"currentMessage":{"userInputMessage":{"userInputMessageContext":{"toolResults":[{"toolUseId":"ignore me","content":[{"text":"done"}]}]}}}}}}`,
		`{"request":{"conversationState":{"currentMessage":{"userInputMessage":{"content":"summary","images":[{"bytes":"ignore me"}]}}}}}`,
	}
	for i, request := range requests {
		if err := os.WriteFile(filepath.Join(dir, string(rune('a'+i))+".json"), []byte(request), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Symlinks must not pull unrelated captures into the invocation.
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(requests[0]), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "linked.json")); err != nil {
		t.Fatal(err)
	}
	n, err := kiroCapturedInput(dir, kiroTestEncoder(t))
	if err != nil || n == nil || *n != 5 {
		t.Fatalf("input = %v, %v; want 5", n, err)
	}
}

func TestKiroInputIncludesToolsButExcludesSignaturesAndTransportIDs(t *testing.T) {
	enc := kiroTestEncoder(t)
	raw := `{"request":{"conversationState":{"history":[{"assistantResponseMessage":{"content":"hello","reasoningContent":{"text":"think","signature":"ignore"},"toolUses":[{"name":"read","toolUseId":"ignore","input":{"path":"a"}}]}}],"currentMessage":{"userInputMessage":{"userInputMessageContext":{"tools":[{"toolSpecification":{"name":"read","description":"read a file"}}]}}}}}}`
	n, err := kiroRequestInput([]byte(raw), enc)
	want := 0
	for _, text := range []string{"hello", "think", "read", `{"path":"a"}`, `[{"toolSpecification":{"description":"read a file","name":"read"}}]`} {
		c, _ := enc.Count(text)
		want += c
	}
	if err != nil || n == nil || *n != int64(want) {
		t.Fatalf("input = %v, %v; want %d", n, err, want)
	}
	if n, err := kiroRequestInput([]byte(`{"request":{"conversationState":{"currentMessage":{"userInputMessage":{"images":[{"bytes":"ignore"}]}}}}}`), enc); err != nil || n != nil {
		t.Fatalf("image-only input = %v, %v", n, err)
	}
}

func TestKiroEstimatesKeepCollectedZeroAndPartialCoverage(t *testing.T) {
	r := accounting.NewRecorder()
	var e kiroOutputEstimator
	e.observe("text", json.RawMessage(`{"content":"hello"}`))
	zero := int64(0)
	if err := recordKiroEstimates(r, adapter.NewTokenCounters(nil, &zero, nil), &e, ""); err != nil {
		t.Fatal(err)
	}
	a, err := r.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	x := a.Entries[0].Tokens
	if x.Output.Collected == nil || x.Output.Collected.Value != "0" || x.Output.Estimated == nil || x.Output.Estimated.Value != "1" || x.Output.Estimated.Method == "" || x.Output.Estimated.Version == "" || x.Output.Estimated.Fidelity != "proxy" {
		t.Fatalf("output evidence = %+v", x.Output)
	}
	if x.Input.Estimated != nil || x.Total.Estimated != nil || x.CacheRead.Estimated != nil || x.CacheWrite.Estimated != nil {
		t.Fatalf("invented dimensions: %+v", x)
	}
	if len(a.Coverage) != 1 || a.Coverage[0].State != "partial" {
		t.Fatalf("coverage = %+v", a.Coverage)
	}
}

func TestKiroBadCaptureDoesNotLoseCollectedOrOutputEvidence(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{"request":`), 0600); err != nil {
		t.Fatal(err)
	}
	r := accounting.NewRecorder()
	var e kiroOutputEstimator
	e.observe("text", json.RawMessage(`{"content":"hello"}`))
	in := int64(7)
	if err := recordKiroEstimates(r, adapter.NewTokenCounters(&in, nil, nil), &e, dir); err == nil {
		t.Fatal("expected malformed capture diagnostic")
	}
	a, err := r.Finish()
	if err != nil {
		t.Fatal(err)
	}
	x := a.Entries[0].Tokens
	if x.Input.Collected.Value != "7" || x.Input.Estimated != nil || x.Output.Estimated.Value != "1" {
		t.Fatalf("accounting = %+v", x)
	}
}

func TestKiroStreamCapturesInputAndRemovesPrivateFilesOnFailure(t *testing.T) {
	bin := buildAgentKiro(t)
	workDir, binDir := t.TempDir(), t.TempDir()
	capturePath := filepath.Join(workDir, "capture-path")
	cli := filepath.Join(binDir, "kiro-cli")
	script := `#!/bin/sh
printf '%s' "$KIRO_DUMP_REQUESTS_DIR" > capture-path
/bin/cat > "$KIRO_DUMP_REQUESTS_DIR/request.json" <<'EOF'
{"request":{"conversationState":{"currentMessage":{"userInputMessage":{"content":"hello"}}}}}
EOF
printf '%s\n' '{"type":"text","data":{"content":"world"}}' '{"type":"runError","data":{"message":"fixture failure"}}'
`
	if err := os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	req, _ := json.Marshal(map[string]any{"contractVersion": "1", "workspace": workDir, "workDir": workDir, "executor": "agent/kiro", "task": "estimate", "hostExecutionAllowed": true})
	cmd := exec.Command(bin)
	cmd.Env = []string{"PATH=" + binDir, "HOME=" + workDir, "KIRO_CLI=" + cli, "KIRO_DUMP_REQUESTS_DIR=/unrelated/path", "KIRO_DUMP_REQUESTS=0"}
	cmd.Stdin = strings.NewReader(string(req))
	b, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Error      string               `json:"error"`
		Accounting *contract.Accounting `json:"accounting"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	if !strings.Contains(resp.Error, "fixture failure") || resp.Accounting == nil {
		t.Fatalf("response = %s", b)
	}
	x := resp.Accounting.Entries[0].Tokens
	if x.Input.Estimated.Value != "1" || x.Output.Estimated.Value != "1" {
		t.Fatalf("accounting = %s", b)
	}
	p, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(string(p)); !os.IsNotExist(err) {
		t.Fatalf("capture directory remains: %q, %v", p, err)
	}
}
