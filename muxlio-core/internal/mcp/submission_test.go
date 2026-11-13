package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStartRunForwardsStableIdempotencyKey(t *testing.T) {
	calls := 0
	coordinator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Idempotency-Key") != "invocation-123" || r.URL.Path != "/v1/runs" {
			t.Errorf("submission: %s key=%q", r.URL.Path, r.Header.Get("Idempotency-Key"))
		}
		var body struct {
			Input json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if string(body.Input) != `{"n":9007199254740993}` {
			t.Errorf("input changed: %s", body.Input)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runId":"original","state":"running"}`))
	}))
	defer coordinator.Close()
	s := &server{api: newAPIClient(coordinator.URL, "token")}
	args := json.RawMessage(`{"plan":{"irVersion":"1.0","name":"submit","root":"root","subgraphs":{"root":{"nodes":[]}}},"input":{"n":9007199254740993},"idempotencyKey":"invocation-123"}`)
	for i := 0; i < 2; i++ {
		if result := s.toolStartRun(args); result.IsError {
			t.Fatalf("start_run: %+v", result)
		}
	}
	if calls != 2 {
		t.Fatalf("requests=%d", calls)
	}
}
