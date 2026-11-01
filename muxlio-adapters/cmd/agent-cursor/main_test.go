package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCursorAgentEnvSetsFileCredentialStore(t *testing.T) {
	t.Setenv("AGENT_CLI_CREDENTIAL_STORE", "keychain")
	t.Setenv("MUXLIO_LOG_FD", "3")
	env := cursorAgentEnv()
	if got := envLookup(env, "AGENT_CLI_CREDENTIAL_STORE"); got != "file" {
		t.Fatalf("AGENT_CLI_CREDENTIAL_STORE = %q, want file", got)
	}
	if envLookup(env, "MUXLIO_LOG_FD") != "" {
		t.Fatalf("cursor agent env must not inherit MUXLIO_LOG_FD")
	}
}

func TestCursorAdapterChildUsesFileCredentialsWithoutSecurity(t *testing.T) {
	bin := buildAgentCursor(t)
	workDir := t.TempDir()
	binDir := t.TempDir()
	securityMarker := filepath.Join(binDir, "security-invoked")
	fakeSecurity := "#!/bin/sh\necho security-invoked >&2\ntouch " + securityMarker + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "security"), []byte(fakeSecurity), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeAgent := "#!/bin/sh\n" +
		"if [ \"$AGENT_CLI_CREDENTIAL_STORE\" != file ]; then\n" +
		"  echo \"AGENT_CLI_CREDENTIAL_STORE=$AGENT_CLI_CREDENTIAL_STORE\" >&2\n" +
		"  exit 2\n" +
		"fi\n" +
		"echo '{\"type\":\"result\",\"result\":\"ok\"}'\n"
	agentPath := filepath.Join(binDir, "agent")
	if err := os.WriteFile(agentPath, []byte(fakeAgent), 0o755); err != nil {
		t.Fatal(err)
	}

	stdin, _ := json.Marshal(map[string]any{
		"workspace": workDir,
		"executor":  "agent/cursor",
		"task":      "noop",
		"workDir":   workDir,
		"nodeId":    "n1",
	})
	cmd := exec.Command(bin)
	cmd.Env = []string{
		"PATH=" + binDir,
		"HOME=" + workDir,
		"CURSOR_AGENT=" + agentPath,
	}
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("adapter process failed: %v", err)
	}
	if _, err := os.Stat(securityMarker); err == nil {
		t.Fatal("adapter or child invoked security unlock-keychain helper")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat security marker: %v", err)
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected adapter error: %q", resp.Error)
	}
}

func envLookup(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return strings.TrimPrefix(kv, prefix)
		}
	}
	return ""
}

func buildAgentCursor(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "agent-cursor")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = wd
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build agent-cursor: %v\n%s", err, out)
	}
	return bin
}
