package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInputRequestTools(t *testing.T) {
	requests := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer scoped" {
			t.Error("missing caller credential")
		}
		if r.Method == "POST" {
			body, _ := io.ReadAll(r.Body)
			if r.URL.Path != "/v1/runs/r/input-requests/req" || r.Header.Get("Idempotency-Key") != "delivery" || string(body) != "false" {
				t.Errorf("response delivery: %s %s %s", r.URL.Path, r.Header.Get("Idempotency-Key"), body)
			}
		} else if r.URL.Path != "/v1/runs/r/input-requests" {
			t.Error(r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"requests":[],"accepted":true}`))
	}))
	defer api.Close()
	s := &server{api: newAPIClient(api.URL, "scoped")}
	for _, tc := range []struct {
		name, args string
		fail       bool
	}{
		{"get_input_requests", `{"runId":"r"}`, false},
		{"respond_input_request", `{"runId":"r","requestId":"req","idempotencyKey":"delivery","response":false}`, false},
		{"respond_input_request", `{"runId":"r","requestId":"req","response":false}`, true},
		{"respond_input_request", `{"runId":"r","requestId":"req","idempotencyKey":"delivery"}`, true},
	} {
		result := s.dispatchTool(tc.name, json.RawMessage(tc.args))
		if result.IsError != tc.fail {
			t.Fatalf("%s: %+v", tc.name, result)
		}
	}
	if requests != 2 {
		t.Fatalf("requests=%d", requests)
	}
}
