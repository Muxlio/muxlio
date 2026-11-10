package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestPaginatedEventsRejectsMalformedEventCursors(t *testing.T) {
	st := state.New()
	if err := st.CreateRun(&state.Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendEvent("r", "test", "", nil); err != nil {
		t.Fatal(err)
	}
	h := NewRouter(st, nil)
	for _, path := range []string{
		"/v1/runs/r/events?after=foo",
		"/v1/runs/r/events?after=-1",
		"/v1/runs/r/events?after=1.5",
		"/v1/runs/r/events?order=desc&before=bad",
		"/v1/runs/r/snapshots?after=-2",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d body=%s", path, w.Code, w.Body.String())
		}
		var env APIError
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if env.Code != CodeBadEventCursor {
			t.Fatalf("%s: code=%q", path, env.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/runs/r/events?after=0", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("valid cursor: %d %s", w.Code, w.Body.String())
	}
}

func TestStreamRejectsMalformedEventCursor(t *testing.T) {
	st := state.New()
	_ = st.CreateRun(&state.Run{ID: "r"})
	h := NewRouter(st, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/runs/r/stream?after=xy", nil)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), CodeBadEventCursor) {
		t.Fatalf("stream cursor: %d %s", w.Code, w.Body.String())
	}
}
