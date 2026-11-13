package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/state"
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
		if string(body["planDigest"]) != `"sha256:registered"` || string(body["originLabel"]) != `"Editor"` || string(body["workspaceId"]) != `"project"` || string(body["input"]) != `{"n":9007199254740993}` {
			t.Errorf("body: %s", body)
		}
		_, _ = w.Write([]byte(`{"runId":"original"}`))
	}))
	defer coordinator.Close()
	s := &server{api: newAPIClient(coordinator.URL, "token")}
	args := json.RawMessage(`{"originLabel":"Editor","planDigest":"sha256:registered","workspaceId":"project","input":{"n":9007199254740993},"idempotencyKey":"retry"}`)
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

func TestListRunsForwardsDiscoveryFilters(t *testing.T) {
	coordinator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := map[string]string{"workspaceId": "project / one", "planDigest": "sha256:one", "state": "running", "callerId": "me", "createdAfter": "2026-01-02T00:00:00Z", "createdBefore": "2026-02-02T00:00:00Z", "limit": "10", "offset": "20"}
		for key, value := range want {
			if r.URL.Query().Get(key) != value {
				t.Errorf("%s=%q", key, r.URL.Query().Get(key))
			}
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing auth")
		}
		_, _ = w.Write([]byte(`{"runs":[],"total":0}`))
	}))
	defer coordinator.Close()
	s := &server{api: newAPIClient(coordinator.URL, "token")}
	result := s.dispatchTool("list_runs", json.RawMessage(`{"workspaceId":"project / one","planDigest":"sha256:one","state":"running","callerId":"me","createdAfter":"2026-01-02T00:00:00Z","createdBefore":"2026-02-02T00:00:00Z","limit":10,"offset":20}`))
	if result.IsError {
		t.Fatal(result)
	}
	if result := s.dispatchTool("list_runs", json.RawMessage(`{"limit":"ten"}`)); !result.IsError {
		t.Fatal("invalid pagination accepted")
	}
}

func TestListRunsWithoutArgumentsPreservesGenericDiscovery(t *testing.T) {
	st := state.New()
	defer st.Close()
	if err := st.CreateRun(&state.Run{ID: "legacy"}); err != nil {
		t.Fatal(err)
	}
	coordinator := httptest.NewServer(api.WithAuth(api.NewRouter(st, nil), "token", "worker"))
	defer coordinator.Close()
	s := &server{api: newAPIClient(coordinator.URL, "token")}
	for _, args := range []json.RawMessage{nil, json.RawMessage(`{}`)} {
		got := s.dispatchTool("list_runs", args)
		if got.IsError {
			t.Fatalf("arguments %s: %+v", args, got)
		}
		var result struct {
			Runs []state.Run `json:"runs"`
		}
		if err := json.Unmarshal([]byte(got.Content[0].Text), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Runs) != 1 || result.Runs[0].ID != "legacy" {
			t.Fatalf("generic discovery lost legacy run: %+v", result)
		}
	}
}
