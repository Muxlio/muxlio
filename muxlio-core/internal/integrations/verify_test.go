package integrations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func evidence(client string, failed bool) string {
	var events []string
	for i, tool := range []string{"server_health", "get_workers"} {
		switch client {
		case "codex":
			events = append(events, fmt.Sprintf(`{"type":"item.completed","item":{"type":"mcp_tool_call","server":"muxlio","tool":%q,"status":"completed","result":{"isError":%t},"error":null}}`, tool, failed))
		case "kiro":
			events = append(events, fmt.Sprintf(`{"type":"sessionUpdate","data":{"update":{"sessionUpdate":"tool_call","toolCallId":"%d","title":"muxlio___%s"}}}`, i, tool), fmt.Sprintf(`{"type":"sessionUpdate","data":{"update":{"sessionUpdate":"tool_call_update","toolCallId":"%d","status":"completed","rawOutput":{"isError":%t}}}}`, i, failed))
		case "cursor":
			events = append(events, fmt.Sprintf(`{"type":"tool_call","subtype":"completed","tool_call":{"function":{"name":"mcp__muxlio__%s"},"result":{"success":{"isError":%t}}}}`, tool, failed))
		default:
			events = append(events, fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"%d","name":"mcp__muxlio__%s"}]}}`, i, tool), fmt.Sprintf(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"%d","is_error":%t,"content":"ok"}]}}`, i, failed))
		}
	}
	return strings.Join(events, "\n") + "\n"
}

func TestToolEvidenceRequiresSuccessfulCalls(t *testing.T) {
	for _, client := range Clients {
		t.Run(client, func(t *testing.T) {
			for _, failed := range []bool{false, true} {
				ok, err := toolEvidence(strings.NewReader(evidence(client, failed)), client)
				if err != nil || ok == failed {
					t.Fatalf("failed=%t: %t %v", failed, ok, err)
				}
			}
			for _, fake := range []string{`{"type":"result","result":"Called server_health and get_workers successfully"}`, strings.ReplaceAll(evidence(client, false), "muxlio", "other"), strings.ReplaceAll(evidence(client, false), "get_workers", "other")} {
				if ok, _ := toolEvidence(strings.NewReader(fake), client); ok {
					t.Fatal("accepted absent or unrelated tool calls")
				}
			}
		})
	}
}

func TestVerifyUsesClientAndWithholdsOutput(t *testing.T) {
	for _, client := range Clients {
		t.Run(client, func(t *testing.T) {
			o := fixture(t, client, "project", "http")
			name := map[string]string{"claude": "claude", "codex": "codex", "cursor": "cursor", "kiro": "kiro-cli"}[client]
			dir := t.TempDir()
			script := "#!/bin/sh\ncase \"$*\" in *server_health*get_workers*) ;; *) exit 9;; esac\ncat <<'EVENTS'\n" + evidence(client, false) + "EVENTS\n"
			if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := VerifyClient(ctx, o); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\necho SECRET >&2\necho SECRET\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := VerifyClient(ctx, o); err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("error leaked output or claimed success: %v", err)
			}
		})
	}
}

func TestVerificationRejectsProjectOverrideOfUserScope(t *testing.T) {
	o := fixture(t, "codex", "project", "http")
	if _, err := Apply(o, "add"); err != nil {
		t.Fatal(err)
	}
	o.Scope = "user"
	err := VerifyClient(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "override user scope") {
		t.Fatalf("unexpected result: %v", err)
	}
}

func TestCursorNativeMCPToolEvidence(t *testing.T) {
	for _, failed := range []bool{false, true} {
		var events []string
		for _, tool := range []string{"server_health", "get_workers"} {
			// Cursor serializes its MCP protobuf oneof as mcpToolCall, unlike
			// the function envelope used by some other tools/versions.
			events = append(events, fmt.Sprintf(`{"type":"tool_call","subtype":"completed","tool_call":{"mcpToolCall":{"args":{"providerIdentifier":"muxlio","toolName":%q},"result":{"success":{"content":[],"isError":%t}}}}}`, tool, failed))
		}
		stream := strings.Join(events, "\n")
		if ok, err := toolEvidence(strings.NewReader(stream), "cursor"); err != nil || ok == failed {
			t.Fatalf("failed=%t: %t %v", failed, ok, err)
		}
		if ok, _ := toolEvidence(strings.NewReader(strings.ReplaceAll(stream, `"muxlio"`, `"other"`)), "cursor"); ok {
			t.Fatal("accepted another server's native MCP calls")
		}
	}
}

func TestToolEvidenceRejectsEncodedMCPErrors(t *testing.T) {
	for _, client := range []string{"claude", "kiro"} {
		stream := evidence(client, false)
		if client == "claude" {
			stream = strings.ReplaceAll(stream, `"content":"ok"`, `"content":[{"type":"text","text":"{\"isError\":true}"}]`)
		} else {
			stream = strings.ReplaceAll(stream, `"rawOutput":{"isError":false}`, `"rawOutput":"{\"isError\":true}"`)
		}
		if ok, _ := toolEvidence(strings.NewReader(stream), client); ok {
			t.Fatalf("%s accepted a JSON-encoded MCP error", client)
		}
	}
}

func TestClaudeVerificationRejectsLocalScopeOverride(t *testing.T) {
	for _, scope := range []string{"project", "user"} {
		o := fixture(t, "claude", scope, "http")
		workspace, err := filepath.EvalSymlinks(o.Workspace)
		if err != nil {
			t.Fatal(err)
		}
		user := o
		user.Scope = "user"
		path, _ := user.Path()
		writeFixture(t, path, []byte(fmt.Sprintf(`{"projects":{%q:{"mcpServers":{"muxlio":{"command":"custom"}}}}}`, workspace)))
		if err := VerifyClient(context.Background(), o); err == nil || !strings.Contains(err.Error(), "local-scope") {
			t.Fatalf("%s accepted a shadowed registration: %v", scope, err)
		}
	}
}

func TestVerificationDeadlineWithInheritedStdout(t *testing.T) {
	o := fixture(t, "cursor", "project", "http")
	dir := t.TempDir()
	// The child outlives the client and inherits its stdout pipe.
	if err := os.WriteFile(filepath.Join(dir, "cursor"), []byte("#!/bin/sh\nsleep 2 &\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := VerifyClient(ctx, o); err == nil {
		t.Fatal("timed-out client was verified")
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("verification waited for the inherited stdout pipe: %s", elapsed)
	}
}
