package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestAnalyticsEndpointBoundedAndFiltered(t *testing.T) {
	s := state.New()
	defer s.Close()
	at := time.Date(2026, 6, 2, 15, 0, 0, 0, time.UTC)
	if err := s.CreateRun(&state.Run{ID: "run", WorkflowName: "demo", State: "succeeded", Error: "private failure detail", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{
		ID: "a1", RunID: "run", ActivationID: "act", Executor: "cursor",
		State: "succeeded", StartedAt: at, FinishedAt: at,
		Usage:  &state.TokenUsage{Input: 3, Output: 1},
		Result: &state.AttemptResult{Outputs: map[string]any{"x": 1}},
	}); err != nil {
		t.Fatal(err)
	}
	from := "2026-06-01T00:00:00Z"
	to := "2026-06-03T00:00:00Z"
	w := httptest.NewRecorder()
	NewRouter(s, nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/analytics?from="+from+"&to="+to, nil))
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Runs          []*state.Run
		AttemptsByRun map[string][]state.ExecutionAttempt
		TotalRuns     int
		Populations   map[string]string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.TotalRuns != 1 || len(body.Runs) != 1 {
		t.Fatalf("runs=%+v total=%d", body.Runs, body.TotalRuns)
	}
	attempts := body.AttemptsByRun["run"]
	if len(attempts) != 1 || attempts[0].Result != nil {
		t.Fatalf("attempts=%+v", attempts)
	}
	if body.Populations["runOutcomes"] == "" {
		t.Fatal("populations missing")
	}
	if body.Runs[0].Error != "" {
		t.Fatalf("analytics summary exposed run error detail: %q", body.Runs[0].Error)
	}
}

func TestAnalyticsQueryPaginationBounds(t *testing.T) {
	s := state.New()
	defer s.Close()
	router := NewRouter(s, nil)
	for _, spec := range []struct {
		path      string
		wantLimit int
	}{
		{"/v1/analytics", 200},
		{"/v1/analytics?limit=50", 50},
		{"/v1/analytics?limit=300", 300},
		{"/v1/analytics?limit=900", 500},
		{"/v1/analytics?limit=0", 200},
		{"/v1/analytics?limit=-3", 200},
		{"/v1/analytics?limit=not-a-number", 200},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", spec.path, nil))
		if w.Code != 200 {
			t.Fatalf("%s status=%d body=%s", spec.path, w.Code, w.Body.String())
		}
		var body struct {
			Limit int `json:"limit"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v", spec.path, err)
		}
		if body.Limit != spec.wantLimit {
			t.Fatalf("%s limit=%d want %d", spec.path, body.Limit, spec.wantLimit)
		}
	}
}

func TestAnalyticsQueryInvalidFromRejected(t *testing.T) {
	s := state.New()
	defer s.Close()
	w := httptest.NewRecorder()
	NewRouter(s, nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/analytics?from=not-a-date", nil))
	if w.Code != 400 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAnalyticsQueryBoundaryInclusive(t *testing.T) {
	s := state.New()
	defer s.Close()
	boundary := time.Date(2026, 6, 2, 12, 0, 0, 0, time.FixedZone("CET", 2*3600))
	utc := boundary.UTC()
	if err := s.CreateRun(&state.Run{ID: "edge", WorkflowName: "demo", State: "succeeded", CreatedAt: utc, UpdatedAt: utc}); err != nil {
		t.Fatal(err)
	}
	from := "2026-06-02T10:00:00Z"
	to := "2026-06-02T12:00:00Z"
	w := httptest.NewRecorder()
	NewRouter(s, nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/analytics?from="+from+"&to="+to, nil))
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		TotalRuns int `json:"totalRuns"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.TotalRuns != 1 {
		t.Fatalf("boundary inclusive count=%d", body.TotalRuns)
	}
}

func TestAnalyticsPreservesZeroReportedUsage(t *testing.T) {
	s := state.New()
	defer s.Close()
	at := time.Date(2026, 6, 2, 15, 0, 0, 0, time.UTC)
	if err := s.CreateRun(&state.Run{ID: "run", WorkflowName: "demo", State: "succeeded", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{
		ID: "a1", RunID: "run", ActivationID: "act", Executor: "cursor",
		State: "succeeded", StartedAt: at, FinishedAt: at, Usage: contract.NewTokenUsage(&zero, nil, nil),
	}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	NewRouter(s, nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/analytics?from=2026-06-01T00:00:00Z&to=2026-06-03T00:00:00Z", nil))
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	attempts, ok := raw["attemptsByRun"].(map[string]any)
	if !ok {
		t.Fatal("attemptsByRun missing")
	}
	list, ok := attempts["run"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("attempts=%v", attempts["run"])
	}
	attempt, ok := list[0].(map[string]any)
	if !ok {
		t.Fatal("bad attempt shape")
	}
	usageObj, ok := attempt["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage missing: %v", attempt["usage"])
	}
	if usageObj["input"] == nil {
		t.Fatalf("reported zero input stripped: %+v", usageObj)
	}
}

func TestAnalyticsCommaSeparatedStates(t *testing.T) {
	s := state.New()
	defer s.Close()
	at := time.Date(2026, 6, 2, 15, 0, 0, 0, time.UTC)
	for _, spec := range []struct{ id, st string }{{"ok", "succeeded"}, {"bad", "failed"}} {
		if err := s.CreateRun(&state.Run{ID: spec.id, WorkflowName: "demo", CreatedAt: at, UpdatedAt: at}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetRunState(spec.id, spec.st); err != nil {
			t.Fatal(err)
		}
	}
	w := httptest.NewRecorder()
	NewRouter(s, nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/analytics?state=succeeded,failed&from=2026-06-01T00:00:00Z&to=2026-06-03T00:00:00Z", nil))
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	var body struct {
		TotalRuns int `json:"totalRuns"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.TotalRuns != 2 {
		t.Fatalf("state CSV filter total=%d", body.TotalRuns)
	}
}
