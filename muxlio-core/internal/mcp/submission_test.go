package mcp

import (
	"bytes"
	"encoding/json"
	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/state"
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

func TestStartRunForwardsRegisteredReference(t *testing.T) {
	calls := 0
	coordinator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/runs" || r.Header.Get("Idempotency-Key") != "retry" || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("request: %s %v", r.URL.Path, r.Header)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if _, ok := body["plan"]; ok {
			t.Error("reference submission sent a plan")
		}
		if string(body["planDigest"]) != `"sha256:registered"` || string(body["workspaceId"]) != `"project"` || string(body["input"]) != `{"n":9007199254740993}` {
			t.Errorf("body: %s", body)
		}
		_, _ = w.Write([]byte(`{"runId":"original"}`))
	}))
	defer coordinator.Close()
	s := &server{api: newAPIClient(coordinator.URL, "token")}
	args := json.RawMessage(`{"planDigest":"sha256:registered","workspaceId":"project","input":{"n":9007199254740993},"idempotencyKey":"retry"}`)
	for i := 0; i < 2; i++ {
		if got := s.dispatchTool("start_run", args); got.IsError {
			t.Fatalf("%+v", got)
		}
	}
	for _, args := range []string{`{"input":{}}`, `{"plan":{},"planDigest":"sha256:registered","input":{}}`} {
		if got := s.dispatchTool("start_run", json.RawMessage(args)); !got.IsError {
			t.Fatalf("accepted %s", args)
		}
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestMCPRegistrationStoresCoordinatorPlan(t *testing.T) {
	st := state.New()
	coordinator := httptest.NewServer(api.WithAuth(api.NewRouter(st, nil), "token", "worker"))
	defer coordinator.Close()
	s := &server{api: newAPIClient(coordinator.URL, "token")}
	raw := json.RawMessage(`{"irVersion":"1.0","name":"registered","root":"root","subgraphs":{"root":{"nodes":[]}}}`)
	args, _ := json.Marshal(map[string]json.RawMessage{"plan": raw})
	got := s.dispatchTool("register_workflow_version", args)
	if got.IsError {
		t.Fatalf("%+v", got)
	}
	var version map[string]string
	if err := json.Unmarshal([]byte(got.Content[0].Text), &version); err != nil {
		t.Fatal(err)
	}
	stored, err := st.GetWorkflowVersion(version["digest"])
	if err != nil || !bytes.Contains(stored, []byte(`"name":"registered"`)) {
		t.Fatalf("stored: %s %v", stored, err)
	}
}
