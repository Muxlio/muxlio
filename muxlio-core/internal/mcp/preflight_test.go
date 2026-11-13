package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPreflightForwardsOnlyValidationRequest(t *testing.T) {
	for _, feasible := range []bool{true, false} {
		calls := 0
		coordinator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.Method != http.MethodPost || r.URL.Path != "/v1/runs/preflight" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
			var body struct {
				WorkspaceID string          `json:"workspaceId"`
				Input       json.RawMessage `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.WorkspaceID != "project" || string(body.Input) != `{"n":9007199254740993}` {
				t.Errorf("changed validation arguments: %+v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"feasible": feasible, "unverified": []string{"current_tool_authorization"}})
		}))
		s := &server{api: newAPIClient(coordinator.URL, "token")}
		result := s.dispatchTool("preflight_run", json.RawMessage(`{"workspaceId":"project","plan":{},"input":{"n":9007199254740993}}`))
		coordinator.Close()
		if result.IsError == feasible || calls != 1 {
			t.Fatalf("validation result=%+v calls=%d", result, calls)
		}
	}
}
