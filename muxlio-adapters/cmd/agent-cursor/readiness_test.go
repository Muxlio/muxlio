package main

import (
	"encoding/json"
	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCursorProbeUsesExecutionCredentials(t *testing.T) {
	binary := buildAgentCursor(t)
	home := t.TempDir()
	cli := filepath.Join(t.TempDir(), "agent")
	script := `#!/bin/sh
case "$1" in
 --version) echo '2026.11.11-b912975'; exit 0 ;;
 status) [ "$AGENT_CLI_CREDENTIAL_STORE" = file ] || { echo 'not logged in'; exit 1; }; echo 'logged in'; exit 0 ;;
esac
[ "$AGENT_CLI_CREDENTIAL_STORE" = file ] || exit 1
echo '{"type":"result","result":"ok"}'
`
	if err := os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	environment := []string{"PATH=/bin:/usr/bin", "HOME=" + home, "CURSOR_AGENT=" + cli}
	input, _ := json.Marshal(adapter.Request{Authorization: fixtureAuthorization(), Executor: "agent/cursor", Workspace: home, WorkDir: home, Task: "noop"})
	invocation := exec.Command(binary)
	invocation.Env = environment
	invocation.Stdin = strings.NewReader(string(input))
	raw, err := invocation.Output()
	if err != nil {
		t.Fatal(err)
	}
	var result adapter.Response
	if err := json.Unmarshal(raw, &result); err != nil || result.Error != "" {
		t.Fatalf("execution failed: %s %v", raw, err)
	}
	probe := exec.Command(binary, adapter.ProbeFlag)
	probe.Env = environment
	raw, err = probe.Output()
	if err != nil {
		t.Fatal(err)
	}
	var report contract.ProbeResult
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if !report.Ready {
		t.Fatalf("normal execution succeeded, but identical readiness probe rejected credentials: %s", raw)
	}
}
