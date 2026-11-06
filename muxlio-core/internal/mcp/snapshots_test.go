package mcp

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestGetSnapshotsForwardsHistoryFilters(t *testing.T) {
	ts := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runs/run/snapshots" || r.URL.Query().Get("attemptId") != "attempt" || r.URL.Query().Get("after") != "7" || r.URL.Query().Get("current") != "true" {
			t.Errorf("request: %s", r.URL)
		}
		w.Write([]byte(`{"snapshots":[],"nextAfter":7,"hasMore":false}`))
	}))
	srv := &server{api: newAPIClient(ts.URL, "")}
	result := srv.dispatchTool("get_snapshots", json.RawMessage(`{"runId":"run","attemptId":"attempt","current":true,"after":7}`))
	if result.IsError {
		t.Fatalf("get_snapshots: %+v", result)
	}
}
