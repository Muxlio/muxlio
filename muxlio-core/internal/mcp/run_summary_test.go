package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestRunSummaryIsClientNeutral(t *testing.T) {
	const summary = `{"runId":"run","outcome":"uncertain","terminal":false,"nextAction":{"classification":"reconcile"},"links":{"run":"/v1/runs/run"}}`
	ts := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.RequestURI() != "/v1/runs/run/summary" {
			t.Errorf("request: %s %s", r.Method, r.URL)
		}
		w.Write([]byte(summary))
	}))
	srv := &server{api: newAPIClient(ts.URL, "")}
	for _, client := range []string{"Claude", "Cursor", "Codex", "Kiro"} {
		t.Run(client, func(t *testing.T) {
			var response bytes.Buffer
			transport := &mcpServer{tools: srv, enc: json.NewEncoder(&response)}
			init, _ := json.Marshal(map[string]any{"protocolVersion": mcpProtocolVersion, "clientInfo": map[string]string{"name": client, "version": "test"}, "capabilities": map[string]any{}})
			transport.handle(jsonRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize", Params: init})
			response.Reset()
			transport.handle(jsonRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/call", Params: json.RawMessage(`{"name":"get_run_summary","arguments":{"runId":"run"}}`)})
			var envelope struct {
				Result toolCallResult
				Error  *jsonRPCError
			}
			if err := json.Unmarshal(response.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error != nil {
				t.Fatalf("RPC error: %+v", envelope.Error)
			}
			result := envelope.Result
			if result.IsError || len(result.Content) != 1 {
				t.Fatalf("result: %+v", result)
			}
			var got, want any
			json.Unmarshal([]byte(result.Content[0].Text), &got)
			json.Unmarshal([]byte(summary), &want)
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(want)
			if string(a) != string(b) {
				t.Fatalf("summary altered: %s", a)
			}
		})
	}
	for _, args := range []string{`{}`, `{"runId":1}`, `null`} {
		if !srv.dispatchTool("get_run_summary", json.RawMessage(args)).IsError {
			t.Fatalf("accepted %s", args)
		}
	}
	found := false
	for _, tool := range tools {
		if tool.Name == "get_run_summary" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnly || !tool.Annotations.Idempotent {
				t.Fatal("summary must be read-only")
			}
		}
	}
	if !found {
		t.Fatal("summary not advertised")
	}
}
