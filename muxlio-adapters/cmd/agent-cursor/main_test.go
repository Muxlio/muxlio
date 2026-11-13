package main

import (
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/adapter"
)

func TestSubtypeIndicatesError(t *testing.T) {
	for value, want := range map[string]bool{
		"success":        false,
		"no_error":       false,
		"model_call":     false,
		"error":          true,
		"error_max_turn": true,
	} {
		if got := subtypeIndicatesError(value); got != want {
			t.Fatalf("subtypeIndicatesError(%q) = %v, want %v", value, got, want)
		}
	}
}

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
	if err := os.WriteFile(agentPath, []byte(versionedFixture(fakeAgent)), 0o755); err != nil {
		t.Fatal(err)
	}

	stdin, _ := json.Marshal(map[string]any{
		"authorization": fixtureAuthorization(),
		"workspace":     workDir,
		"executor":      "agent/cursor",
		"task":          "noop",
		"workDir":       workDir,
		"nodeId":        "n1",
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

func TestCursorAdapterReportsUsageFromSuccessfulResult(t *testing.T) {
	got := runCursorFixture(t, `echo '{"type":"result","result":"done","usage":{"inputTokens":100,"outputTokens":40,"cacheReadTokens":12}}'`)
	if got.Error != "" {
		t.Fatalf("unexpected adapter error: %q", got.Error)
	}
	if fixtureTokens(t, got.Accounting) == nil || fixtureTokens(t, got.Accounting).Input != 112 || fixtureTokens(t, got.Accounting).Output != 40 || fixtureTokens(t, got.Accounting).Cached != 12 {
		t.Fatalf("usage = %+v, want input=112 output=40 cached=12", fixtureTokens(t, got.Accounting))
	}
}

func TestCursorAdapterReportsUsageFromPinnedRunnerResultShape(t *testing.T) {
	got := runCursorFixture(t, `echo '{"type":"result","subtype":"success","result":"done","usage":{"inputTokens":49,"outputTokens":222,"cacheReadTokens":9728,"cacheWriteTokens":0}}'`)
	if got.Error != "" {
		t.Fatalf("unexpected adapter error: %q", got.Error)
	}
	if fixtureTokens(t, got.Accounting) == nil || fixtureTokens(t, got.Accounting).Input != 9777 || fixtureTokens(t, got.Accounting).Output != 222 || fixtureTokens(t, got.Accounting).Cached != 9728 {
		t.Fatalf("usage = %+v, want input=9777 output=222 cached=9728", fixtureTokens(t, got.Accounting))
	}
}

func TestCursorAdapterMergesPartialResultUsage(t *testing.T) {
	script := `echo '{"type":"usage","subtype":"model_call","usage":{"inputTokens":100,"outputTokens":40,"cacheReadTokens":12}}'
echo '{"type":"result","result":"done","usage":{"inputTokens":110}}'`
	got := runCursorFixture(t, script)
	if got.Error != "" {
		t.Fatalf("unexpected adapter error: %q", got.Error)
	}
	if fixtureTokens(t, got.Accounting) == nil || fixtureTokens(t, got.Accounting).Input != 110 || fixtureTokens(t, got.Accounting).Output != 40 || fixtureTokens(t, got.Accounting).Cached != 12 {
		t.Fatalf("usage = %+v, want input=110 output=40 cached=12", fixtureTokens(t, got.Accounting))
	}
}

func TestCursorAdapterPreservesUsageFromFailedResult(t *testing.T) {
	got := runCursorFixture(t, `echo '{"type":"result","is_error":true,"error":"request failed","usage":{"inputTokens":7,"outputTokens":3,"cacheReadTokens":2}}'`+"\nexit 1")
	if got.Error == "" {
		t.Fatal("expected adapter error")
	}
	if fixtureTokens(t, got.Accounting) == nil || fixtureTokens(t, got.Accounting).Input != 9 || fixtureTokens(t, got.Accounting).Output != 3 || fixtureTokens(t, got.Accounting).Cached != 2 {
		t.Fatalf("usage = %+v, want input=9 output=3 cached=2", fixtureTokens(t, got.Accounting))
	}
}

func TestCursorAdapterReportsUsageFromUsageEvent(t *testing.T) {
	script := `echo '{"type":"usage","subtype":"model_call","usage":{"inputTokens":100,"outputTokens":40,"cacheReadTokens":12}}'
echo '{"type":"usage","subtype":"model_call","usage":{"inputTokens":25,"outputTokens":9,"cacheReadTokens":3}}'
echo '{"type":"result","subtype":"success","result":"done"}'`
	got := runCursorFixture(t, script)
	if got.Error != "" {
		t.Fatalf("unexpected adapter error: %q", got.Error)
	}
	if fixtureTokens(t, got.Accounting) == nil || fixtureTokens(t, got.Accounting).Input != 140 || fixtureTokens(t, got.Accounting).Output != 49 || fixtureTokens(t, got.Accounting).Cached != 15 {
		t.Fatalf("usage = %+v, want aggregated input=140 output=49 cached=15", fixtureTokens(t, got.Accounting))
	}
}

func TestCursorAdapterResultUsageOverridesUsageEvents(t *testing.T) {
	script := `echo '{"type":"usage","subtype":"model_call","usage":{"inputTokens":100,"outputTokens":40,"cacheReadTokens":12}}'
echo '{"type":"result","usage":{"inputTokens":110,"outputTokens":45,"cacheReadTokens":14},"result":"done"}'`
	got := runCursorFixture(t, script)
	if got.Error != "" {
		t.Fatalf("unexpected adapter error: %q", got.Error)
	}
	if fixtureTokens(t, got.Accounting) == nil || fixtureTokens(t, got.Accounting).Input != 124 || fixtureTokens(t, got.Accounting).Output != 45 || fixtureTokens(t, got.Accounting).Cached != 14 {
		t.Fatalf("usage = %+v, want result totals input=124 output=45 cached=14", fixtureTokens(t, got.Accounting))
	}
}

func TestCursorAccountingOverflowPreservesOutcomeAndConfirmedConsumption(t *testing.T) {
	max := int64(9223372036854775807)
	script := `echo '{"type":"usage","subtype":"model_call","usage":{"inputTokens":` + fmt.Sprint(max) + `,"outputTokens":1,"cacheReadTokens":0}}'
echo '{"type":"usage","subtype":"model_call","usage":{"inputTokens":1,"outputTokens":0,"cacheReadTokens":0}}'
echo '{"type":"usage","subtype":"model_call","usage":{"inputTokens":0,"outputTokens":0,"cacheReadTokens":0}}'
echo '{"type":"result","subtype":"success","result":"done"}'`
	got := runCursorFixture(t, script)
	if got.Error != "" {
		t.Fatal("accounting anomaly changed successful execution outcome")
	}
	if got.Accounting == nil || got.Accounting.Coverage[0].State == "complete" {
		t.Fatal("overflow claimed complete consumption")
	}
	if fixtureTokens(t, got.Accounting) == nil || fixtureTokens(t, got.Accounting).Input != max {
		t.Fatalf("usage = %+v, want preserved input=%d on overflow failure", fixtureTokens(t, got.Accounting), max)
	}
}

func TestCursorAdapterPreservesUsageWhenCacheReadNull(t *testing.T) {
	got := runCursorFixture(t, `echo '{"type":"result","result":"done","usage":{"inputTokens":100,"outputTokens":40,"cacheReadTokens":null}}'`)
	if got.Error != "" {
		t.Fatalf("unexpected adapter error: %q", got.Error)
	}
	if fixtureTokens(t, got.Accounting) == nil || fixtureTokens(t, got.Accounting).Input != 100 || fixtureTokens(t, got.Accounting).Output != 40 {
		t.Fatalf("usage = %+v, want input=100 output=40", fixtureTokens(t, got.Accounting))
	}
}

func TestCursorAdapterPreservesUsageWhenCacheWriteNull(t *testing.T) {
	got := runCursorFixture(t, `echo '{"type":"result","result":"done","usage":{"inputTokens":100,"outputTokens":40,"cacheReadTokens":12,"cacheWriteTokens":null}}'`)
	if got.Error != "" {
		t.Fatalf("unexpected adapter error: %q", got.Error)
	}
	if fixtureTokens(t, got.Accounting) == nil || fixtureTokens(t, got.Accounting).Input != 112 || fixtureTokens(t, got.Accounting).Output != 40 || fixtureTokens(t, got.Accounting).Cached != 12 {
		t.Fatalf("usage = %+v, want input=112 output=40 cached=12", fixtureTokens(t, got.Accounting))
	}
}

func TestCursorAdapterLeavesUsageAbsentWhenNotReported(t *testing.T) {
	got := runCursorFixture(t, `echo '{"type":"result","result":"done"}'`)
	if got.Error != "" {
		t.Fatalf("unexpected adapter error: %q", got.Error)
	}
	if fixtureTokens(t, got.Accounting) != nil {
		t.Fatalf("usage = %+v, want absent", fixtureTokens(t, got.Accounting))
	}
}

func TestCursorAdapterPreservesStderrWithoutProgressChannel(t *testing.T) {
	t.Setenv("MUXLIO_LOG_FD", "")
	for _, script := range []string{
		`echo 'diagnostic details' >&2; echo '{"type":"result","result":"done"}'`,
		`echo 'diagnostic details' >&2; exit 1`,
	} {
		got := runCursorFixture(t, script)
		if !strings.Contains(got.Logs, "diagnostic details") {
			t.Fatalf("stderr lost: %+v", got)
		}
	}
}

func runCursorFixture(t *testing.T, script string) adapter.Response {
	t.Helper()
	bin := buildAgentCursor(t)
	workDir := t.TempDir()
	fakeCLI := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(fakeCLI, []byte(versionedFixture("#!/bin/sh\n"+script+"\n")), 0o755); err != nil {
		t.Fatal(err)
	}
	stdin, _ := json.Marshal(map[string]any{"authorization": fixtureAuthorization(), "workspace": workDir, "executor": "agent/cursor", "task": "do nothing", "workDir": workDir, "nodeId": "n1"})
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "CURSOR_AGENT="+fakeCLI)
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("adapter process failed: %v\n%s", err, out)
	}
	var resp adapter.Response
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid adapter JSON: %v\n%s", err, out)
	}
	return resp
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
