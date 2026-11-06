package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestInputRequestAPI(t *testing.T) {
	s := state.New()
	_ = s.CreateRun(&state.Run{ID: "r"})
	_ = s.CreateInputRequest(&state.InputRequest{RunID: "r", RequestID: "a", Name: "review", Permissions: []string{"review"}, Schema: json.RawMessage(`{"type":"object","required":["approved"],"properties":{"approved":{"type":"boolean"}},"additionalProperties":false}`)})
	h := WithAuth(NewRouter(s, nil), "admin", "worker",
		AccessGrant{Token: "allowed", RunID: "r", Permissions: []string{"inputs", "review"}},
		AccessGrant{Token: "denied", RunID: "r", Permissions: []string{"inputs"}},
		AccessGrant{Token: "other-run", RunID: "other", Permissions: []string{"inputs", "review"}})
	call := func(method, path, token, key, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		token, key, body string
		status           int
	}{
		{"", "key", `{}`, 401}, {"worker", "key", `{}`, 403}, {"other-run", "key", `{}`, 403}, {"denied", "key", `{}`, 403},
		{"allowed", "", `{"approved":true}`, 400}, {"allowed", "key", `{"approved":`, 400}, {"allowed", "key", `{} {}`, 400},
		{"allowed", "key", `{"approved":"yes"}`, 422}, {"allowed", "key", `{"approved":true}`, 200},
		{"allowed", "key", `{"approved":false}`, 200}, {"allowed", "other", `{"approved":true}`, 409},
	} {
		w := call("POST", "/v1/runs/r/input-requests/a", tc.token, tc.key, tc.body)
		if w.Code != tc.status {
			t.Errorf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
	got, _ := s.GetInputRequest("r", "a")
	if got.Response.(map[string]any)["approved"] != true || got.ResponderID == "" {
		t.Fatalf("accepted response: %+v", got)
	}
	w := call("GET", "/v1/runs/r/input-requests", "allowed", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"requestId":"a"`) {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	past := time.Now().Add(-time.Second)
	_ = s.CreateInputRequest(&state.InputRequest{RunID: "r", RequestID: "expired", ExpiresAt: &past})
	w = call("POST", "/v1/runs/r/input-requests/expired", "allowed", "key", `null`)
	if w.Code != 409 {
		t.Fatalf("expired: %d %s", w.Code, w.Body.String())
	}
	for _, body := range []string{`{bad`, `{} {}`, `[]`, `true`} {
		w = call(http.MethodPost, "/v1/runs/r/signals/answer", "admin", "", body)
		if w.Code != 400 {
			t.Errorf("malformed signal %q: %d", body, w.Code)
		}
	}
}
