package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestRunSummaryPreservesPartialTokenSubtotals(t *testing.T) {
	s := state.New()
	defer s.Close()
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{
		ID: "a1", RunID: "run", ActivationID: "act", State: "succeeded",
		StartedAt: now, FinishedAt: now,
		Usage: &state.TokenUsage{Input: 100, Output: 20, Cached: 30},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{
		ID: "a2", RunID: "run", ActivationID: "act", State: "succeeded",
		StartedAt: now, FinishedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	NewRouter(s, nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/runs/run?summary=true", nil))
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Overview struct {
			Tokens map[string]any `json:"tokens"`
			Usage  struct {
				Tokens          state.UsageTotal           `json:"tokens"`
				TokenDimensions state.TokenDimensionTotals `json:"tokenDimensions"`
			} `json:"usage"`
		} `json:"overview"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	tokens := body.Overview.Tokens
	if tokens["input"] != float64(100) || tokens["output"] != float64(20) || tokens["cached"] != float64(30) {
		t.Fatalf("partial overview tokens=%v", tokens)
	}
	if body.Overview.Usage.Tokens.Observed == nil || *body.Overview.Usage.Tokens.Observed != 120 || body.Overview.Usage.Tokens.Unknown != 1 {
		t.Fatalf("usage tokens=%+v", body.Overview.Usage.Tokens)
	}
	if body.Overview.Usage.TokenDimensions.Input.Observed == nil || *body.Overview.Usage.TokenDimensions.Input.Observed != 100 {
		t.Fatalf("input dimension=%+v", body.Overview.Usage.TokenDimensions.Input)
	}
}
