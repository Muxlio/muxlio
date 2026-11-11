package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
)

func TestAdapterProcess(t *testing.T) {
	if os.Getenv("MUXLIO_A2A_TEST_PROCESS") != "1" {
		return
	}
	main()
	os.Exit(0)
}
func invoke(t *testing.T, req adapter.Request) adapter.Response {
	t.Helper()
	if req.WorkDir == "" {
		req.WorkDir = t.TempDir()
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAdapterProcess$")
	cmd.Env = append(os.Environ(), "MUXLIO_A2A_TEST_PROCESS=1")
	cmd.Stdin = strings.NewReader(string(raw))
	raw, err = cmd.Output()
	if err != nil {
		t.Fatalf("adapter process: %v", err)
	}
	result, err := contract.ParseResult(raw)
	if err != nil {
		t.Fatalf("invalid result %s: %v", raw, err)
	}
	return result
}
func taskResult(state string) map[string]any {
	return map[string]any{"kind": "task", "id": "durable-task", "contextId": "remote-context", "status": map[string]any{"state": state}, "artifacts": []any{
		map[string]any{"name": "answer", "parts": []any{map[string]any{"kind": "data", "data": map[string]any{"ok": true}}}},
		map[string]any{"name": "report", "parts": []any{map[string]any{"kind": "file", "file": map[string]any{"bytes": "cmVwb3J0", "mimeType": "text/plain"}}}},
	}}
}

type fixture struct {
	metadata   any
	mu         sync.Mutex
	server     *httptest.Server
	methods    []string
	state      string
	card       map[string]any
	disconnect bool
	rpcError   bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{state: "working"}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/.well-known/agent-card.json" {
			json.NewEncoder(w).Encode(f.card)
			return
		}
		if r.URL.Path != "/rpc" {
			http.NotFound(w, r)
			return
		}
		var request struct {
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		f.methods = append(f.methods, request.Method)
		if request.Method == "tasks/get" || request.Method == "tasks/resubscribe" {
			if string(request.Params["id"]) != `"durable-task"` {
				t.Errorf("lost durable ID: %s", request.Params["id"])
			}
		}
		if request.Method == "message/send" {
			var conf struct {
				Blocking bool `json:"blocking"`
			}
			json.Unmarshal(request.Params["configuration"], &conf)
			if conf.Blocking {
				t.Error("blocking submission")
			}
		}
		if f.rpcError {
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"muxlio","error":{"code":-32001,"message":"Task not found"}}`)
			return
		}
		if request.Method == "tasks/resubscribe" {
			w.Header().Set("Content-Type", "text/event-stream")
			raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "muxlio", "result": map[string]any{"kind": "status-update", "taskId": "durable-task", "contextId": "remote-context", "status": map[string]any{"state": f.state}}})
			fmt.Fprintf(w, ": heartbeat\ndata: %s\n\n", raw)
			if f.disconnect {
				// End the socket without the terminating HTTP chunk, as when a
				// connection drops after an update. A clean SSE EOF is not enough
				// to exercise recovery from a transport error.
				w.(http.Flusher).Flush()
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				conn.Close()
				return
			}
			return
		}
		result := taskResult(f.state)
		if f.metadata != nil {
			result["metadata"] = f.metadata
		}

		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "muxlio", "result": result})
	}))
	f.card = map[string]any{"protocolVersion": "0.3.0", "url": f.server.URL + "/rpc", "preferredTransport": "JSONRPC", "defaultInputModes": []string{"text/plain"}, "capabilities": map[string]any{"streaming": true}}
	t.Cleanup(f.server.Close)
	return f
}

func TestA2AMetadataAndRepeatedPollsNeverInventAccounting(t *testing.T) {
	f := newFixture(t)
	f.state = "completed"
	f.metadata = map[string]any{"usage": map[string]any{"totalTokens": 100}, "accounting": map[string]any{"revision": 1, "final": true}}
	req := f.request()
	req.Outputs = nil
	first := invoke(t, req)
	if first.Error != "" || first.Accounting != nil || first.RemoteAccounting != nil {
		t.Fatalf("A2A invented accounting: %+v", first)
	}
	req.RemoteTask = first.RemoteTask
	req.Action = "poll"
	for i := 0; i < 10; i++ {
		result := invoke(t, req)
		if result.Error != "" || result.Accounting != nil || result.RemoteAccounting != nil {
			t.Fatalf("A2A poll invented accounting: %+v", result)
		}
	}
}
func (f *fixture) request() adapter.Request {
	raw, _ := json.Marshal(params{URL: f.server.URL})
	return adapter.Request{Executor: "agent/a2a", Params: raw, Task: "Do remote work", Outputs: map[string]adapter.OutputDecl{"answer": {Mime: "application/json"}, "report": {Path: "report.txt", Mime: "text/plain"}}}
}

func TestRemoteTaskOutlivesAdapterAndResumesAfterStreamDisconnect(t *testing.T) {
	f := newFixture(t)
	req := f.request()
	first := invoke(t, req)
	if first.Error != "" || first.RemoteTask == nil || !first.RemoteTask.Pending || first.RemoteTask.ContextID != "remote-context" || first.Accounting != nil {
		t.Fatalf("submission: %+v", first)
	}
	// Persist through JSON as the coordinator does; the submitting process has exited.
	raw, _ := json.Marshal(first.RemoteTask)
	req.RemoteTask = &contract.RemoteTaskReference{}
	json.Unmarshal(raw, req.RemoteTask)
	req.Action = "poll"
	req.Params, _ = json.Marshal(params{URL: f.server.URL, Stream: true})
	f.mu.Lock()
	f.disconnect = true
	f.mu.Unlock()
	disconnected := invoke(t, req)
	if disconnected.Error != "" || !disconnected.RemoteTask.Pending || disconnected.Logs == "" {
		t.Fatalf("disconnect: %+v", disconnected)
	}
	f.mu.Lock()
	f.state = "completed"
	f.mu.Unlock()
	done := invoke(t, req)
	if done.Error != "" || done.RemoteTask.Pending || done.Accounting != nil {
		t.Fatalf("completion: %+v", done)
	}
	if done.Outputs["answer"].(map[string]any)["ok"] != true || done.Outputs["report"].(map[string]any)["path"] == "" {
		t.Fatalf("artifacts: %+v", done.Outputs)
	}
	report, err := os.ReadFile(done.Outputs["report"].(map[string]any)["path"].(string))
	if err != nil || string(report) != "report" {
		t.Fatalf("imported bytes: %q %v", report, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if got := strings.Join(f.methods, ","); got != "message/send,tasks/resubscribe,tasks/get,tasks/resubscribe,tasks/get" {
		t.Fatalf("unexpected requests/repeated submission: %s", got)
	}
}
func TestReconcileNeverResubmitsOrClaimsNotApplied(t *testing.T) {
	f := newFixture(t)
	req := f.request()
	req.Action = "reconcile"
	unknown := invoke(t, req)
	if unknown.Error != "" || unknown.Reconciliation.Status != "inconclusive" {
		t.Fatalf("unknown: %+v", unknown)
	}
	req.RemoteTask = &contract.RemoteTaskReference{ID: "durable-task", ContextID: "remote-context", URL: f.server.URL + "/rpc"}
	pending := invoke(t, req)
	if pending.Reconciliation.Status != "pending" || !pending.RemoteTask.Pending {
		t.Fatalf("pending: %+v", pending)
	}
	f.mu.Lock()
	f.rpcError = true
	f.mu.Unlock()
	missing := invoke(t, req)
	if missing.Reconciliation.Status != "inconclusive" {
		t.Fatalf("missing: %+v", missing)
	}
	f.mu.Lock()
	f.rpcError = false
	f.state = "completed"
	f.mu.Unlock()
	done := invoke(t, req)
	if done.Reconciliation.Status != "completed" || done.Error != "" {
		t.Fatalf("done: %+v", done)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.methods {
		if m != "tasks/get" {
			t.Fatalf("mutating reconciliation: %s", m)
		}
	}
}
func TestUnsupportedDiscoveryCapabilitiesAndAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fixture, *adapter.Request)
		want   string
	}{
		{"version", func(f *fixture, _ *adapter.Request) { f.card["protocolVersion"] = "1.0.0" }, "unsupported A2A protocol"},
		{"transport", func(f *fixture, _ *adapter.Request) { f.card["preferredTransport"] = "GRPC" }, "JSONRPC endpoint"},
		{"stream", func(f *fixture, r *adapter.Request) {
			f.card["capabilities"] = map[string]any{}
			r.Params, _ = json.Marshal(params{URL: f.server.URL, Stream: true})
		}, "does not support streaming"},
		{"extension", func(f *fixture, _ *adapter.Request) {
			f.card["capabilities"] = map[string]any{"extensions": []any{map[string]any{"uri": "unknown", "required": true}}}
		}, "unsupported required A2A extension"},
		{"oauth", func(f *fixture, _ *adapter.Request) {
			f.card["security"] = []any{map[string]any{"oauth": []string{}}}
			f.card["securitySchemes"] = map[string]any{"oauth": map[string]any{"type": "oauth2"}}
		}, "unsupported A2A authentication"},
		{"missing-token", func(f *fixture, _ *adapter.Request) {
			f.card["security"] = []any{map[string]any{"token": []string{}}}
			f.card["securitySchemes"] = map[string]any{"token": map[string]any{"type": "http", "scheme": "bearer"}}
		}, "missing A2A_TOKEN"},
		{"input", func(f *fixture, _ *adapter.Request) { f.card["defaultInputModes"] = []string{"image/png"} }, "text/plain"},
		{"endpoint-origin", func(f *fixture, _ *adapter.Request) { f.card["url"] = "https://elsewhere.example/rpc" }, "share the configured URL origin"},
		{"idempotency", func(_ *fixture, r *adapter.Request) { r.IdempotencyKey = "key" }, "idempotencyKey"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			req := f.request()
			tc.change(f, &req)
			result := invoke(t, req)
			if !strings.Contains(result.Error, tc.want) {
				t.Fatalf("wanted %q, got %+v", tc.want, result)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.methods) > 0 {
				t.Fatalf("submitted despite rejection: %v", f.methods)
			}
		})
	}
}
func TestBearerDiscoveryAndRPC(t *testing.T) {
	var url string
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(401)
			return
		}
		calls.Add(1)
		if r.Method == "GET" {
			fmt.Fprintf(w, `{"protocolVersion":"0.3.0","url":%q,"defaultInputModes":["text/plain"],"security":[{"token":[]}],"securitySchemes":{"token":{"type":"http","scheme":"bearer"}}}`, url+"/rpc")
			return
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"muxlio","result":{"kind":"task","id":"task","contextId":"ctx","status":{"state":"working"}}}`)
	}))
	defer s.Close()
	url = s.URL
	raw, _ := json.Marshal(params{URL: url})
	req := adapter.Request{Executor: "agent/a2a", Params: raw, Secrets: map[string]string{"A2A_TOKEN": "secret"}}
	result := invoke(t, req)
	if result.Error != "" || !result.RemoteTask.Pending || calls.Load() != 2 {
		t.Fatalf("authenticated: %+v calls %d", result, calls.Load())
	}
	req.Secrets = nil
	result = invoke(t, req)
	if !strings.Contains(result.Error, "authentication rejected") {
		t.Fatalf("auth error: %+v", result)
	}
}
func TestRemoteStatesAndInvalidArtifacts(t *testing.T) {
	for _, state := range []string{"submitted", "working", "completed", "failed", "canceled", "rejected", "input-required", "auth-required", "unexpected"} {
		t.Run(state, func(t *testing.T) {
			raw, _ := json.Marshal(taskResult(state))
			var task task
			json.Unmarshal(raw, &task)
			result := finish(&adapter.Request{}, task, "https://example.test/rpc")
			if state == "submitted" || state == "working" {
				if !result.RemoteTask.Pending || result.Error != "" {
					t.Fatalf("pending: %+v", result)
				}
			} else if state == "completed" {
				if result.Error != "" {
					t.Fatal(result.Error)
				}
			} else if result.Error == "" {
				t.Fatalf("state silently accepted: %+v", result)
			}
			if (state == "unexpected" || state == "input-required" || state == "auth-required") && result.RemoteTask != nil {
				t.Fatalf("nonterminal state claimed terminal outcome: %+v", result)
			}
		})
	}
	for _, raw := range []string{
		`[{"kind":"file","file":{"uri":"http://internal/secret"}}]`,
		`[{"kind":"file","file":{"bytes":"invalid-base64"}}]`,
		`[{"kind":"image"}]`,
	} {
		var parts []part
		json.Unmarshal([]byte(raw), &parts)
		if _, err := artifactData(parts); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestPollingFailureRetainsIdentity(t *testing.T) {
	f := newFixture(t)
	req := f.request()
	req.WorkDir = t.TempDir()
	req.Action = "poll"
	req.RemoteTask = &contract.RemoteTaskReference{ID: "durable-task", ContextID: "remote-context"}
	f.rpcError = true
	result := execute(context.Background(), &req)
	if result.Error != "" || !result.RemoteTask.Pending || !strings.Contains(result.Logs, "Task not found") {
		t.Fatalf("poll lost identity: %+v", result)
	}
}

func TestDiscoveryAlternateInterfaceAndRecoveryIdentity(t *testing.T) {
	f := newFixture(t)
	f.card["preferredTransport"] = "GRPC"
	f.card["additionalInterfaces"] = []any{map[string]any{"transport": "JSONRPC", "url": f.server.URL + "/rpc"}}
	req := f.request()
	result := invoke(t, req)
	if result.Error != "" || !result.RemoteTask.Pending {
		t.Fatalf("alternate JSONRPC: %+v", result)
	}
	req.Action = "poll"
	req.RemoteTask = result.RemoteTask
	req.RemoteTask.ContextID = "different-context"
	mismatch := invoke(t, req)
	if !strings.Contains(mismatch.Error, "identity mismatch") {
		t.Fatalf("context mismatch accepted: %+v", mismatch)
	}
	req.RemoteTask.ContextID = "remote-context"
	req.RemoteTask.URL = f.server.URL + "/other"
	mismatch = invoke(t, req)
	if !strings.Contains(mismatch.Error, "durable task endpoint differs") {
		t.Fatalf("endpoint changed: %+v", mismatch)
	}
}

func TestMalformedScalarDoesNotDiscardCompletedArtifacts(t *testing.T) {
	raw, _ := json.Marshal(taskResult("completed"))
	var remote task
	json.Unmarshal(raw, &remote)
	remote.Artifacts[0].Parts[0] = part{Kind: "text", Text: "not JSON"}
	req := adapter.Request{WorkDir: t.TempDir(), Outputs: map[string]adapter.OutputDecl{"answer": {}, "report": {Path: "report.bin", Mime: "application/octet-stream"}}}
	result := finish(&req, remote, "https://example.test/rpc")
	if result.Error != "" || result.RemoteTask.Pending || result.OutputErrors["answer"] == "" || result.Outputs["report"] == nil {
		t.Fatalf("scalar error discarded completed artifact: %+v", result)
	}
}

func TestInterruptedRemoteTaskReconciliationIsInconclusive(t *testing.T) {
	for _, state := range []string{"input-required", "auth-required", "unexpected"} {
		t.Run(state, func(t *testing.T) {
			f := newFixture(t)
			f.state = state
			req := f.request()
			req.Action = "reconcile"
			req.RemoteTask = &contract.RemoteTaskReference{ID: "durable-task", ContextID: "remote-context", URL: f.server.URL + "/rpc"}
			result := invoke(t, req)
			if result.Error == "" || result.RemoteTask != nil || result.Reconciliation == nil || result.Reconciliation.Status != "inconclusive" {
				t.Fatalf("interrupted operation claimed completed: %+v", result)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if strings.Join(f.methods, ",") != "tasks/get" {
				t.Fatalf("reconciliation mutated interrupted task: %v", f.methods)
			}
		})
	}
}

func TestDurableReferencePreventsSubmissionWithoutActionFlag(t *testing.T) {
	f := newFixture(t)
	req := f.request()
	req.RemoteTask = &contract.RemoteTaskReference{ID: "durable-task", ContextID: "remote-context", URL: f.server.URL + "/rpc"}
	result := invoke(t, req)
	if result.Error != "" || result.RemoteTask == nil || !result.RemoteTask.Pending {
		t.Fatalf("recovered reference: %+v", result)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Join(f.methods, ",") != "tasks/get" {
		t.Fatalf("recovery repeated submission: %v", f.methods)
	}
}

func TestRedirectCannotForwardBearerCredentials(t *testing.T) {
	var forwarded atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
		t.Error("followed redirect to another origin")
	}))
	defer target.Close()
	for _, stage := range []string{"discovery", "submission"} {
		t.Run(stage, func(t *testing.T) {
			var endpoint string
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("missing configured bearer credential")
				}
				if stage == "submission" && r.Method == http.MethodGet {
					json.NewEncoder(w).Encode(map[string]any{"protocolVersion": "0.3.0", "url": endpoint, "defaultInputModes": []string{"text/plain"}})
					return
				}
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			}))
			defer source.Close()
			endpoint = source.URL + "/rpc"
			raw, _ := json.Marshal(params{URL: source.URL})
			result := invoke(t, adapter.Request{Executor: "agent/a2a", Params: raw, Secrets: map[string]string{"A2A_TOKEN": "secret"}})
			if result.Error == "" || result.RemoteTask != nil || forwarded.Load() != 0 {
				t.Fatalf("redirect accepted: %+v forwarded=%d", result, forwarded.Load())
			}
		})
	}
}

func TestPathPrefixedURLDiscoversAgentCard(t *testing.T) {
	const prefix = "/v1/agent"
	var rpc string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == prefix+"/.well-known/agent-card.json" {
			json.NewEncoder(w).Encode(map[string]any{
				"protocolVersion": "0.3.0", "url": rpc, "defaultInputModes": []string{"text/plain"},
			})
			return
		}
		if r.URL.Path == prefix+"/rpc" && r.Method == http.MethodPost {
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "muxlio", "result": map[string]any{
				"kind": "task", "id": "t1", "contextId": "c1", "status": map[string]any{"state": "working"},
			}})
			return
		}
		http.NotFound(w, r)
	}))
	defer s.Close()
	rpc = s.URL + prefix + "/rpc"
	raw, _ := json.Marshal(params{URL: s.URL + prefix})
	req := adapter.Request{Executor: "agent/a2a", Params: raw, Task: "work"}
	result := invoke(t, req)
	if result.Error != "" || result.RemoteTask == nil || !result.RemoteTask.Pending {
		t.Fatalf("path-prefixed discovery: %+v", result)
	}
}

func TestOversizedRemoteResponsesRejected(t *testing.T) {
	for _, stage := range []string{"discovery", "submission"} {
		t.Run(stage, func(t *testing.T) {
			var endpoint string
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stage == "submission" && r.Method == http.MethodGet {
					json.NewEncoder(w).Encode(map[string]any{"protocolVersion": "0.3.0", "url": endpoint, "defaultInputModes": []string{"text/plain"}})
					return
				}
				fmt.Fprint(w, strings.Repeat(" ", adapter.MaxCaptureBytes+1))
			}))
			defer source.Close()
			endpoint = source.URL + "/rpc"
			raw, _ := json.Marshal(params{URL: source.URL})
			result := invoke(t, adapter.Request{Executor: "agent/a2a", Params: raw})
			if !strings.Contains(result.Error, "exceeds 4 MiB") || result.RemoteTask != nil {
				t.Fatalf("oversized response accepted: %+v", result)
			}
		})
	}
}
