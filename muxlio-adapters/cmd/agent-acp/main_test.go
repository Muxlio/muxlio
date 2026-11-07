package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
)

func TestResultPortsAndEphemeralProgress(t *testing.T) {
	workspace, scratch := t.TempDir(), t.TempDir()
	program := filepath.Join(workspace, "fixture-agent")
	// Capture the actual ACP requests, including output instructions. Context
	// size and cost are observable updates, never reported token usage.
	script := `#!/bin/sh
[ "$1" = '--stdio' ] || exit 9
[ -z "$MUXLIO_LOG_FD" ] || exit 10
IFS= read -r request
printf '%s\n' "$request" > requests.jsonl
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}}'
IFS= read -r request
printf '%s\n' "$request" >> requests.jsonl
printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"sessionId":"temporary-session"}}'
IFS= read -r request
printf '%s\n' "$request" >> requests.jsonl
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"temporary-session","update":{"sessionUpdate":"usage_update","used":100,"size":200000,"cost":{"amount":0.01,"currency":"USD"}}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"temporary-session","update":{"sessionUpdate":"plan","entries":[]}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"temporary-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"completed"}}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"temporary-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"read","status":"completed","content":[{"type":"content","content":{"type":"text","text":"tool result"}}]}}}'
printf 'true' > "$TEST_OUTPUT_DIR/valid"
printf 'not json' > "$TEST_OUTPUT_DIR/invalid"
printf 'artifact' > "$TEST_OUTPUT_DIR/report.txt"
printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}'
`
	if err := os.WriteFile(program, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_OUTPUT_DIR", scratch)
	log, err := os.CreateTemp(t.TempDir(), "progress")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	t.Setenv("MUXLIO_LOG_FD", strconv.Itoa(int(log.Fd())))
	raw, _ := json.Marshal(params{Program: program, Args: []string{"--stdio"}})
	req := &adapter.Request{Workspace: workspace, WorkDir: scratch, HostExecutionAllowed: true, Params: raw, Task: "write results",
		Outputs: map[string]adapter.OutputDecl{"valid": {}, "invalid": {}, "absent": {}, "report": {Path: "report.txt", Mime: "text/plain"}}}
	result := execute(req)
	if result.Error != "" || result.Outputs["valid"] != true || result.OutputErrors["invalid"] == "" || result.OutputErrors["absent"] == "" {
		t.Fatalf("result ports: %+v", result)
	}
	if result.Session != nil || result.SessionID != "" || result.Usage != nil {
		t.Fatalf("invented session/usage: %+v", result)
	}
	if result.Outputs["report"].(map[string]any)["data"] != "artifact" || result.Outputs["_text"].(map[string]any)["data"] != "completed" {
		t.Fatalf("output lost: %+v", result)
	}
	progress, err := os.ReadFile(log.Name())
	if err != nil {
		t.Fatal(err)
	}
	var observed strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(string(progress)), "\n") {
		var event contract.Progress
		if err := json.Unmarshal([]byte(line), &event); err != nil || event.Stream == "" || event.OperationRef != nil {
			t.Fatalf("invalid progress: %s", line)
		}
		observed.WriteString(event.Text)
	}
	for _, expected := range []string{"protocol 1", "plan", "usage_update", "tool_call_update", "completed"} {
		if !strings.Contains(observed.String(), expected) {
			t.Fatalf("missing observable %q: %s", expected, observed.String())
		}
	}
	requests, err := os.ReadFile(filepath.Join(workspace, "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(requests), scratch) || !strings.Contains(string(requests), workspace) || !strings.Contains(string(requests), "write results") {
		t.Fatalf("missing prompt instructions: %s", requests)
	}
}

func TestUnsupportedCapabilities(t *testing.T) {
	m, err := contract.ParseManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range []string{"permission", "session", "sessionInput", "liveInput", "typedResult", "remoteTask", "usage"} {
		t.Run(capability, func(t *testing.T) {
			req := &adapter.Request{Executor: "agent/acp", RequiredCapabilities: map[string]string{capability: contract.Version}}
			if err := req.Check(m); err == nil || !strings.Contains(err.Error(), capability) {
				t.Fatalf("unsupported capability not explicit: %v", err)
			}
		})
	}
	if !m.RequiresHostExecution {
		t.Fatal("host execution must require operator authorization")
	}
}

func TestInvalidInvocation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*adapter.Request)
	}{
		{"unauthorized", func(r *adapter.Request) { r.HostExecutionAllowed = false }},
		{"timeout", func(r *adapter.Request) { r.Params = json.RawMessage(`{"timeout":"invalid"}`) }},
		{"output escape", func(r *adapter.Request) { r.Outputs = map[string]adapter.OutputDecl{"bad": {Path: "../escape"}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &adapter.Request{Workspace: t.TempDir(), WorkDir: t.TempDir(), HostExecutionAllowed: true}
			tc.mutate(r)
			if result := execute(r); result.Error == "" {
				t.Fatalf("invalid request accepted: %+v", result)
			}
		})
	}
}
