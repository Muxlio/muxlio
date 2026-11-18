package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunCollectionTools(t *testing.T) {
	const view = `{"collection":{"name":"c","open":true,"items":[{"id":"a","value":{"n":9007199254740993}}]}}`
	for _, tc := range []struct {
		name, args, method, suffix, body string
	}{
		{"get_run_collection", `{"runId":"r","collection":"c"}`, "GET", "", ""},
		{"append_run_collection_item", `{"runId":"r","collection":"c","item":{"id":"a","n":9007199254740993}}`, "POST", "/items", `{"id":"a","n":9007199254740993}`},
		{"close_run_collection", `{"runId":"r","collection":"c"}`, "POST", "/close", `{}`},
		{"append_run_collection_item", `{"runId":"r","collection":"c"}`, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if r.Header.Get("Authorization") != "Bearer scoped" || r.Method != tc.method ||
					r.URL.Path != "/v1/runs/r/collections/c"+tc.suffix || string(body) != tc.body {
					t.Errorf("unexpected request: %s %s %s", r.Method, r.URL.Path, body)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, view+"\n")
			}))
			defer api.Close()
			s := &server{api: newAPIClient(api.URL, "scoped")}
			result := s.dispatchTool(tc.name, json.RawMessage(tc.args))
			if result.IsError != (tc.method == "") {
				t.Fatalf("unexpected result: %+v", result)
			}
			if !result.IsError && result.Content[0].Text != view {
				t.Fatalf("collection JSON changed: %+v", result)
			}
		})
	}
}

func TestRunCollectionToolsRejectIncompleteResponse(t *testing.T) {
	resp := &apiResponse{statusCode: http.StatusOK, body: []byte(`{"collection":`)}
	if result := collectionResult(resp, nil); !result.IsError {
		t.Fatalf("incomplete coordinator JSON returned as success: %+v", result)
	}
}
