package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

const initialized = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"futureFeature":{}}}}`
const sessionCreated = `{"jsonrpc":"2.0","id":2,"result":{"sessionId":"session"}}`
const complete = `{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}`

func TestLifecycle(t *testing.T) {
	updates := []string{
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello"}}`,
		`{"sessionUpdate":"tool_call","toolCallId":"tool","title":"read","status":"pending"}`,
		`{"sessionUpdate":"plan","entries":[]}`,
		`{"sessionUpdate":"future_update","data":true}`,
	}
	stream := initialized + "\n" + sessionCreated + "\n"
	for _, update := range updates {
		stream += `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session","update":` + update + "}}\n"
	}
	var requests bytes.Buffer
	client := New(strings.NewReader(stream+complete+"\n"), &requests)
	var observed []string
	client.Update = func(raw json.RawMessage) error { observed = append(observed, string(raw)); return nil }
	if err := client.Execute(context.Background(), "/workspace", "task and result ports"); err != nil {
		t.Fatal(err)
	}
	if len(observed) != len(updates) || string(client.Capabilities["loadSession"]) != "true" {
		t.Fatalf("updates/capabilities lost: %v %+v", observed, client.Capabilities)
	}
	decoder := json.NewDecoder(&requests)
	for i, method := range []string{"initialize", "session/new", "session/prompt"} {
		var m message
		if err := decoder.Decode(&m); err != nil || m.Method != method || string(m.ID) != fmt.Sprint(i+1) {
			t.Fatalf("request: %+v %v", m, err)
		}
		switch method {
		case "initialize":
			if !bytes.Contains(m.Params, []byte(`"clientCapabilities":{}`)) {
				t.Fatalf("unimplemented capabilities advertised: %s", m.Params)
			}
		case "session/new":
			if !bytes.Contains(m.Params, []byte(`"cwd":"/workspace"`)) {
				t.Fatalf("cwd lost: %s", m.Params)
			}
		case "session/prompt":
			if !bytes.Contains(m.Params, []byte(`"text":"task and result ports"`)) {
				t.Fatalf("prompt lost: %s", m.Params)
			}
		}
	}
}

func TestFailures(t *testing.T) {
	for name, stream := range map[string]string{
		"version":                   `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":2,"agentCapabilities":{}}}`,
		"missing capabilities":      `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}`,
		"invalid capability":        `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":"yes"}}}`,
		"invalid prompt capability": `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{"promptCapabilities":{"image":"yes"}}}}`,
		"invalid MCP capability":    `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{"mcpCapabilities":{"http":{}}}}}`,
		"invalid prompt shape":      `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{"promptCapabilities":[]}}}`,
		"authentication":            initialized + "\n" + `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"auth_required"}}`,
		"null session":              initialized + "\n" + `{"jsonrpc":"2.0","id":2,"result":null}`,
		"missing session":           initialized + "\n" + `{"jsonrpc":"2.0","id":2,"result":{}}`,
		"missing stop":              initialized + "\n" + sessionCreated + "\n" + `{"jsonrpc":"2.0","id":3,"result":{}}`,
		"wrong id":                  `{"jsonrpc":"2.0","id":20,"result":{}}`,
		"invalid envelope":          `{"jsonrpc":"2.0","id":1,"result":{},"error":{}}`,
		"string id type mismatch":   `{"jsonrpc":"2.0","id":"1","result":{"protocolVersion":1,"agentCapabilities":{}}}`,
		"malformed":                 `{broken}`,
		"empty":                     "",
		"oversized":                 strings.Repeat("x", (4<<20)+1),
	} {
		t.Run(name, func(t *testing.T) {
			var requests bytes.Buffer
			client := New(strings.NewReader(stream+"\n"), &requests)
			if err := client.Execute(context.Background(), "/workspace", "prompt"); err == nil {
				t.Fatal("invalid protocol accepted")
			}
		})
	}
}

func TestPermissionDenied(t *testing.T) {
	stream := initialized + "\n" + sessionCreated + "\n" + `{"jsonrpc":"2.0","id":"permission","method":"session/request_permission","params":{"sessionId":"session","options":[{"optionId":"allow","kind":"allow_once"}]}}` + "\n"
	var requests bytes.Buffer
	client := New(strings.NewReader(stream), &requests)
	err := client.Execute(context.Background(), "/workspace", "prompt")
	if err == nil || !strings.Contains(err.Error(), "permission") || !strings.Contains(requests.String(), `"outcome":"cancelled"`) || strings.Contains(requests.String(), `"optionId":"allow"`) {
		t.Fatalf("permission was not explicitly denied: %v %s", err, &requests)
	}
}

func TestCancellation(t *testing.T) {
	for _, acknowledge := range []bool{true, false} {
		t.Run(fmt.Sprint(acknowledge), func(t *testing.T) {
			local, remote := net.Pipe()
			defer local.Close()
			defer remote.Close()
			_ = local.SetDeadline(time.Now().Add(5 * time.Second))
			_ = remote.SetDeadline(time.Now().Add(5 * time.Second))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			observed := make(chan error, 1)
			go func() {
				decoder := json.NewDecoder(remote)
				for _, response := range []string{initialized, sessionCreated} {
					var request message
					if err := decoder.Decode(&request); err != nil {
						observed <- err
						return
					}
					if _, err := fmt.Fprintln(remote, response); err != nil {
						observed <- err
						return
					}
				}
				var request message
				if err := decoder.Decode(&request); err != nil {
					observed <- err
					return
				}
				if _, err := fmt.Fprintln(remote, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session","update":{"sessionUpdate":"plan","entries":[]}}}`); err != nil {
					observed <- err
					return
				}
				request = message{}
				if err := decoder.Decode(&request); err != nil {
					observed <- err
					return
				}
				if request.Method != "session/cancel" || string(request.Params) != `{"sessionId":"session"}` || len(request.ID) > 0 {
					observed <- fmt.Errorf("invalid cancel: %+v", request)
					return
				}
				if acknowledge {
					_, _ = fmt.Fprintln(remote, `{"jsonrpc":"2.0","id":3,"result":{"stopReason":"cancelled"}}`)
				}
				observed <- nil
			}()
			client := New(local, local)
			client.Update = func(json.RawMessage) error { cancel(); return nil }
			if err := client.Execute(ctx, "/workspace", "prompt"); err == nil || !strings.Contains(err.Error(), "cancelled") {
				t.Fatalf("cancel: %v", err)
			}
			if err := <-observed; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCancellationWithBlockedWriter(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := New(local, local).Execute(ctx, "/workspace", "prompt"); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("blocked write: %v", err)
	}
}

func TestModelSelectedFromPendingConfigOptionUpdate(t *testing.T) {
	pre := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session","update":{"sessionUpdate":"config_option_update","configOptions":[{"category":"model","currentValue":"from-pending"}]}}}` + "\n"
	stream := initialized + "\n" + pre + sessionCreated + "\n" + complete + "\n"
	var requests bytes.Buffer
	var models []string
	client := New(strings.NewReader(stream), &requests)
	client.ModelSelected = func(id string) { models = append(models, id) }
	if err := client.Execute(context.Background(), "/workspace", "prompt"); err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0] != "from-pending" {
		t.Fatalf("model selection from pre-response update lost: %v", models)
	}
}

// JSON numbers may use equivalent encodings on the wire.
func TestResponseIDFormats(t *testing.T) {
	for name, completeLine := range map[string]string{
		"float":  `{"jsonrpc":"2.0","id":3.0,"result":{"stopReason":"end_turn"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			stream := initialized + "\n" + sessionCreated + "\n" + completeLine + "\n"
			var requests bytes.Buffer
			client := New(strings.NewReader(stream), &requests)
			if err := client.Execute(context.Background(), "/workspace", "prompt"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Nested capabilities permit ACP metadata and future extensions, which need
// not be booleans. Known capability values must still have their defined type.
func TestCapabilityExtensions(t *testing.T) {
	initialize := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{"promptCapabilities":{"image":true,"_meta":{"vendor/feature":{"enabled":true}},"future":{"mode":"text"}},"mcpCapabilities":{"http":false,"_meta":{"vendor/version":1},"future":[1,2]}}}}`
	var requests bytes.Buffer
	client := New(strings.NewReader(initialize+"\n"+sessionCreated+"\n"+complete+"\n"), &requests)
	if err := client.Execute(context.Background(), "/workspace", "prompt"); err != nil {
		t.Fatalf("valid capability extensions rejected: %v", err)
	}
	if !bytes.Contains(client.Capabilities["promptCapabilities"], []byte(`"vendor/feature"`)) {
		t.Fatal("capability extension metadata lost")
	}
}
