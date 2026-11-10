package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

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
