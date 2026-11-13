package integration_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/mcp"
)

// Exercise the transport and authorization boundary with generic coordinator
// resources. MCP annotations and discovery never substitute for API grants.
func TestMCPRemoteGrantRotationAndAuthorization(t *testing.T) {
	var mu sync.RWMutex
	grants := []api.AccessGrant{{Token: "reader", RunID: "own", Permissions: []string{"read"}}}
	coordinator := httptest.NewServer(api.WithReloadingAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if api.CallerFromRequest(r).IsAdmin {
			t.Error("MCP escalated a client to administrator")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"own"}`))
	}), "admin", func() (string, []api.AccessGrant, error) {
		mu.RLock()
		defer mu.RUnlock()
		return "worker", append([]api.AccessGrant(nil), grants...), nil
	}))
	defer coordinator.Close()
	t.Setenv("MUXLIO_MCP_HTTP_TOKEN", "gateway")
	handler, err := mcp.NewHTTPHandler(coordinator.URL, "admin", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(token, tool, run string, wantStatus int, wantError bool) {
		t.Helper()
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tool+`","arguments":{"runId":"`+run+`"}}}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-11-25")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != wantStatus {
			t.Fatalf("%s %s: %d %s", token, tool, w.Code, w.Body)
		}
		if w.Code == 200 {
			var reply struct {
				Result struct {
					IsError bool `json:"isError"`
				}
			}
			if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || reply.Result.IsError != wantError {
				t.Fatalf("%s %s: %s (%v)", token, tool, w.Body, err)
			}
		}
	}
	call("reader", "get_run", "own", 200, false)
	call("reader", "get_run", "other", 200, true)
	call("reader", "cancel_run", "own", 200, true)
	call("invalid", "get_run", "own", 401, false)
	mu.Lock()
	grants[0].Token = "rotated"
	mu.Unlock()
	call("reader", "get_run", "own", 401, false)
	call("rotated", "get_run", "own", 200, false)
	mu.Lock()
	grants[0].Revoked = true
	mu.Unlock()
	call("rotated", "get_run", "own", 401, false)
}
